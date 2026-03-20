package output

type SuccessCallback func(filePath string, uploadSize int64)

// Data 数据输出内容
type Data struct {
	// ContainerID 容器ID
	ContainerID string
	// ContainerName 容器名称
	ContainerName string
	// AppName 应用名称
	AppName string
	// FilePath 文件路径
	FilePath string
	// Lines 内容行
	Lines [][]byte
	// CurSize 本次上传的大小
	CurSize int64
}

// Output 数据输出接口
// 具体实现：HTTPUploader、ChatPusher 等
type Output interface {
	// Name 输出器名称
	Name() string
	// Start 启动输出器
	Start()
	// Stop 停止输出器
	Stop()
	// Write 写入数据
	Write(data *Data)
	// RegisterCallback 处理成功后注册回调
	RegisterCallback(callback SuccessCallback)
}
