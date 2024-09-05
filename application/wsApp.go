// @area /ws/
package application

import (
	"github.com/farseer-go/docker"
	"github.com/farseer-go/fs/flog"
	"github.com/farseer-go/utils/system"
	"github.com/farseer-go/webapi/websocket"
	"time"
)

type Request struct {
	Type string
}

// 获取宿主资源使用情况
// @ws /host/resource
func WsHostResource(context *websocket.Context[Request]) {
	for {
		resource := system.GetResource()
		if resource.CpuUsagePercent > 0 {
			_ = context.Send(resource)
		}
		time.Sleep(3 * time.Second)
	}
}

// 获取宿主资源使用情况
// @ws /docker/resource
func WsDockerResource(context *websocket.Context[Request]) {
	for {
		stats := docker.NewClient().Stats()
		if stats.Count() > 0 {
			flog.Infof("%+v", stats)
			_ = context.Send(stats)
		}
		time.Sleep(3 * time.Second)
	}
}
