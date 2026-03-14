package collector

import (
	"context"
)

// Collector 采集器接口
type Collector interface {
	// Name 采集器名称
	Name() string
	// Start 启动采集器
	Start(ctx context.Context)
	// Stop 停止采集器
	Stop()
}
