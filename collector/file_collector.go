package collector

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
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
	name          string            // 配置名称
	containerID   string            // 容器ID
	containerName string            // 容器名称
	appName       string            // 应用名称
	watchDir      string            // 配置文件定义的目录
	actualPath    string            // 实际要监听的目录
	fileExt       string            // 监听的文件扩展名
	serializeType string            // 序列化格式（json 或 messagePack）
	pid           int               // 容器在主机的进程ID
	watcher       *fsnotify.Watcher // 文件监听客户端
	output        output.Output     // 上传器

	// 文件状态管理
	filesMu sync.RWMutex
	files   map[string]*fileState // filePath -> state

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// fileState 文件状态
type fileState struct {
	mu           sync.Mutex // 保护 readOffset / uploadOffset 的并发读写
	path         string
	modTime      time.Time // 文件修改时间
	size         int64     // 文件大小
	readOffset   int64     // 读取时的偏移量
	uploadOffset int64     // 上传时的偏移量
}

// 文件属性
type fileInfo struct {
	path    string    //路径
	size    int64     // 大小
	modTime time.Time // 修改时间
}

// NewFileCollector 创建文件采集器
func NewFileCollector(name string, containerID string, containerName string, watchDir string, fileExt string, pid int, serializeType string, out output.Output) *FileCollector {
	fc := &FileCollector{
		name:          name,
		containerID:   containerID,
		containerName: containerName,
		watchDir:      watchDir,
		fileExt:       fileExt,
		serializeType: serializeType,
		pid:           pid,
		output:        out,
		files:         make(map[string]*fileState),
	}
	// 注册回调到全局上传器
	if out != nil {
		out.RegisterCallback(fc.OnOutputSuccess)
	}
	return fc
}

// Name 采集器名称
func (c *FileCollector) Name() string {
	return c.name
}

// Start 启动采集器 (通过 Docker event 触发的，只会被调用一次)
func (c *FileCollector) Start(ctx context.Context) {
	c.ctx, c.cancel = context.WithCancel(ctx)
	// 尝试监听
	if c.tryWatch() {
		return
	}

	// 先等30秒,等应用启动完毕
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if c.tryWatch() {
				return
			}
		}
	}
}

// 尝试监听
func (c *FileCollector) tryWatch() bool {
	// 1. 获取应用名称
	var actualPath string
	actualPath, c.appName = c.detectAppName()
	if c.appName == "" {
		return false
	}

	// 2. 构建实际监听路径：/proc/1000/root//var/log/linkTrace/应用名称/
	c.actualPath = filepath.Join(actualPath, c.appName)

	// 3. 尝试启动监控
	return c.startWatching()
}

// startWatching 启动目录监控
func (c *FileCollector) startWatching() bool {
	// 检查目录是否存在
	if _, err := os.Stat(c.actualPath); os.IsNotExist(err) {
		flog.Warningf("[%s:%s] 监听目录不存在: %s, 稍候再试", c.containerName, c.name, c.actualPath)
		return false
	}

	// 创建 fsnotify watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		flog.Warningf("[%s:%s] 创建 fsnotify watcher 失败: %s, 稍候再试", c.containerName, c.name, err.Error())
		return false
	}

	// 添加目录监听
	if err := watcher.Add(c.actualPath); err != nil {
		watcher.Close()
		flog.Warningf("[%s:%s] 添加目录监听失败: %s, 稍候再试", c.containerName, c.name, err.Error())
		return false
	}

	c.watcher = watcher
	flog.Infof("[%s:%s] 监听目录: %s", c.containerName, c.name, c.actualPath)

	// 启动时扫描已有文件
	c.scanExistingFiles()

	// 启动事件处理协程
	c.wg.Add(1)
	go c.handleEvents()

	return true
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
}

// detectAppName 从目录检测应用名称
func (w *FileCollector) detectAppName() (string, string) {
	// w.watchDir = /var/log/linkTrace/{app}/
	if !strings.Contains(w.watchDir, "{app}") {
		return "", ""
	}

	// parentDir = /var/log/linkTrace
	parentDir := strings.TrimSuffix(filepath.Dir(strings.Replace(w.watchDir, "{app}/", "", -1)), "/")
	// actualPath = /proc/1000/root/var/log/linkTrace
	actualPath := filepath.Join(config.ProcPrefix, fmt.Sprintf("%d", w.pid), "root", parentDir)
	// 读取目录
	entries, err := os.ReadDir(actualPath)
	if err != nil {
		return actualPath, ""
	}

	var firstDir string
	for _, entry := range entries {
		if entry.IsDir() {
			dirName := entry.Name()
			// 先使用名称匹配
			if strings.EqualFold(dirName, w.containerName) {
				return actualPath, dirName
			}
			if len(firstDir) == 0 {
				firstDir = dirName
			}
		}
	}
	// 返回第1个目录.(理论只会有1个目录存在)
	return actualPath, firstDir
}

// scanExistingFiles 扫描已有文件
func (c *FileCollector) scanExistingFiles() {
	// 收集文件信息
	fileInfos := c.getSortFileList()

	if len(fileInfos) == 0 {
		flog.Infof("[%s:%s] 目录: %s, 扫描到 %d 个文件", c.containerName, c.name, c.actualPath, len(fileInfos))
		return
	}

	flog.Infof("[%s:%s] %s, 扫描到 %d 个文件, 最新的文件为: %s, 跟踪大小: %d", c.containerName, c.name, c.actualPath, len(fileInfos), filepath.Base(fileInfos[0].path), fileInfos[0].size)

	// 处理所有文件
	for _, fi := range fileInfos {
		state := &fileState{
			path:    fi.path,
			size:    fi.size,
			modTime: fi.modTime,
		}

		c.filesMu.Lock()
		c.files[fi.path] = state
		c.filesMu.Unlock()

		// 读取文件内容
		c.readFile(state)
	}
}

// handleEvents 处理 fsnotify 事件
func (c *FileCollector) handleEvents() {
	defer c.wg.Done()

	for {
		select {
		case <-c.ctx.Done():
			flog.Warningf("[watcher.Events]%s %s 退出信号", c.containerID, c.containerName)
			return

		case event, ok := <-c.watcher.Events:
			if !ok {
				flog.Warningf("[watcher.Events]%s %s %s 通道关闭", c.containerID, c.containerName, c.actualPath)
				return
			}
			c.processEvent(event)

		case err, ok := <-c.watcher.Errors:
			if !ok {
				flog.Warningf("[watcher.Errors]%s %s %s 通道关闭", c.containerID, c.containerName, c.actualPath)
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
	flog.Infof("[%s:%s] 新文件: %s", c.containerName, c.name, filePath)

	// 添加新文件状态
	info, err := os.Stat(filePath)
	if err != nil {
		flog.Warningf("[%s:%s] 获取文件信息失败: %v", c.containerName, c.name, err)
		return
	}

	c.filesMu.Lock()
	c.files[filePath] = &fileState{
		path:    filePath,
		size:    info.Size(),
		modTime: info.ModTime(),
	}
	c.filesMu.Unlock()

	// 检查是否有待删除的文件可以删除
	c.tryDeletePendingFiles()
}

// handleFileWrite 处理文件写入事件
func (c *FileCollector) handleFileWrite(filePath string) {
	// 先获取文件信息，避免在持锁期间做 IO
	info, err := os.Stat(filePath)
	if err != nil {
		return
	}

	c.filesMu.Lock()
	state, ok := c.files[filePath]
	if !ok {
		// 文件不在跟踪列表，新建 state，避免后续 nil 指针 panic
		state = &fileState{path: filePath}
		c.files[filePath] = state
		flog.Warningf("[%s:%s] 文件不在跟踪列表,重新加入: %s", c.containerName, c.name, filePath)
	}

	// 检查文件是否被 rotate（变小了）
	state.mu.Lock()
	if info.Size() < state.size {
		flog.Infof("[%s:%s] 文件rotate: %s, 跟踪大小: %d, 实际大小: %d", c.containerName, c.name, filePath, state.size, info.Size())
		state.readOffset = 0
	}

	// 重新读取文件大小和时间
	state.size = info.Size()
	state.modTime = info.ModTime()
	state.mu.Unlock()
	c.filesMu.Unlock()

	// 读取新增内容
	c.readFile(state)
}

// readFile 读取文件内容（按序列化格式分发）
func (c *FileCollector) readFile(state *fileState) {
	if c.serializeType == "messagePack" {
		c.readMsgPackFile(state)
	} else {
		c.readJSONFile(state)
	}
}

// readJSONFile 按行读取 JSON 文本文件（原有逻辑）
func (c *FileCollector) readJSONFile(state *fileState) {
	file, err := os.Open(state.path)
	if err != nil {
		flog.Warningf("[%s:%s] 打开文件失败: %v", c.containerName, c.name, err)
		return
	}
	defer file.Close()

	state.mu.Lock()
	offset := state.readOffset
	state.mu.Unlock()

	if offset > 0 {
		if _, err = file.Seek(offset, 0); err != nil {
			flog.Warningf("[%s:%s] 定位文件失败: %v, 重置偏移量为0", c.containerName, c.name, err)
			state.mu.Lock()
			state.readOffset = 0
			state.mu.Unlock()
			offset = 0
		}
	}

	var lines [][]byte
	var bytesRead int64

	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		bytesRead += int64(len(line))
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		if line != "" {
			lines = append(lines, []byte(line))
		}
	}

	if len(lines) == 0 {
		return
	}

	state.mu.Lock()
	state.readOffset += bytesRead
	state.mu.Unlock()

	if c.output != nil {
		c.output.Write(&output.Data{
			ContainerID:   c.containerID,
			ContainerName: c.containerName,
			AppName:       c.appName,
			FilePath:      state.path,
			Lines:         lines,
			CurSize:       bytesRead,
		})
	}

	flog.Debugf("[%s:%s] %s 读取 %d 行, %.2f MB", c.containerName, c.name, state.path, len(lines), float64(bytesRead)/1024/1024)
}

// readMsgPackFile 按「4字节长度前缀 + payload」分帧读取 msgpack 二进制文件。
// batchFileWriter 在 SerializeMessagePack 模式下写入格式为：
//
//	[uint32 BE: N][N bytes msgpack payload]
//
// 直接按行读取会被 payload 内部的 0x0A 字节截断，必须用此分帧协议。
func (c *FileCollector) readMsgPackFile(state *fileState) {
	file, err := os.Open(state.path)
	if err != nil {
		flog.Warningf("[%s:%s] 打开文件失败: %v", c.containerName, c.name, err)
		return
	}
	defer file.Close()

	state.mu.Lock()
	offset := state.readOffset
	state.mu.Unlock()

	if offset > 0 {
		if _, err = file.Seek(offset, 0); err != nil {
			flog.Warningf("[%s:%s] 定位文件失败: %v, 重置偏移量为0", c.containerName, c.name, err)
			state.mu.Lock()
			state.readOffset = 0
			state.mu.Unlock()
		}
	}

	var lines [][]byte
	var bytesRead int64

	for {
		// 读取 4 字节长度头
		var lenBuf [4]byte
		if _, err := io.ReadFull(file, lenBuf[:]); err != nil {
			break // EOF 或不足 4 字节，等待下次写入
		}
		payloadLen := binary.BigEndian.Uint32(lenBuf[:])

		// 按长度精确读取 payload
		payload := make([]byte, payloadLen)
		if _, err := io.ReadFull(file, payload); err != nil {
			break // payload 尚未写完，下次继续
		}

		bytesRead += int64(4 + payloadLen)
		lines = append(lines, payload)
	}

	if len(lines) == 0 {
		return
	}

	state.mu.Lock()
	state.readOffset += bytesRead
	state.mu.Unlock()

	if c.output != nil {
		c.output.Write(&output.Data{
			ContainerID:   c.containerID,
			ContainerName: c.containerName,
			AppName:       c.appName,
			FilePath:      state.path,
			Lines:         lines,
			CurSize:       bytesRead,
		})
	}

	flog.Debugf("[%s:%s] %s 读取 %d 条msgpack记录, %.2f MB", c.containerName, c.name, state.path, len(lines), float64(bytesRead)/1024/1024)
}

// OnOutputSuccess 输出成功回调
func (c *FileCollector) OnOutputSuccess(filePath string, uploadSize int64) {
	c.filesMu.RLock()
	state, ok := c.files[filePath]
	c.filesMu.RUnlock()
	if ok {
		state.mu.Lock()
		state.uploadOffset += uploadSize
		state.mu.Unlock()
	}
}

// tryDeletePendingFiles 尝试删除待删除的文件
func (c *FileCollector) tryDeletePendingFiles() {
	fileInfos := c.getSortFileList()
	if len(fileInfos) == 1 {
		return
	}

	// 从状态中移除
	c.filesMu.Lock()
	defer c.filesMu.Unlock()

	// 永远不删除第1个最新修改时间的文件
	for i := 1; i < len(fileInfos); i++ {
		if fileState, ok := c.files[fileInfos[i].path]; ok {
			// 读取了文件,且上传和读取的偏移量相同,则表示可以删除
			fileState.mu.Lock()
			canDelete := fileState.readOffset > 0 && fileState.readOffset == fileState.uploadOffset
			fileState.mu.Unlock()

			if canDelete {
				// 删除文件
				err := os.Remove(fileState.path)
				if err == nil {
					delete(c.files, fileState.path)
					flog.Infof("[%s:%s] 删除文件: %s, 最新文件: %s", c.containerName, c.name, fileState.path, fileInfos[0].path)
					continue
				}

				// 文件不存在
				if strings.Contains(err.Error(), "no such file") {
					delete(c.files, fileState.path)
					flog.Infof("[%s:%s] 文件不存在,仅删除跟踪列表: %s", c.containerName, c.name, fileState.path)
					continue
				}

				// 删除失败
				flog.Warningf("[%s:%s] 删除文件失败: %v", c.containerName, c.name, err)
			}
		}
	}
}

// 获取根据修改时间倒排的文件
func (c *FileCollector) getSortFileList() []fileInfo {
	var fileInfos []fileInfo

	// 扫描目录，获取所有文件并按修改时间排序
	entries, err := os.ReadDir(c.actualPath)
	if err != nil {
		flog.Warningf("[%s:%s] 扫描目录失败: %v", c.containerName, c.name, err)
		return fileInfos
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		// 检查文件扩展名
		if !strings.HasSuffix(entry.Name(), "."+c.fileExt) {
			continue
		}

		path := filepath.Join(c.actualPath, entry.Name())
		info, err := entry.Info()
		if err != nil {
			flog.Warningf("[%s:%s] 查看文件%s详细失败: %v", c.containerName, c.name, entry.Name(), err)
			continue
		}

		fileInfos = append(fileInfos, fileInfo{path, info.Size(), info.ModTime()})
	}

	// 如果没有找到匹配文件，直接返回空字符串
	if len(fileInfos) == 0 {
		return fileInfos
	}

	// 按修改时间从新到旧排序
	sort.Slice(fileInfos, func(i, j int) bool {
		return fileInfos[i].modTime.After(fileInfos[j].modTime)
	})

	return fileInfos
}
