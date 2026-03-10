package uploader

import (
	"sync"
)

// bufferQueue 缓冲队列
type bufferQueue struct {
	mu        sync.Mutex
	data      []string          // 存储每行数据
	fileInfos map[string]string // filePath -> collectorName
	size      int64             // 当前数据大小（字节）
	maxSize   int64             // 最大大小（字节）
}

// NewBufferQueue 创建缓冲队列
func NewBufferQueue(maxSizeMB int) *bufferQueue {
	return &bufferQueue{
		data:      make([]string, 0),
		fileInfos: make(map[string]string),
		maxSize:   int64(maxSizeMB) * 1024 * 1024,
	}
}

// Add 添加数据
func (q *bufferQueue) Add(lines []string, filePath string, collectorName string) int64 {
	q.mu.Lock()
	defer q.mu.Unlock()

	var size int64
	for _, line := range lines {
		q.data = append(q.data, line)
		size += int64(len(line))
	}
	q.fileInfos[filePath] = collectorName
	q.size += size

	return q.size
}

// GetAndClear 获取缓冲区数据并清空
func (q *bufferQueue) GetAndClear() ([]string, map[string]string, int64) {
	q.mu.Lock()
	defer q.mu.Unlock()

	data := q.data
	fileInfos := q.fileInfos
	size := q.size

	q.data = make([]string, 0)
	q.fileInfos = make(map[string]string)
	q.size = 0

	return data, fileInfos, size
}

// IsEmpty 是否为空
func (q *bufferQueue) IsEmpty() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.data) == 0
}
