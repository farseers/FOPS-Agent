package job

import (
	"fops/application"
	"github.com/farseer-go/docker"
	"github.com/farseer-go/tasks"
)

// DockerResourceJob 收集所有容器的资源占用情况
func DockerResourceJob(*tasks.TaskContext) {
	stats := docker.NewClient().Stats()
	if stats.Count() > 0 {
		application.DefaultDockerResource = stats
	}
}
