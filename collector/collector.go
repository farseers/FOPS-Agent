package collector

import (
	"context"
)

// Collector 采集器接口
type Collector interface {
	// Name 采集器名称
	Name() string

	// Start 启动采集器
	Start(ctx context.Context) error

	// Stop 停止采集器
	Stop()

	// OnOutputSuccess 输出成功回调
	// 用于通知采集器可以删除已输出的文件
	OnOutputSuccess(filePath string)
}
