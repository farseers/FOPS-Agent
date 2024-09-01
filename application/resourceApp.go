// @area /api/
package application

import (
	"github.com/farseer-go/collections"
	"github.com/farseer-go/docker"
	"github.com/farseer-go/utils/system"
)

var DefaultSystemResource system.Resource
var DefaultDockerResource collections.List[docker.DockerStatsVO]

// 获取宿主资源使用情况
// @get /host/resource
func HostResource() system.Resource {
	//return system.GetResource()
	return DefaultSystemResource
}

// 获取Docker资源使用情况
// @get /docker/resource
func DockerResource() collections.List[docker.DockerStatsVO] {
	//return docker.NewClient().Stats()
	return DefaultDockerResource
}
