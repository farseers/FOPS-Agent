package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"fops-agent/collector"
	"fops-agent/config"

	"github.com/farseer-go/fs/flog"
)

// ContainerWatcher 容器文件监视器
type ContainerWatcher struct {
	containerID   string
	containerName string
	pid           int
	appName       string
	collectors    []collector.Collector
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
}

// NewContainerWatcher 创建容器文件监视器
func NewContainerWatcher(containerID, containerName string, pid int, cfg *config.Config, store *collector.FileStore, manager *FileWatcherManager) (*ContainerWatcher, error) {
	ctx, cancel := context.WithCancel(context.Background())
	w := &ContainerWatcher{
		containerID:   containerID,
		containerName: containerName,
		pid:           pid,
		appName:       containerName,
		ctx:           ctx,
		cancel:        cancel,
	}
	if appName := w.detectAppName(cfg); appName != "" {
		w.appName = appName
	}
	flog.Infof("[ContainerWatcher] 创建: %s, PID: %d, 应用: %s", containerName, pid, w.appName)
	for _, cc := range cfg.Collectors {
		// 使用全局上传器
		out := manager.GetOutput(cc.Name)
		col := collector.NewFileCollector(cc.Name, containerID, containerName, w.appName, cc.WatchDir, cc.FileExt, pid, store, out)
		w.collectors = append(w.collectors, col)

		// 注册回调到全局上传器
		if out != nil {
			out.RegisterCallback(cc.Name, func(filePath string) {
				col.OnOutputSuccess(filePath)
			})
		}
	}
	return w, nil
}

// detectAppName 从目录检测应用名称
func (w *ContainerWatcher) detectAppName(cfg *config.Config) string {
	for _, cc := range cfg.Collectors {
		if !strings.Contains(cc.WatchDir, "{app}") {
			continue
		}
		parentDir := strings.TrimSuffix(filepath.Dir(strings.Replace(cc.WatchDir, "{app}/", "", -1)), "/")
		actualPath := filepath.Join(config.ProcPrefix, fmt.Sprintf("%d", w.pid), "root", parentDir)
		entries, err := os.ReadDir(actualPath)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				return entry.Name()
			}
		}
	}
	return ""
}

// Start 启动容器监视器
func (w *ContainerWatcher) Start() error {
	for _, col := range w.collectors {
		if err := col.Start(w.ctx); err != nil {
			flog.Warningf("[ContainerWatcher] 启动采集器 %s 失败: %v", col.Name(), err)
		}
	}
	return nil
}

// Stop 停止容器监视器
func (w *ContainerWatcher) Stop() {
	w.cancel()
	for _, col := range w.collectors {
		col.Stop()
	}
	w.wg.Wait()
	flog.Infof("[ContainerWatcher] 已停止: %s", w.containerName)
}

// ContainerID 获取容器ID
func (w *ContainerWatcher) ContainerID() string {
	return w.containerID
}
