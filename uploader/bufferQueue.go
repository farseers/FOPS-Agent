package uploader

import (
	"sync"
)

type fileInfo struct {
	filePath string   // 文件地址
	data     []string // 文件数据(本次要上传的数据)
	dataSize int64    // 数据大小(本次要上传的数据大小)
}

// bufferQueue 缓冲队列
type bufferQueue struct {
	mu        sync.Mutex           // 锁
	fileInfos map[string]*fileInfo // filePath -> fileInfo 文件本次要上传的内容和大小（字节）
	curSize   int64                // 当前数据大小（字节）
	maxSize   int64                // 最大大小（字节）
	line      int                  // 共多少行数据
}

// NewBufferQueue 创建缓冲队列
func NewBufferQueue(maxSize int64) *bufferQueue {
	return &bufferQueue{
		fileInfos: make(map[string]*fileInfo),
		maxSize:   maxSize,
	}
}

// Add 添加数据
func (q *bufferQueue) Add(filePath string, line []string, lineSize int64) int64 {
	q.mu.Lock()
	defer q.mu.Unlock()

	// 检查文件
	if _, ok := q.fileInfos[filePath]; !ok {
		q.fileInfos[filePath] = &fileInfo{
			filePath: filePath,
		}
	}

	q.fileInfos[filePath].data = append(q.fileInfos[filePath].data, line...)
	q.fileInfos[filePath].dataSize += lineSize
	q.curSize += lineSize
	q.line += len(line)
	return q.curSize
}

// GetAndClear 获取缓冲区数据并清空
func (q *bufferQueue) GetAndClear() (map[string]*fileInfo, int64, int) {
	q.mu.Lock()
	defer q.mu.Unlock()

	fileInfos := q.fileInfos
	size := q.curSize
	line := q.line

	// 清空数据
	q.fileInfos = make(map[string]*fileInfo)
	q.curSize = 0
	q.line = 0

	return fileInfos, size, line
}

// IsEmpty 是否为空
func (q *bufferQueue) IsEmpty() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.fileInfos) == 0
}

// PutBack 将数据放回队列头部（用于上传失败时恢复数据）
func (q *bufferQueue) PutBack(fileInfos map[string]*fileInfo) {
	// 将失败的数据放回队列头部，确保优先重试
	for filePath, v := range fileInfos {
		q.Add(filePath, v.data, v.dataSize)
	}
}
