package container

import (
	"context"
	"strings"
	"sync"

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
}

// NewManager 创建容器管理器
func NewManager() *Manager {
	return &Manager{
		Client: docker.NewClient(),
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
			m.notifyAdd(inspect)
			flog.Infof("[ContainerManager] 容器新增: %s (%s)", parseContainerName(inspect.Name), inspect.ID[:12])
		}
	case "die", "destroy":
		if c, ok := m.GetContainer(containerID); ok {
			m.notifyRemove(containerID)
			flog.Infof("[ContainerManager] 容器移除: %s (%s)", parseContainerName(c.Name), containerID[:12])
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

// Stop 停止容器管理器
func (m *Manager) Stop() {
	m.containers.Range(func(key, value interface{}) bool {
		m.containers.Delete(key)
		return true
	})
	flog.Infof("[ContainerManager] 容器管理器已停止")
}

// parseContainerName 解析容器名称
func parseContainerName(name string) string {
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
	for _, o := range m.observers {
		o.OnContainerRemove(containerID)
	}
}
