package interfaces

import (
	"fops/application"
	"github.com/farseer-go/fs/modules"
	"github.com/farseer-go/webapi"
)

type Module struct {
}

func (module Module) DependsModule() []modules.FarseerModule {
	return []modules.FarseerModule{webapi.Module{}, application.Module{}}
}

func (module Module) PostInitialize() {
}
