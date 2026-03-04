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
	Availability        string                                 // Docker节点状态
	Label               collections.List[docker.DockerLabelVO] // Docker节点标签
	Role                string                                 // 节点角色   manager worker
}

func getResource(wsServer string, dockerInfo docker.DockerInfo, dockerClient *docker.Client) {
	wsServer += "/ws/resource"

	if dockerInfo.ServerVersion == "" {
		dockerInfo.ServerVersion = "未安装"
	}

	for {
		wsClient, err := ws.Connect(wsServer, 8192)
		wsClient.AutoExit = false
		if err != nil {
			flog.Warningf("[%s]Fops.WsServer连接失败: %s, 将在3秒后重连", wsServer, err.Error())
			time.Sleep(3 * time.Second)
			continue
		}

		for {
			// 发送消息
			res := Res{
				IsDockerMaster:      dockerInfo.Swarm.ControlAvailable,
				DockerEngineVersion: dockerInfo.ServerVersion,
				Host:                system.GetResource("/", "/home"),
				Dockers:             dockerClient.Stats(),
				Availability:        dockerInfo.Swarm.LocalNodeState,
				Role:                "Worker",
			}
			// 判断当前是节点角色
			if dockerInfo.Swarm.ControlAvailable {
				res.Role = "Manager"
			}

			// 标签
			res.Label = collections.NewList[docker.DockerLabelVO]()
			for k, v := range dockerInfo.Labels {
				res.Label.Add(docker.DockerLabelVO{
					Name:  k,
					Value: v,
				})
			}

			// 如果是Docker节点，则使用Docker节点的IP和主机名，否则使用主机的IP和主机名
			if dockerInfo.Swarm.NodeAddr != "" {
				res.Host.IP = dockerInfo.Swarm.NodeAddr
			}
			if dockerInfo.Name != "" {
				res.Host.HostName = dockerInfo.Name
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
