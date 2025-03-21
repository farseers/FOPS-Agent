package main

import (
	"net"
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

var hostIP string

func main() {
	fs.Initialize[StartupModule]("fops-agent")
	wsServer := configure.GetString("Fops.WsServer")
	if wsServer == "" {
		panic("请配置Fops.WsServer")
	}

	client := docker.NewClient()
	dockerVer := client.GetVersion()
	isMaster := client.IsMaster()
	if dockerVer == "" {
		dockerVer = "未安装"
	} else {
		hostIP = getHostIP()
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
				Dockers:             docker.NewClient().Stats(),
			}
			// 如果是Docker节点，获取主机IP
			if hostIP != "" {
				res.Host.IP = hostIP
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

func getHostIP() string {
	addrs, err := net.LookupHost("host.docker.internal")
	if err != nil || len(addrs) == 0 {
		return ""
	}
	return addrs[0]
}
