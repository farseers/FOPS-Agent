package interfaces

import (
	"context"
	"fops/application"
	"fops/interfaces/job"
	"github.com/farseer-go/fs/modules"
	"github.com/farseer-go/tasks"
	"github.com/farseer-go/webapi"
	"time"
)

type Module struct {
}

func (module Module) DependsModule() []modules.FarseerModule {
	return []modules.FarseerModule{webapi.Module{}, application.Module{}}
}

func (module Module) PostInitialize() {
	tasks.RunNow("收集所有容器的资源占用情况", time.Second*3, job.DockerResourceJob, context.Background())
	tasks.RunNow("收集主机的资源占用情况", time.Second*3, job.HostResourceJob, context.Background())
}
