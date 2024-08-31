// @area /api/
package application

import (
	"github.com/farseer-go/collections"
	"github.com/farseer-go/docker"
	"github.com/farseer-go/utils/system"
)

// 获取宿主资源使用情况
// @get /host/resource
func HostResource() system.Resource {
	return system.GetResource()
}

// 获取Docker资源使用情况
// @get /docker/resource
func DockerResource() collections.List[docker.DockerStatsVO] {
	client := docker.NewClient()
	return client.Stats()
}
