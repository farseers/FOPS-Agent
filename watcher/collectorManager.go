package watcher

import (
	"context"
	"sync"
	"sync/atomic"

	"fops-agent/collector"
	"fops-agent/config"
	"fops-agent/container"
	"fops-agent/output"
	"fops-agent/uploader"

	"github.com/farseer-go/docker"
	"github.com/farseer-go/fs/flog"
)

// CollectorManager 文件监视器管理器
// 订阅容器事件，管理每个容器的文件监视器
type CollectorManager struct {
	cfg            *config.Config           // 配置文件
	outputs        map[string]output.Output // collectorName -> Output（全局共享）
	hostCollectors []collector.Collector    // 主机路径采集器
	watchers       sync.Map                 // containerID -> *ContainerCollector
	watcherCount   atomic.Int64             // 监视器数量
}

// NewCollectorManager 创建文件监视器管理器
func NewCollectorManager(cfg *config.Config) *CollectorManager {
	m := &CollectorManager{
		cfg:     cfg,
		outputs: make(map[string]output.Output),
	}

	// 预创建全局上传器（每个 collector 一个）
	for _, cc := range cfg.Collectors {
		if _, ok := m.outputs[cc.Name]; ok {
			continue
		}
		m.outputs[cc.Name] = uploader.NewHTTPUploader(cc.Name, cc.UploadURL, cfg.FopsHttpServer, cc.UploadInterval, cc.BufferSizeMB, cc.SerializeType, cc.CompressThresholdKB)
		// 启动所有上传器
		m.outputs[cc.Name].Start()
	}

	return m
}

// StartHostCollectors 启动主机路径采集器
func (m *CollectorManager) StartHostCollectors(ctx context.Context) {
	for _, cc := range m.cfg.Collectors {
		if !cc.RunsOnHost() {
			continue
		}

		out := m.outputs[cc.Name]
		col := collector.NewFileCollector(cc.Name, "host", "宿主机", cc.AppName, cc.WatchDir, cc.FileExt, 0, collector.WatchPathModeHost, cc.SerializeType, cc.BufferSizeMB*1024*1024, out)
		m.hostCollectors = append(m.hostCollectors, col)
		go col.Start(ctx)
		flog.Infof("[HostCollector] 启动: %s, 监听目录: %s", cc.Name, cc.WatchDir)
	}
}

// OnContainerAdd 容器新增事件（实现 container.Observer 接口）
func (m *CollectorManager) OnContainerAdd(c *docker.ContainerIdInspectJson) {
	containerName := container.ParseContainerName(c.Name)
	if m.cfg.ShouldIgnore(containerName) {
		flog.Debugf("[FileWatcher] 忽略容器: %s", containerName)
		return
	}
	if c.State.Pid == 0 {
		flog.Warningf("[FileWatcher] 容器 PID 为 0: %s", containerName)
		return
	}
	if _, ok := m.watchers.Load(c.ID); ok {
		return
	}
	w, err := NewContainerCollector(c.ID, containerName, c.State.Pid, m.cfg, m.outputs)
	if err != nil {
		flog.Errorf("[FileWatcher] 创建监视器失败: %v", err)
		return
	}

	w.Start()
	m.watchers.Store(c.ID, w)
	m.watcherCount.Add(1)
}

// OnContainerRemove 容器删除事件（实现 container.Observer 接口）
func (m *CollectorManager) OnContainerRemove(containerID string) {
	val, ok := m.watchers.Load(containerID)
	if !ok {
		return
	}
	w := val.(*ContainerCollector)
	w.Stop()
	m.watchers.Delete(containerID)
	m.watcherCount.Add(-1)
}

// Stop 停止所有监视器
func (m *CollectorManager) Stop() {
	for _, col := range m.hostCollectors {
		col.Stop()
	}
	m.hostCollectors = nil

	m.watchers.Range(func(key, value interface{}) bool {
		value.(*ContainerCollector).Stop()
		return true
	})

	// 停止所有上传器
	for _, out := range m.outputs {
		out.Stop()
	}

	flog.Infof("[FileWatcher] 所有监视器和上传器已停止")
}

// GetWatcherCount 获取监视器数量
func (m *CollectorManager) GetWatcherCount() int {
	return int(m.watcherCount.Load())
}

// 获取out
func (m *CollectorManager) GetOutPut(name string) output.Output {
	return m.outputs[name]
}
