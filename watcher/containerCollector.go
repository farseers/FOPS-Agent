package watcher

import (
	"context"
	"sync"

	"fops-agent/collector"
	"fops-agent/config"
	"fops-agent/output"

	"github.com/farseer-go/fs/flog"
)

// ContainerCollector 容器文件监视器
type ContainerCollector struct {
	containerID   string
	containerName string
	pid           int
	collectors    []collector.Collector
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
}

// NewContainerCollector 创建容器文件监视器
func NewContainerCollector(containerID, containerName string, pid int, cfg *config.Config, outputs map[string]output.Output) (*ContainerCollector, error) {
	ctx, cancel := context.WithCancel(context.Background())
	w := &ContainerCollector{
		containerID:   containerID,
		containerName: containerName,
		pid:           pid,
		ctx:           ctx,
		cancel:        cancel,
	}
	flog.Infof("[ContainerCollector] 创建: %s, PID: %d", containerName, pid)
	// 遍历需要采集的目录,如:/var/log/flog/{app}/ /var/log/linkTrace/{app}/
	for _, cc := range cfg.Collectors {
		// 使用全局上传器
		out := outputs[cc.Name]
		col := collector.NewFileCollector(cc.Name, containerID, containerName, cc.WatchDir, cc.FileExt, pid, out)
		w.collectors = append(w.collectors, col)
	}
	return w, nil
}

// Start 启动容器监视器
func (w *ContainerCollector) Start() {
	for _, col := range w.collectors {
		go col.Start(w.ctx)
	}
}

// Stop 停止容器监视器
func (w *ContainerCollector) Stop() {
	w.cancel()
	for _, col := range w.collectors {
		col.Stop()
	}
	w.wg.Wait()
	flog.Infof("[ContainerCollector] 已停止: %s", w.containerName)
}

// ContainerID 获取容器ID
func (w *ContainerCollector) ContainerID() string {
	return w.containerID
}
