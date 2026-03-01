package main

import (
	"github.com/farseer-go/docker"
	"github.com/farseer-go/fs"
	"github.com/farseer-go/fs/configure"
	"github.com/farseer-go/fs/flog"
)

func main() {
	fs.Initialize[StartupModule]("fops-agent")
	wsServer := configure.GetString("Fops.WsServer")
	if wsServer == "" {
		panic("请配置Fops.WsServer")
	}

	dockerClient := docker.NewClient()
	dockerInfo := dockerClient.GetInfo() // 获取docker版本

	flog.Infof("当前容器版本: %s", dockerInfo.Version)

	containers, _ := dockerClient.Container.List("", nil)
	containers.Foreach(func(item *docker.Container) {
		dockerStatsVO := dockerClient.Container.Stats(item.ID)
		flog.Infof("容器: %s, CPU使用率: %.2f%%, 内存使用: %dMB", dockerStatsVO.ContainerName, dockerStatsVO.CpuUsagePercent, dockerStatsVO.MemoryUsage/1024/1024)
	})

	// 持续上传系统资源
	go getResource(wsServer, dockerInfo, dockerClient)

	if dockerInfo.Version != "" {
		// 监听docker事件
		go func() {
			// 这里用for是怕shell命令执行失败，导致无法持续获取docker事件
			for {
				WatchDockerEventJob(dockerClient)
			}
		}()

		// 监听docker容器日志
		// go func() {
		// 	WatchFLogsJob(dockerClient)
		// }()

		// // 监听docker容器日志
		// go func() {
		// 	WatchLinkTraceJob(dockerClient)
		// }()
	}

	fs.Run()
}
