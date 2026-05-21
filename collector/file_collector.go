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
	name           string            // 配置名称
	containerID    string            // 容器ID
	containerName  string            // 容器名称
	appName        string            // 应用名称
	watchDir       string            // 配置文件定义的目录
	actualPath     string            // 实际要监听的目录
	fileExt        string            // 监听的文件扩展名
	serializeType  string            // 序列化格式（json 或 messagePack）
	readBatchBytes int64             // 单批读取大小上限
	pid            int               // 容器在主机的进程ID
	watcher        *fsnotify.Watcher // 文件监听客户端
	output         output.Output     // 上传器

	// 文件状态管理
	filesMu sync.RWMutex
	files   map[string]*fileState // filePath -> state

	dirtyMu    sync.Mutex          // 保护 dirtyFiles 的并发读写
	dirtyFiles map[string]struct{} // 待合并读取的文件集合
	dirtyCh    chan struct{}       // dirty 通知通道，容量为1用于合并高频事件

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
func NewFileCollector(name string, containerID string, containerName string, watchDir string, fileExt string, pid int, serializeType string, readBatchBytes int64, out output.Output) *FileCollector {
	if readBatchBytes <= 0 {
		readBatchBytes = 10 * 1024 * 1024
	}
	fc := &FileCollector{
		name:           name,
		containerID:    containerID,
		containerName:  containerName,
		watchDir:       watchDir,
		fileExt:        fileExt,
		serializeType:  serializeType,
		readBatchBytes: readBatchBytes,
		pid:            pid,
		output:         out,
		files:          make(map[string]*fileState),
		dirtyFiles:     make(map[string]struct{}),
		dirtyCh:        make(chan struct{}, 1),
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
	c.wg.Add(2)

	// 处理 fsnotify 事件
	go c.handleEvents()

	// 合并处理文件变更，并定时兜底扫描防止 fsnotify 事件丢失
	go c.dirtyWorker()

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

// dirtyWorker 合并处理文件变更，并定时兜底扫描防止 fsnotify 事件丢失
func (c *FileCollector) dirtyWorker() {
	defer c.wg.Done()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.dirtyCh:
			c.readDirtyFiles()
		case <-ticker.C:
			// 扫描目录中有新增内容的文件，作为事件丢失时的兜底
			c.scanChangedFiles()
			// 读取当前已标记 dirty 的文件
			c.readDirtyFiles()
		}
	}
}

// markDirty 标记文件需要读取，重复标记会自动合并
func (c *FileCollector) markDirty(filePath string) {
	c.dirtyMu.Lock()
	c.dirtyFiles[filePath] = struct{}{}
	c.dirtyMu.Unlock()

	select {
	case c.dirtyCh <- struct{}{}:
	default:
	}
}

// readDirtyFiles 读取当前已标记 dirty 的文件
func (c *FileCollector) readDirtyFiles() {
	filePaths := c.takeDirtyFiles()
	for _, filePath := range filePaths {
		info, err := os.Stat(filePath)
		if err != nil {
			continue
		}
		fi := fileInfo{path: filePath, size: info.Size(), modTime: info.ModTime()}
		if !c.refreshFileState(fi) {
			continue
		}

		c.filesMu.RLock()
		state, ok := c.files[filePath]
		c.filesMu.RUnlock()
		if !ok {
			continue
		}
		c.readFile(state)
	}
}

// takeDirtyFiles 取出并清空本轮待读取文件
func (c *FileCollector) takeDirtyFiles() []string {
	c.dirtyMu.Lock()
	defer c.dirtyMu.Unlock()

	filePaths := make([]string, 0, len(c.dirtyFiles))
	for filePath := range c.dirtyFiles {
		filePaths = append(filePaths, filePath)
	}
	c.dirtyFiles = make(map[string]struct{})
	sort.Strings(filePaths)
	return filePaths
}

// scanChangedFiles 扫描目录中有新增内容的文件，作为事件丢失时的兜底
func (c *FileCollector) scanChangedFiles() {
	fileInfos := c.getSortFileList()
	for _, fi := range fileInfos {
		if c.refreshFileState(fi) {
			c.markDirty(fi.path)
		}
	}
}

// refreshFileState 刷新文件大小和修改时间，返回是否存在未读内容
func (c *FileCollector) refreshFileState(fi fileInfo) bool {
	c.filesMu.Lock()
	state, ok := c.files[fi.path]
	if !ok {
		state = &fileState{path: fi.path}
		c.files[fi.path] = state
		flog.Warningf("[%s:%s] 文件不在跟踪列表,重新加入: %s", c.containerName, c.name, fi.path)
	}
	c.filesMu.Unlock()

	state.mu.Lock()
	defer state.mu.Unlock()

	if fi.size < state.size || fi.size < state.readOffset {
		flog.Infof("[%s:%s] 文件rotate: %s, 跟踪大小: %d, 实际大小: %d", c.containerName, c.name, fi.path, state.size, fi.size)
		state.readOffset = 0
	}

	state.size = fi.size
	state.modTime = fi.modTime
	return fi.size > state.readOffset
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
	c.markDirty(filePath)

	// 检查是否有待删除的文件可以删除
	c.tryDeletePendingFiles()
}

// handleFileWrite 处理文件写入事件
func (c *FileCollector) handleFileWrite(filePath string) {
	c.markDirty(filePath)
}

// readFile 读取文件内容（按序列化格式分发）
func (c *FileCollector) readFile(state *fileState) {
	if c.serializeType == "messagePack" {
		c.readMsgPackFile(state)
	} else {
		c.readJSONFile(state)
	}
}

func (c *FileCollector) flushReadBatch(state *fileState, lines [][]byte, bytesRead int64, unit string) {
	if bytesRead <= 0 {
		return
	}

	state.mu.Lock()
	state.readOffset += bytesRead
	state.mu.Unlock()

	if len(lines) == 0 {
		return
	}

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

	flog.Debugf("[%s:%s] %s 读取 %d%s, %.2f MB", c.containerName, c.name, state.path, len(lines), unit, float64(bytesRead)/1024/1024)
}

// readJSONFile 按行分批读取 JSON 文本文件
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
	// batchBytes 统计当前批次实际读取的文件字节数，用于限制单批内存并推进 readOffset。
	var batchBytes int64

	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}

		lineBytes := int64(len(line))
		batchBytes += lineBytes
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		if line != "" {
			lines = append(lines, []byte(line))
		}

		if batchBytes >= c.readBatchBytes {
			c.flushReadBatch(state, lines, batchBytes, " 行")
			lines = nil
			batchBytes = 0
		}
	}

	c.flushReadBatch(state, lines, batchBytes, " 行")
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
			offset = 0
		}
	}

	var lines [][]byte
	// batchBytes 统计当前批次实际读取的文件字节数，用于限制单批内存并推进 readOffset。
	var batchBytes int64

	const maxPayloadSize = 64 * 1024 * 1024 // 单条 payload 上限 64 MB，防止格式错误导致内存暴涨

	for {
		// 读取 4 字节长度头
		var lenBuf [4]byte
		if _, err := io.ReadFull(file, lenBuf[:]); err != nil {
			break // EOF 或不足 4 字节，等待下次写入
		}
		payloadLen := binary.BigEndian.Uint32(lenBuf[:])

		// 校验 payload 长度合法性：0 或超过上限说明文件格式不是 messagePack，跳过到文件末尾
		if payloadLen == 0 || payloadLen > maxPayloadSize {
			flog.Warningf("[%s:%s] %s 检测到非法帧长度 %d，文件可能不是 messagePack 格式，跳过到文件末尾", c.containerName, c.name, state.path, payloadLen)
			if info, statErr := file.Stat(); statErr == nil {
				state.mu.Lock()
				currentOffset := state.readOffset
				state.mu.Unlock()
				if skipBytes := info.Size() - currentOffset - batchBytes; skipBytes > 0 {
					batchBytes += skipBytes
				}
			}
			break
		}

		// 按长度精确读取 payload
		payload := make([]byte, payloadLen)
		if _, err := io.ReadFull(file, payload); err != nil {
			break // payload 尚未写完，下次继续
		}

		batchBytes += int64(4 + payloadLen)
		lines = append(lines, payload)

		if batchBytes >= c.readBatchBytes {
			c.flushReadBatch(state, lines, batchBytes, " 条msgpack记录")
			lines = nil
			batchBytes = 0
		}
	}

	c.flushReadBatch(state, lines, batchBytes, " 条msgpack记录")
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
			canDelete := fileState.readOffset > 0 && fileState.readOffset == fileState.uploadOffset && fileInfos[i].size <= fileState.readOffset
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
