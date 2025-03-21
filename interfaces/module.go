package interfaces

import (
	"fops-agent/application"

	"github.com/farseer-go/fs/modules"
)

type Module struct {
}

func (module Module) DependsModule() []modules.FarseerModule {
	return []modules.FarseerModule{application.Module{}}
}
