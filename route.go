// 该文件由fsctl route命令自动生成，请不要手动修改此文件
package main

import (
	"fops/application"
	"github.com/farseer-go/webapi"
	"github.com/farseer-go/webapi/context"
)

var route = []webapi.Route{
	{"GET", "/api/host/resource", application.HostResource, "", []context.IFilter{}, []string{}},
	{"GET", "/api/docker/resource", application.DockerResource, "", []context.IFilter{}, []string{}},
	{"WS", "/ws/host/resource", application.WsHostResource, "", []context.IFilter{}, []string{"context"}},
	{"WS", "/ws/docker/resource", application.WsDockerResource, "", []context.IFilter{}, []string{"context"}},
}
