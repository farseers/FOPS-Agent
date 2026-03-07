package collector

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"time"

	"github.com/farseer-go/collections"
	"github.com/farseer-go/docker"
	"github.com/farseer-go/fs/flog"
)

// CollectFile 文件
type CollectFile struct {
	// Container 容器信息
	Container *docker.Container
	// Lines 文件内容行
	Lines collections.List[[]byte]
}

// Collector 采集器
type Collector struct {
	client        *docker.Client           // Docker客户端
	interval      time.Duration            // 采集间隔
	maxConcurrent int                      // 最大并发数
	filePath      string                   // 采集的文件路径（相对于容器内）
	fileExtension string                   // 文件扩展名
	ignoreNames   collections.List[string] // 忽略的容器名称
	store         *FileStore

	// 事件回调
	onLogFile func(logFile *CollectFile) error

	// 控制
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewCollector 创建采集器
func NewCollector(filePath string, fileExtension string, interval time.Duration, maxConcurrent int, ignoreNames []string) *Collector {
	store, _ := NewFileStore(filePath)
	return &Collector{
		client:        docker.NewClient(),
		interval:      interval,
		maxConcurrent: maxConcurrent,
		filePath:      filePath,
		fileExtension: fileExtension,
		ignoreNames:   collections.NewList(ignoreNames...),
		store:         store,
	}
}

// OnLogFile 设置文件回调（整个文件回调一次）
func (c *Collector) OnLogFile(fn func(logFile *CollectFile) error) {
	c.onLogFile = fn
}

// Start 启动采集器
func (c *Collector) Start() {
	c.ctx, c.cancel = context.WithCancel(context.Background())

	c.wg.Add(1)
	go c.run()
}

// Stop 停止采集器
func (c *Collector) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
}

// run 采集循环
func (c *Collector) run() {
	defer c.wg.Done()

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	// 立即执行一次
	c.collect()

	for {
		select {
		case <-c.ctx.Done():
			flog.Infof("Collector结束了")
			return
		case <-ticker.C:
			c.collect()
		}
	}
}

// collect 执行一次采集
func (c *Collector) collect() {
	startTime := time.Now()
	defer func() {
		flog.Infof("[采集完成] 耗时: %v", time.Since(startTime))
	}()

	// 获取所有容器
	containers, err := c.client.Container.List("", nil)
	if err != nil {
		flog.Warningf("获取容器列表失败: %v", err)
		return
	}

	// 过滤容器
	containers.RemoveAll(func(item docker.Container) bool {
		return c.ignoreNames.Contains(item.Name)
	})

	// 并行采集
	containers.Parallel(c.maxConcurrent, func(cnt *docker.Container) {
		c.collectContainer(cnt)
	})
}

// collectContainer 采集单个容器文件
func (c *Collector) collectContainer(container *docker.Container) {
	ctx, cancel := context.WithTimeout(c.ctx, 60*time.Second)
	defer cancel()

	//flog.Infof("正在读取%s的文件", container.Name)
	// 获取容器内的文件列表（已排除current.*）
	files, err := c.client.Container.ListLogFiles(container.ID, c.filePath, c.fileExtension, 100, ctx)
	if err != nil {
		flog.Warningf("[跳过] 容器 %s 获取文件列表失败: %v", container.Name, err)
		return
	}

	// 移除当前文件
	files.RemoveAll(func(file docker.FileInfo) bool {
		return strings.HasPrefix(file.Name, "current.")
	})

	if files.Count() == 0 || strings.Contains(files.First().Name, "no such file") {
		//flog.Infof("%s,未读取到文件", container.Name)
		return
	}

	// 按修改时间排序（旧的先处理）
	files = files.OrderBy(func(item docker.FileInfo) any {
		return item.ModTime
	}).ToList()

	// 读取最后一个文件地址(后续不允许删除该文件)
	lastFilePath := files.Last().Path
	//flog.Infof("[发现] 容器 %s 有 %d 个待采集文件", container.Name, files.Count())

	// 批量读取文件内容
	lstFileBatch := c.collectFiles(ctx, container, files)
	if lstFileBatch.Count() == 0 {
		return
	}

	// 回调处理文件
	if c.onLogFile != nil {
		// 汇总所有文件内容
		lines := collections.NewList[[]byte]()
		lstFileBatch.Foreach(func(fileBatch *FileBatch) {
			lines.AddList(fileBatch.line)
		})

		if err := c.onLogFile(&CollectFile{Container: container, Lines: lines}); err != nil {
			flog.Errorf("[上传失败] 容器 %s: %v", container.Name, err)
			return
		}
	}

	// 上传成功，删除已读取的文件
	lstFileBatch.For(func(index int, fileBatch *FileBatch) {
		// 更新偏移量
		fileBatch.off.Offset += int64(len(fileBatch.content))
		fileBatch.off.FileSize = fileBatch.file.Size
		fileBatch.off.LastReadTime = time.Now()
		fileBatch.off.LastModifyTime = fileBatch.file.ModTime
		c.saveOffset(fileBatch.off)

		// 不是最后一个文件,可以删除
		if fileBatch.file.Path != lastFilePath {
			c.client.Container.DeleteFile(container.ID, fileBatch.file.Path, ctx)
			// 删除偏移量记录
			c.store.Delete(container.ID, fileBatch.file.Path)
		}
	})
}

// FileBatch 文件批次
type FileBatch struct {
	file    *docker.FileInfo         // 文件
	content []byte                   // 从容器中读取的原始内容
	line    collections.List[[]byte] // 换行后的内容
	off     *FileOffset              // 文件的偏移
}

// collectFiles 批量采集文件
func (c *Collector) collectFiles(ctx context.Context, container *docker.Container, files collections.List[docker.FileInfo]) collections.List[FileBatch] {
	lstFileBatch := collections.NewList[FileBatch]()
	files.Foreach(func(file *docker.FileInfo) {
		// 获取偏移量
		off := c.getOffset(container.ID, file.Path)
		if off == nil {
			off = &FileOffset{ContainerID: container.ID, ContainerName: container.Name, FilePath: file.Path, Offset: 0, FileSize: 0}
		}

		// 检查文件是否有新内容
		if file.Size <= off.Offset {
			return
		}

		// 检查文件是否被rotate（文件变小了）
		if file.Size < off.FileSize {
			flog.Infof("[检测] 文件 %s 可能被rotate，从头开始读取\n", file.Path)
			off.Offset = 0
		}

		// 增量读取文件内容
		content := c.client.Container.ReadFileFromContainerByOffset(container.ID, file.Path, off.Offset, ctx)
		if len(content) == 0 {
			return
		}

		// 解析日志行
		lines := c.parseLogLines(content)
		if lines.Count() == 0 {
			return
		}

		flog.Infof("[采集] 容器 %s 文件 %s (%d 字节, %d 行)\n", container.Name, file.Name, len(content), lines.Count())

		lstFileBatch.Add(FileBatch{file: file, content: content, line: lines, off: off})
	})

	return lstFileBatch
}

// parseLogLines 解析文件内容行（按\n分割）
func (c *Collector) parseLogLines(content []byte) collections.List[[]byte] {
	// 按换行符分割
	lines := bytes.Split(content, []byte("\n"))

	// 过滤空行
	lst := collections.NewList[[]byte]()
	for _, line := range lines {
		if len(line) > 0 {
			lst.Add(line)
		}
	}

	return lst
}

// getOffset 获取文件偏移量
func (c *Collector) getOffset(containerID, filePath string) *FileOffset {
	return c.store.Get(containerID, filePath)
}

// saveOffset 保存文件偏移量
func (c *Collector) saveOffset(off *FileOffset) {
	c.store.Set(off)
}

// CleanOldOffsets 清理过期的偏移量记录
func (c *Collector) CleanOldOffsets() {
	// 清理7天前的记录
	c.store.Clean(time.Now().AddDate(0, 0, -7))
}
