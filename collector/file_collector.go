package collector

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"fops-agent/config"
	"fops-agent/output"

	"github.com/farseer-go/fs/flog"
	"github.com/fsnotify/fsnotify"
)

// FileCollector 文件采集器
type FileCollector struct {
	name          string
	containerID   string
	containerName string
	appName       string
	watchDir      string
	fileExt       string
	pid           int

	watcher *fsnotify.Watcher
	store   *FileStore
	output  output.Output

	// 文件状态管理
	filesMu sync.RWMutex
	files   map[string]*fileState // filePath -> state

	// 当前写入的文件
	currentFileMu sync.Mutex
	currentFile   string

	// 待删除的文件（已输出成功，等待新文件出现后删除）
	pendingDeleteMu sync.Mutex
	pendingDelete   map[string]bool

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// fileState 文件状态
type fileState struct {
	path       string
	size       int64
	modTime    time.Time
	offset     int64
	status     fileStatus
	outputDone bool // 输出成功标记
}

type fileStatus int

const (
	statusWriting  fileStatus = iota // 正在写入
	statusFinished                   // 已完结
	statusRead                       // 已读完
	statusOutput                     // 已输出
)

// NewFileCollector 创建文件采集器
func NewFileCollector(
	name string,
	containerID string,
	containerName string,
	appName string,
	watchDir string,
	fileExt string,
	pid int,
	store *FileStore,
	out output.Output,
) *FileCollector {
	return &FileCollector{
		name:          name,
		containerID:   containerID,
		containerName: containerName,
		appName:       appName,
		watchDir:      watchDir,
		fileExt:       fileExt,
		pid:           pid,
		store:         store,
		output:        out,
		files:         make(map[string]*fileState),
		pendingDelete: make(map[string]bool),
	}
}

// Name 采集器名称
func (c *FileCollector) Name() string {
	return c.name
}

// Start 启动采集器
func (c *FileCollector) Start(ctx context.Context) error {
	c.ctx, c.cancel = context.WithCancel(ctx)

	// output 由 FileCollectorManager 统一管理，这里不再启动

	// 构建实际监听路径：/proc/PID/root/watchDir
	actualPath := c.getActualPath()

	// 检查目录是否存在
	if _, err := os.Stat(actualPath); os.IsNotExist(err) {
		return fmt.Errorf("监听目录不存在: %s", actualPath)
	}

	// 创建 fsnotify watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("创建 fsnotify watcher 失败: %w", err)
	}
	c.watcher = watcher

	// 添加目录监听
	if err := watcher.Add(actualPath); err != nil {
		watcher.Close()
		return fmt.Errorf("添加目录监听失败: %w", err)
	}

	flog.Infof("[%s:%s] 开始监听目录: %s", c.containerName, c.name, actualPath)

	// 启动时扫描已有文件
	c.scanExistingFiles(actualPath)

	// 启动事件处理协程
	c.wg.Add(1)
	go c.handleEvents()

	return nil
}

// Stop 停止采集器
func (c *FileCollector) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
	if c.watcher != nil {
		c.watcher.Close()
	}
	// output 由 FileCollectorManager 统一管理，这里不再停止
}

// getActualPath 获取实际监听路径
func (c *FileCollector) getActualPath() string {
	// 替换 {app} 占位符
	path := strings.Replace(c.watchDir, "{app}", c.appName, -1)
	// 使用 ProcPrefix（自动检测 Docker 或主机环境）
	return filepath.Join(config.ProcPrefix, fmt.Sprintf("%d", c.pid), "root", path)
}

// scanExistingFiles 扫描已有文件
func (c *FileCollector) scanExistingFiles(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		flog.Warningf("[%s:%s] 扫描目录失败: %v", c.containerName, c.name, err)
		return
	}

	// 收集文件信息
	var fileInfos []struct {
		path    string
		size    int64
		modTime time.Time
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		// 检查文件扩展名
		if !strings.HasSuffix(entry.Name(), "."+c.fileExt) {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}

		fileInfos = append(fileInfos, struct {
			path    string
			size    int64
			modTime time.Time
		}{path, info.Size(), info.ModTime()})
	}

	if len(fileInfos) == 0 {
		return
	}

	// 按修改时间排序
	sort.Slice(fileInfos, func(i, j int) bool {
		return fileInfos[i].modTime.Before(fileInfos[j].modTime)
	})

	// 最新的文件是当前写入的文件
	currentFilePath := fileInfos[len(fileInfos)-1].path

	flog.Infof("[%s:%s] 扫描到 %d 个文件，当前: %s",
		c.containerName, c.name, len(fileInfos), filepath.Base(currentFilePath))

	// 处理所有文件
	for _, fi := range fileInfos {
		isCurrent := fi.path == currentFilePath

		// 获取偏移量
		offset := c.store.Get(c.containerID, c.name, fi.path)

		state := &fileState{
			path:    fi.path,
			size:    fi.size,
			modTime: fi.modTime,
			status:  statusWriting,
		}

		if offset != nil {
			state.offset = offset.Offset
			state.size = offset.FileSize
		}

		if !isCurrent {
			// 非当前文件，标记为已完结
			state.status = statusFinished
		}

		c.filesMu.Lock()
		c.files[fi.path] = state
		c.filesMu.Unlock()

		if isCurrent {
			c.currentFileMu.Lock()
			c.currentFile = fi.path
			c.currentFileMu.Unlock()
		}

		// 读取文件内容
		c.readFile(state, isCurrent)
	}
}

// handleEvents 处理 fsnotify 事件
func (c *FileCollector) handleEvents() {
	defer c.wg.Done()

	for {
		select {
		case <-c.ctx.Done():
			return

		case event, ok := <-c.watcher.Events:
			if !ok {
				return
			}
			c.processEvent(event)

		case err, ok := <-c.watcher.Errors:
			if !ok {
				return
			}
			flog.Warningf("[%s:%s] fsnotify 错误: %v", c.containerName, c.name, err)
		}
	}
}

// processEvent 处理单个事件
func (c *FileCollector) processEvent(event fsnotify.Event) {
	// 只处理目标扩展名的文件
	if !strings.HasSuffix(event.Name, "."+c.fileExt) {
		return
	}

	switch {
	case event.Op&fsnotify.Create == fsnotify.Create:
		c.handleFileCreate(event.Name)

	case event.Op&fsnotify.Write == fsnotify.Write:
		c.handleFileWrite(event.Name)
	}
}

// handleFileCreate 处理文件创建事件
func (c *FileCollector) handleFileCreate(filePath string) {
	flog.Infof("[%s:%s] 新文件: %s", c.containerName, c.name, filepath.Base(filePath))

	// 标记上一个文件为已完结
	c.currentFileMu.Lock()
	oldFile := c.currentFile
	c.currentFile = filePath
	c.currentFileMu.Unlock()

	if oldFile != "" {
		c.filesMu.Lock()
		if state, ok := c.files[oldFile]; ok {
			state.status = statusFinished
		}
		c.filesMu.Unlock()

		// 读取旧文件剩余内容
		c.filesMu.RLock()
		if state, ok := c.files[oldFile]; ok {
			c.readFile(state, false)
		}
		c.filesMu.RUnlock()
	}

	// 添加新文件状态
	info, err := os.Stat(filePath)
	if err != nil {
		flog.Warningf("[%s:%s] 获取文件信息失败: %v", c.containerName, c.name, err)
		return
	}

	state := &fileState{
		path:    filePath,
		size:    info.Size(),
		modTime: info.ModTime(),
		offset:  0,
		status:  statusWriting,
	}

	c.filesMu.Lock()
	c.files[filePath] = state
	c.filesMu.Unlock()

	// 检查是否有待删除的文件可以删除
	c.tryDeletePendingFiles()
}

// handleFileWrite 处理文件写入事件
func (c *FileCollector) handleFileWrite(filePath string) {
	c.filesMu.RLock()
	state, ok := c.files[filePath]
	c.filesMu.RUnlock()

	if !ok {
		// 文件不在跟踪列表中，可能是新文件
		return
	}

	// 只处理当前写入的文件
	c.currentFileMu.Lock()
	isCurrent := c.currentFile == filePath
	c.currentFileMu.Unlock()

	if !isCurrent {
		return
	}

	// 获取最新文件信息
	info, err := os.Stat(filePath)
	if err != nil {
		return
	}

	// 检查文件是否被 rotate（变小了）
	if info.Size() < state.size {
		flog.Infof("[%s:%s] 文件rotate: %s", c.containerName, c.name, filepath.Base(filePath))
		state.offset = 0
	}

	state.size = info.Size()
	state.modTime = info.ModTime()

	// 读取新增内容
	c.readFile(state, true)
}

// readFile 读取文件内容
func (c *FileCollector) readFile(state *fileState, isCurrent bool) {
	// 打开文件
	file, err := os.Open(state.path)
	if err != nil {
		flog.Warningf("[%s:%s] 打开文件失败: %v", c.containerName, c.name, err)
		return
	}
	defer file.Close()

	// 定位到偏移量位置（字节）
	if state.offset > 0 {
		_, err = file.Seek(state.offset, 0)
		if err != nil {
			flog.Warningf("[%s:%s] 定位文件失败: %v", c.containerName, c.name, err)
			return
		}
	}

	// 使用 bufio.Reader 读取，避免 Scanner 的行长度限制
	var lines []string
	var bytesRead int64

	reader := bufio.NewReader(file)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break // EOF 或其他错误
		}

		// 统计读取的字节数（原始字节，包含换行符）
		bytesRead += int64(len(line))

		// 去掉换行符
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")

		if line != "" {
			lines = append(lines, line)
		}
	}

	if len(lines) == 0 {
		return
	}

	// 更新偏移量（字节）
	state.offset += bytesRead

	// 保存偏移量
	c.store.Set(&FileOffset{
		ContainerID:    c.containerID,
		ContainerName:  c.containerName,
		AppName:        c.appName,
		CollectorName:  c.name,
		FilePath:       state.path,
		Offset:         state.offset,
		FileSize:       state.size,
		LastReadTime:   time.Now(),
		LastModifyTime: state.modTime,
	})

	// 发送到输出
	if c.output != nil {
		data := &output.Data{
			ContainerID:   c.containerID,
			ContainerName: c.containerName,
			AppName:       c.appName,
			CollectorName: c.name,
			FilePath:      state.path,
			Lines:         lines,
			FileSize:      state.size,
		}

		c.output.Write(data)
	}

	flog.Debugf("[%s:%s] %s 读取 %d 行", c.containerName, c.name, filepath.Base(state.path), len(lines))
}

// OnOutputSuccess 输出成功回调
func (c *FileCollector) OnOutputSuccess(filePath string) {
	c.filesMu.Lock()
	if state, ok := c.files[filePath]; ok {
		state.outputDone = true
	}
	c.filesMu.Unlock()

	// 添加到待删除列表
	c.pendingDeleteMu.Lock()
	c.pendingDelete[filePath] = true
	c.pendingDeleteMu.Unlock()

	// 尝试删除
	c.tryDeletePendingFiles()
}

// tryDeletePendingFiles 尝试删除待删除的文件
func (c *FileCollector) tryDeletePendingFiles() {
	// 检查是否有当前写入的文件
	c.currentFileMu.Lock()
	hasCurrentFile := c.currentFile != ""
	currentFile := c.currentFile
	c.currentFileMu.Unlock()

	if !hasCurrentFile {
		return // 没有新文件，不能删除
	}

	// 遍历待删除列表
	c.pendingDeleteMu.Lock()
	defer c.pendingDeleteMu.Unlock()

	for filePath := range c.pendingDelete {
		// 不能删除当前写入的文件
		if filePath == currentFile {
			continue
		}

		// 删除文件
		if err := os.Remove(filePath); err != nil {
			flog.Warningf("[%s:%s] 删除文件失败: %v", c.containerName, c.name, err)
			continue
		}

		// 删除偏移量记录
		c.store.Delete(c.containerID, c.name, filePath)

		// 从状态中移除
		c.filesMu.Lock()
		delete(c.files, filePath)
		c.filesMu.Unlock()

		// 从待删除列表移除
		delete(c.pendingDelete, filePath)

		flog.Infof("[%s:%s] 删除文件: %s", c.containerName, c.name, filepath.Base(filePath))
	}
}
