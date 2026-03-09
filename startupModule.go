package main

import (
	"github.com/farseer-go/fs/modules"
	"github.com/farseer-go/monitor"
)

type StartupModule struct {
}

func (module StartupModule) DependsModule() []modules.FarseerModule {
	return []modules.FarseerModule{monitor.Module{}}
}
