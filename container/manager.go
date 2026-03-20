package container

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/farseer-go/collections"
	"github.com/farseer-go/docker"
	"github.com/farseer-go/fs/flog"
)

// Observer 容器观察者
type Observer interface {
	OnContainerAdd(container *docker.ContainerIdInspectJson)
	OnContainerRemove(containerID string)
}

// Manager 容器管理器
type Manager struct {
	Client     *docker.Client
	containers sync.Map
	observers  []Observer

	// 资源收集
	statsInterval time.Duration
	stats         sync.Map // containerID -> DockerStatsVO
	statsCancel   context.CancelFunc
}

// NewManager 创建容器管理器
func NewManager(statsInterval int) *Manager {
	return &Manager{
		Client:        docker.NewClient(),
		statsInterval: time.Duration(statsInterval) * time.Second,
	}
}

// Subscribe 订阅容器变化
func (m *Manager) Subscribe(observer Observer) {
	m.observers = append(m.observers, observer)
}

// Start 启动容器管理器
func (m *Manager) Start(ctx context.Context) error {
	containers, err := m.Client.Container.List("", nil)
	if err != nil {
		return err
	}
	var loadedCount int
	containers.Foreach(func(item *docker.Container) {
		inspect, err := m.Client.Container.Inspect(item.ID)
		if err != nil {
			flog.Warningf("[ContainerManager] Inspect 容器 %s 失败: %v", item.ID[:12], err)
			return
		}
		m.notifyAdd(inspect)
		loadedCount++
	})
	flog.Infof("[ContainerManager] 初始加载 %d 个容器", loadedCount)
	m.Client.Event.Register(m)
	m.Client.Event.Start()

	// 启动资源收集器
	m.startStatsCollector(ctx)

	return nil
}

// Handle 处理 Docker 事件（实现 docker.EventHandler 接口）
func (m *Manager) Handle(event docker.EventResult) {
	if event.Type != "container" {
		return
	}
	containerID := event.Actor.ID
	switch event.Action {
	case "start":
		inspect, err := m.Client.Container.Inspect(containerID)
		if err != nil {
			flog.Warningf("[ContainerManager] Inspect 新容器 %s 失败: %v", containerID[:12], err)
			return
		}
		if inspect.ID != "" {
			flog.Infof("[ContainerManager] 发现新容器: %s (%s)", ParseContainerName(inspect.Name), inspect.ID[:12])
			m.notifyAdd(inspect)
		}
	case "die", "destroy":
		if c, ok := m.GetContainer(containerID); ok {
			flog.Infof("[ContainerManager] 移除容器: %s (%s)", ParseContainerName(c.Name), containerID[:12])
			m.notifyRemove(containerID)
		}
	}
}

// GetContainer 获取容器信息
func (m *Manager) GetContainer(containerID string) (*docker.ContainerIdInspectJson, bool) {
	val, ok := m.containers.Load(containerID)
	if !ok {
		return nil, false
	}
	return val.(*docker.ContainerIdInspectJson), true
}

// GetAllContainers 获取所有容器
func (m *Manager) GetAllContainers() []*docker.ContainerIdInspectJson {
	var result []*docker.ContainerIdInspectJson
	m.containers.Range(func(key, value interface{}) bool {
		result = append(result, value.(*docker.ContainerIdInspectJson))
		return true
	})
	return result
}

// startStatsCollector 启动资源收集器
func (m *Manager) startStatsCollector(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	m.statsCancel = cancel

	// 启动时立即同步收集一次
	m.collectStats()

	go func() {
		ticker := time.NewTicker(m.statsInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.collectStats()
			}
		}
	}()

	flog.Infof("[ContainerManager] 资源收集器已启动，间隔: %v", m.statsInterval)
}

// collectStats 收集所有容器资源
func (m *Manager) collectStats() {
	var wg sync.WaitGroup
	m.containers.Range(func(key, value interface{}) bool {
		containerID := key.(string)
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			stat := m.Client.Container.Stats(id)
			if stat.ContainerID != "" {
				m.stats.Store(stat.ContainerID, stat)
			}
		}(containerID)
		return true
	})
	wg.Wait()
}

// GetStats 获取单个容器资源
func (m *Manager) GetStats(containerID string) (docker.DockerStatsVO, bool) {
	val, ok := m.stats.Load(containerID)
	if !ok {
		return docker.DockerStatsVO{}, false
	}
	return val.(docker.DockerStatsVO), true
}

// GetAllStats 获取所有容器资源
func (m *Manager) GetAllStats() collections.List[docker.DockerStatsVO] {
	result := collections.NewList[docker.DockerStatsVO]()
	m.stats.Range(func(key, value any) bool {
		result.Add(value.(docker.DockerStatsVO))
		return true
	})
	return result
}

// Stop 停止容器管理器
func (m *Manager) Stop() {
	if m.statsCancel != nil {
		m.statsCancel()
	}
	m.containers.Range(func(key, value any) bool {
		m.containers.Delete(key)
		return true
	})
	flog.Infof("[ContainerManager] 容器管理器已停止")
}

// ParseContainerName 解析容器名称（导出供其他包使用）
func ParseContainerName(name string) string {
	name = strings.TrimPrefix(name, "/")
	if idx := strings.Index(name, "."); idx > 0 {
		return name[:idx]
	}
	return name
}

// notifyAdd 存储容器并通知观察者
func (m *Manager) notifyAdd(container docker.ContainerIdInspectJson) {
	m.containers.Store(container.ID, &container)
	for _, o := range m.observers {
		o.OnContainerAdd(&container)
	}
}

// notifyRemove 删除容器并通知观察者
func (m *Manager) notifyRemove(containerID string) {
	m.containers.Delete(containerID)
	m.stats.Delete(containerID)
	for _, o := range m.observers {
		o.OnContainerRemove(containerID)
	}
}
