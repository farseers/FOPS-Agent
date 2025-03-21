package main

import (
	"time"

	"github.com/farseer-go/collections"
	"github.com/farseer-go/docker"
	"github.com/farseer-go/fs"
	"github.com/farseer-go/fs/configure"
	"github.com/farseer-go/fs/core"
	"github.com/farseer-go/fs/flog"
	"github.com/farseer-go/utils/system"
	"github.com/farseer-go/utils/ws"
)

type Res struct {
	Host                system.Resource                        // 主机资源
	IsDockerMaster      bool                                   // 是否是Docker主节点
	DockerEngineVersion string                                 // Docker引擎版本
	Dockers             collections.List[docker.DockerStatsVO] // Docker容器资源
}

func main() {
	fs.Initialize[StartupModule]("fops-agent")
	wsServer := configure.GetString("Fops.WsServer")
	if wsServer == "" {
		panic("请配置Fops.WsServer")
	}

	dockerClient := docker.NewClient()
	dockerVer := dockerClient.GetVersion()
	isMaster := dockerClient.IsMaster()
	var hostIP string
	var hostName string

	if dockerVer == "" {
		dockerVer = "未安装"
	} else {
		hostIP = dockerClient.GetHostIP()
		hostName = dockerClient.GetHostName()
	}

	wsServer += "/ws/resource"
	for {
		wsClient, err := ws.Connect(wsServer, 8192)
		wsClient.AutoExit = false
		if err != nil {
			flog.Warningf("[%s]Fops.WsServer连接失败：%s，将在3秒后重连", wsServer, err.Error())
			time.Sleep(3 * time.Second)
			continue
		}

		for {
			// 发送消息
			res := Res{
				IsDockerMaster:      isMaster,
				DockerEngineVersion: dockerVer,
				Host:                system.GetResource(),
				Dockers:             dockerClient.Stats(),
			}
			// 如果是Docker节点，获取主机IP
			if hostIP != "" {
				res.Host.IP = hostIP
			}
			if hostName != "" {
				res.Host.HostName = hostName
			}

			err = wsClient.Send(res)
			if err != nil {
				flog.Warningf("[%s]发送消息失败：%s", core.AppName, err.Error())
				break
			}
			time.Sleep(3 * time.Second)
		}
		// 断开后重连
		time.Sleep(3 * time.Second)
	}
}
