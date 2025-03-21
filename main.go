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
	Host    system.Resource
	Dockers collections.List[docker.DockerStatsVO]
}

func main() {
	fs.Initialize[StartupModule]("fops-agent")
	wsServer := configure.GetString("Fops.WsServer")
	if wsServer == "" {
		panic("请配置Fops.WsServer")
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
			err = wsClient.Send(Res{
				Host:    system.GetResource(),
				Dockers: docker.NewClient().Stats(),
			})
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
