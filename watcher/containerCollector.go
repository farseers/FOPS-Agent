package watcher

import (
	"context"

	"fops-agent/collector"
	"fops-agent/config"
	"fops-agent/container"
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
}

// NewContainerCollector 创建容器文件监视器
func NewContainerCollector(containerID, containerName string, pid int, matchNames []string, cfg *config.Config, outputs map[string]output.Output) (*ContainerCollector, error) {
	ctx, cancel := context.WithCancel(context.Background())
	w := &ContainerCollector{
		containerID:   containerID,
		containerName: containerName,
		pid:           pid,
		ctx:           ctx,
		cancel:        cancel,
	}
	// 遍历需要采集的目录,如:/var/log/flog/{app}/ /var/log/linkTrace/{app}/
	for _, cc := range cfg.Collectors {
		if !cc.RunsInContainer() || !container.MatchContainerNames(matchNames, cc.ContainerNames) {
			continue
		}
		// 使用全局上传器
		out := outputs[cc.Name]
		col := collector.NewFileCollector(cc.Name, containerID, containerName, cc.AppName, cc.WatchDir, cc.FileExt, pid, collector.WatchPathModeContainer, cc.SerializeType, cc.BufferSizeMB*1024*1024, out)
		w.collectors = append(w.collectors, col)
	}
	if len(w.collectors) > 0 {
		flog.Infof("[ContainerCollector] 创建: %s, PID: %d, 采集器: %d", containerName, pid, len(w.collectors))
	}
	return w, nil
}

// HasCollectors 判断容器是否有需要启动的采集器
func (w *ContainerCollector) HasCollectors() bool {
	return len(w.collectors) > 0
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
	flog.Infof("[ContainerCollector] 已停止: %s", w.containerName)
}

// ContainerID 获取容器ID
func (w *ContainerCollector) ContainerID() string {
	return w.containerID
}
