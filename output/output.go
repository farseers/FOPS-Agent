package output

// Data 数据输出内容
type Data struct {
	// ContainerID 容器ID
	ContainerID string
	// ContainerName 容器名称
	ContainerName string
	// AppName 应用名称
	AppName string
	// CollectorName 采集器名称
	CollectorName string
	// FilePath 文件路径
	FilePath string
	// Lines 内容行
	Lines []string
	// FileSize 文件大小
	FileSize int64
}

// Output 数据输出接口
// 具体实现：HTTPUploader、ChatPusher 等
type Output interface {
	// Name 输出器名称
	Name() string

	// Start 启动输出器
	Start() error

	// Stop 停止输出器
	Stop()

	// Write 写入数据
	Write(data *Data)

	// SetCallback 设置成功回调
	SetCallback(callback func(filePath string))
}
