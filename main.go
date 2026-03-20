package main

import (
	"context"
	"fops-agent/collector"
	"fops-agent/config"
	"fops-agent/container"
	"fops-agent/watcher"

	"github.com/farseer-go/fs"
	"github.com/farseer-go/fs/core"
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
	containerMgr := container.NewManager(cfg.Container.StatsInterval)
	dockerInfo := containerMgr.Client.GetInfo() // 获取docker版本
	flog.Infof("当前容器版本: %s", dockerInfo.ServerVersion)

	// 创建监视器管理器（订阅容器事件）
	fileWatcherMgr := watcher.NewCollectorManager(cfg)

	// 如果不是运行在docker环境,则要手动监听/var/lib/fops-agent/flog/目录
	if !containerMgr.Client.IsRunInDocker() {
		// 找到log的配置
		logCollector := getLogCollector(cfg)
		flog.Infof("运行在宿主机上,手动监听日志目录: %s", logCollector.WatchDir)

		out := fileWatcherMgr.GetOutPut("log")
		col := collector.NewFileCollector("本机", "宿主机", "fops-agent", logCollector.WatchDir, logCollector.FileExt, core.ProcessId, logCollector.SerializeType, out)
		go col.Start(context.Background())
	}

	// 如果启用了docker
	if dockerInfo.ServerVersion != "" {
		// 监听docker事件,用以发送消息到fops
		containerMgr.Client.Event.Register(&MonitorDockerEvent{})

		// 订阅容器变化
		containerMgr.Subscribe(fileWatcherMgr)

		// 启动容器管理器
		if err := containerMgr.Start(fs.Context); err != nil {
			flog.Warningf("启动容器管理器失败: %v", err)
			return
		}
		flog.Infof("FOPS-Agent 已启动，监视 %d 个容器", fileWatcherMgr.GetWatcherCount())
	}
	// 持续上传系统资源
	go getResource(cfg.FopsWsServer, dockerInfo, containerMgr)

	webapi.UsePprof()
	webapi.Run(":8890")
	fs.Run()
}

// 找到日志的配置
func getLogCollector(cfg *config.Config) config.CollectorConfig {
	for _, logCollector := range cfg.Collectors {
		if logCollector.Name == "log" {
			return logCollector
		}
	}
	return config.CollectorConfig{
		Name:           "log",
		WatchDir:       "/var/log/flog/{app}/",
		FileExt:        "log",
		UploadURL:      "/flog/upload",
		UploadInterval: 5,
		BufferSizeMB:   10,
	}
}
