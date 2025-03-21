package infrastructure

import (
	"github.com/farseer-go/fs/modules"
)

type Module struct {
}

func (module Module) DependsModule() []modules.FarseerModule {
	// 这些模块都是farseer-go内置的模块
	return nil
}
