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

	// 事件回调
	onLogFile func(logFile *CollectFile) error

	// 控制
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewCollector 创建采集器
func NewCollector(filePath string, fileExtension string, interval time.Duration, maxConcurrent int, ignoreNames []string) *Collector {
	client := docker.NewClient()
	return &Collector{
		client:        client,
		interval:      interval,
		maxConcurrent: maxConcurrent,
		filePath:      filePath,
		fileExtension: fileExtension,
		ignoreNames:   collections.NewList(ignoreNames...),
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

	containers.Parallel(c.maxConcurrent, func(cnt *docker.Container) {
		// 排除忽略的容器
		if !c.ignoreNames.Contains(cnt.Name) {
			c.collectContainer(cnt)
		}
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

	//flog.Infof("[发现] 容器 %s 有 %d 个待采集文件", container.Name, files.Count())

	// 批量读取文件内容
	batch := c.collectFiles(ctx, container, files)
	if batch.Lines.Count() == 0 {
		return
	}

	// 回调处理文件
	if c.onLogFile != nil {
		if err := c.onLogFile(&CollectFile{Container: container, Lines: batch.Lines}); err != nil {
			flog.Errorf("[上传失败] 容器 %s: %v", container.Name, err)
			return
		}
	}

	// 上传成功，删除已读取的文件
	batch.Files.Foreach(func(file *docker.FileInfo) {
		c.client.Container.DeleteFile(container.ID, file.Path, ctx)
	})
}

// FileBatch 文件批次
type FileBatch struct {
	Files collections.List[docker.FileInfo]
	Lines collections.List[[]byte]
}

// collectFiles 批量采集文件
func (c *Collector) collectFiles(ctx context.Context, container *docker.Container, files collections.List[docker.FileInfo]) *FileBatch {
	batch := &FileBatch{
		Files: collections.NewList[docker.FileInfo](),
		Lines: collections.NewList[[]byte](),
	}

	files.Foreach(func(file *docker.FileInfo) {
		content, err := c.client.Container.ReadFileFromContainer(container.ID, file.Path, ctx)
		if err != nil {
			flog.Warningf("[读取失败] 容器: %s ,%s: %v", container.Name, file.Path, err)
			return
		}

		lines := c.parseLogLines(content)
		if lines.Count() == 0 {
			return
		}

		batch.Files.Add(*file)
		batch.Lines.AddList(lines)
		flog.Infof("[采集] 容器 %s 文件 %s (%d 行)", container.Name, file.Name, lines.Count())
	})

	return batch
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
