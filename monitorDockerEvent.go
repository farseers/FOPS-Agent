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

type MonitorDockerEvent struct {
}

func (*MonitorDockerEvent) Handle(event docker.EventResult) {
	// 过滤其它信息
	if event.Actor.Attributes.ComDockerSwarmServiceName == "" {
		return
	}

	// 过滤rm操作 和 容器进程停止事件
	if event.Action == "destroy" { //  || eventResult.Action == "die"
		return
	}

	// 转换成中文事件描述
	var actionName = event.Action
	if cns, exists := dockerEventMap[event.Action]; exists {
		actionName = event.Action + cns
	}

	// 发送消息
	monitor.SendValue(event.Actor.Attributes.ComDockerSwarmServiceName, "event", event.Actor.Attributes.Name+", "+actionName)
}
