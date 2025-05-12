package main

import (
	"github.com/farseer-go/docker"
	"github.com/farseer-go/monitor"
)

// Docker核心容器事件映射表
var dockerEventMap = map[string]string{
	"create":        "容器创建完成",
	"start":         "容器启动",
	"kill":          "容器被强制终止",
	"die":           "容器进程终止",
	"stop":          "容器被停止",
	"pause":         "容器暂停",
	"unpause":       "容器恢复运行",
	"restart":       "容器重启",
	"rename":        "容器重命名",
	"destroy":       "容器被删除",
	"update":        "容器配置更新",
	"health_status": "健康检查状态变更",
	"attach":        "附加到容器",
	"detach":        "从容器分离",
}

func WatchDockerEventJob(dockerClient *docker.Client) {
	eventResults := dockerClient.Event.Watch()
	for eventResult := range eventResults {
		// 过滤其它信息
		if eventResult.Actor.Attributes.ComDockerSwarmServiceName == "" {
			continue
		}

		// 过滤rm操作 和 容器进程停止事件
		if eventResult.Action == "destroy" { //  || eventResult.Action == "die"
			continue
		}

		// 转换成中文事件描述
		if cns, exists := dockerEventMap[eventResult.Action]; exists {
			eventResult.Action = eventResult.Action + cns
		}

		// 发送消息
		monitor.SendValue(eventResult.Actor.Attributes.ComDockerSwarmServiceName, "event", eventResult.Actor.Attributes.Name+"，"+eventResult.Action)
	}
}
