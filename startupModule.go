package main

import (
	"fops/infrastructure"
	"fops/interfaces"
	"github.com/farseer-go/fs/modules"
)

type StartupModule struct {
}

func (module StartupModule) DependsModule() []modules.FarseerModule {
	return []modules.FarseerModule{infrastructure.Module{}, interfaces.Module{}}
}

func (module StartupModule) PreInitialize() {
}

func (module StartupModule) Initialize() {
}

func (module StartupModule) PostInitialize() {
	//go func() {
	//	for {
	//		resource := system.GetResource()
	//		flog.Infof("CPU使用率：%.2f%%", resource.CpuUsagePercent)
	//		flog.Infof("内存使用率：%.2f%% %d %d", resource.MemoryUsagePercent, resource.MemoryUsage, resource.MemoryTotal)
	//		flog.Infof("硬盘使用率：%.2f%% %d %d", resource.DiskUsagePercent, resource.DiskUsage, resource.DiskTotal)
	//		time.Sleep(time.Second)
	//	}
	//}()
}

func (module StartupModule) Shutdown() {
}
