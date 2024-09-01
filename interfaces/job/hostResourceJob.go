package job

import (
	"fops/application"
	"github.com/farseer-go/tasks"
	"github.com/farseer-go/utils/system"
)

// HostResourceJob 收集主机的资源占用情况
func HostResourceJob(*tasks.TaskContext) {
	resource := system.GetResource()
	if resource.CpuUsagePercent > 0 {
		application.DefaultSystemResource = resource
	}
}
