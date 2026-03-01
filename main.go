package main

import (
	"github.com/farseer-go/docker"
	"github.com/farseer-go/fs"
	"github.com/farseer-go/fs/configure"
)

func main() {
	fs.Initialize[StartupModule]("fops-agent")
	wsServer := configure.GetString("Fops.WsServer")
	if wsServer == "" {
		panic("请配置Fops.WsServer")
	}

	dockerClient := docker.NewClient()
	dockerInfo := dockerClient.GetInfo() // 获取docker版本

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
