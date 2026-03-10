package watcher

import (
	"strings"
	"sync"

	"fops-agent/collector"
	"fops-agent/config"
	"fops-agent/output"
	"fops-agent/uploader"

	"github.com/farseer-go/docker"
	"github.com/farseer-go/fs/flog"
)

// WatcherManager 文件监视器管理器
// 订阅容器事件，管理每个容器的文件监视器
type WatcherManager struct {
	cfg      *config.Config
	store    *collector.FileStore
	outputs  map[string]output.Output // collectorName -> Output（全局共享）
	watchers sync.Map                 // containerID -> *ContainerWatcher
}

// NewWatcherManager 创建文件监视器管理器
func NewWatcherManager(cfg *config.Config, store *collector.FileStore) *WatcherManager {
	m := &WatcherManager{
		cfg:     cfg,
		store:   store,
		outputs: make(map[string]output.Output),
	}

	// 预创建全局上传器（每个 collector 一个）
	for _, cc := range cfg.Collectors {
		m.outputs[cc.Name] = uploader.NewHTTPUploader(cc.Name, cc.UploadURL, cfg.FopsHttpServer, cc.UploadInterval, cc.BufferSizeMB)
	}

	// 启动所有上传器
	for _, out := range m.outputs {
		if err := out.Start(); err != nil {
			flog.Errorf("[FileWatcherManager] 启动上传器失败: %v", err)
		}
	}

	return m
}

// GetOutput 获取指定 collector 的输出器
func (m *WatcherManager) GetOutput(collectorName string) output.Output {
	return m.outputs[collectorName]
}

// OnContainerAdd 容器新增事件（实现 container.Observer 接口）
func (m *WatcherManager) OnContainerAdd(c *docker.ContainerIdInspectJson) {
	containerName := parseContainerName(c.Name)
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
	w, err := NewContainerWatcher(c.ID, containerName, c.State.Pid, m.cfg, m.store, m)
	if err != nil {
		flog.Errorf("[FileWatcher] 创建监视器失败: %v", err)
		return
	}
	if err := w.Start(); err != nil {
		flog.Errorf("[FileWatcher] 启动监视器失败: %v", err)
		return
	}
	m.watchers.Store(c.ID, w)
}

// OnContainerRemove 容器删除事件（实现 container.Observer 接口）
func (m *WatcherManager) OnContainerRemove(containerID string) {
	val, ok := m.watchers.Load(containerID)
	if !ok {
		return
	}
	w := val.(*ContainerWatcher)
	w.Stop()
	m.store.DeleteByContainer(containerID)
	m.watchers.Delete(containerID)
	flog.Infof("[FileWatcher] 已移除: %s", containerID[:12])
}

// Stop 停止所有监视器
func (m *WatcherManager) Stop() {
	m.watchers.Range(func(key, value interface{}) bool {
		value.(*ContainerWatcher).Stop()
		return true
	})

	// 停止所有上传器
	for _, out := range m.outputs {
		out.Stop()
	}

	flog.Infof("[FileWatcher] 所有监视器和上传器已停止")
}

// GetWatcherCount 获取监视器数量
func (m *WatcherManager) GetWatcherCount() int {
	count := 0
	m.watchers.Range(func(key, value interface{}) bool {
		count++
		return true
	})
	return count
}

// parseContainerName 解析容器名称
func parseContainerName(name string) string {
	name = strings.TrimPrefix(name, "/")
	if idx := strings.Index(name, "."); idx > 0 {
		return name[:idx]
	}
	return name
}
