package main

import (
	"time"

	"github.com/farseer-go/collections"
	"github.com/farseer-go/docker"
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

func getResource(wsServer string, dockerInfo docker.DockerInfo, dockerClient *docker.Client) {
	wsServer += "/ws/resource"

	if dockerInfo.Version == "" {
		dockerInfo.Version = "未安装"
	}

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
				IsDockerMaster:      dockerInfo.IsMaster,
				DockerEngineVersion: dockerInfo.Version,
				Host:                system.GetResource("/", "/home"),
				Dockers:             dockerClient.Stats(),
			}
			// 如果是Docker节点，获取主机IP
			if dockerInfo.NodeAddr != "" {
				res.Host.IP = dockerInfo.NodeAddr
			}
			if dockerInfo.HostName != "" {
				res.Host.HostName = dockerInfo.HostName
			}

			// 如果硬盘数量为2，且容量完全一致时，则只需要取一个就可以。说明他们是同一个硬盘
			if len(res.Host.Disk) == 2 {
				if res.Host.Disk[0].DiskTotal == res.Host.Disk[1].DiskTotal && res.Host.Disk[0].DiskAvailable == res.Host.Disk[1].DiskAvailable && res.Host.Disk[0].DiskUsage == res.Host.Disk[1].DiskUsage {
					res.Host.Disk = res.Host.Disk[:1]
				}
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
