package main

import (
	"context"
	"fops-agent/collector"
	"fops-agent/config"
	"fops-agent/container"
	"fops-agent/watcher"

	"github.com/farseer-go/fs"
	"github.com/farseer-go/fs/flog"
	"github.com/farseer-go/webapi"
)

// 运行在每台节点上,支持非docker环境和docker swarm环境.
// 在非docker环境时,只支持当前系统资源上传到fops
// 在docker环境中,同时支持上传所有docker运行的资源, 容器内的日志文件, 链路文件
func main() {
	fs.Initialize[StartupModule]("fops-agent")
	// 加载配置
	cfg := config.Load()

	// 创建容器管理器
	containerMgr := container.NewManager()
	dockerInfo := containerMgr.Client.GetInfo() // 获取docker版本
	flog.Infof("当前容器版本: %s", dockerInfo.ServerVersion)

	// 如果启用了docker
	if dockerInfo.ServerVersion != "" {
		// 监听docker事件,用以发送消息到fops
		containerMgr.Client.Event.Register(&MonitorDockerEvent{})
		// 创建偏移量存储
		store, err := collector.NewFileStore(cfg.Container.OffsetDir)
		if err != nil {
			flog.Warningf("创建偏移量存储失败: %v", err)
			return
		}

		// 创建文件监视器管理器（订阅容器事件）
		fileWatcherMgr := watcher.NewFileWatcherManager(cfg, store)

		// 订阅容器变化
		containerMgr.Subscribe(fileWatcherMgr)

		// 创建上下文
		ctx, _ := context.WithCancel(context.Background())

		// 启动容器管理器
		if err := containerMgr.Start(ctx); err != nil {
			flog.Warningf("启动容器管理器失败: %v", err)
			return
		}

		flog.Infof("FOPS-Agent 已启动，监视 %d 个容器", fileWatcherMgr.GetWatcherCount())

		// 启动时,先打印一遍当前所有容器的资源情况
		for _, item := range containerMgr.GetAllContainers() {
			dockerStatsVO := containerMgr.Client.Container.Stats(item.ID)
			flog.Infof("容器: %s, CPU使用率: %.2f%%, 内存使用: %dMB", dockerStatsVO.ContainerName, dockerStatsVO.CpuUsagePercent, dockerStatsVO.MemoryUsage)
		}

		containerMgr.GetAllContainers()
	}

	// 持续上传系统资源
	go getResource(cfg.FopsWsServer, dockerInfo, containerMgr)

	webapi.UsePprof()
	webapi.Run(":8890")
	fs.Run()
}
