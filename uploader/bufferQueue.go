package uploader

import (
	"sync"
)

type fileInfo struct {
	filePath string   // 文件地址
	data     [][]byte // 文件数据(本次要上传的数据)
	dataSize int64    // 数据大小(本次要上传的数据大小)
}

// bufferQueue 缓冲队列
type bufferQueue struct {
	mu        sync.Mutex           // 锁
	fileInfos map[string]*fileInfo // filePath -> fileInfo 文件本次要上传的内容和大小（字节）
	curSize   int64                // 当前数据大小（字节）
	maxSize   int64                // 最大大小（字节）,当curSize超过maxSize时,需要立即取走数据
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
// 建议内部统一计算大小，防止与 GetAndClear 的切分逻辑不一致导致 curSize 误差
func (q *bufferQueue) Add(filePath string, lines [][]byte, lineSize int64) int64 {
	q.mu.Lock()
	defer q.mu.Unlock()

	info, ok := q.fileInfos[filePath]
	if !ok {
		info = &fileInfo{
			filePath: filePath,
		}
		q.fileInfos[filePath] = info
	}

	// 为了保证 curSize 的准确性，这里以实际 len(line) 为准进行累加
	// 如果外部传入的 lineSize 包含额外开销，可以在这里修正，或者修改下方计算逻辑
	var actualSize int64 = 0
	if lineSize > 0 {
		actualSize = lineSize
	} else {
		for _, l := range lines {
			actualSize += int64(len(l))
		}
	}

	info.data = append(info.data, lines...)
	info.dataSize += actualSize
	q.curSize += actualSize
	q.line += len(lines)

	return q.curSize
}

// GetAndClear 获取缓冲区数据并清空
// popSizeMB: 限制每次取出的最大数据量(字节)。如果 <= 0 则取出全部。
func (q *bufferQueue) GetAndClear(limitBytes int64) (map[string]*fileInfo, int64, int) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.fileInfos) == 0 {
		return nil, 0, 0
	}

	poppedInfos := make(map[string]*fileInfo)
	var poppedSize int64 = 0
	poppedLine := 0

	for filePath, info := range q.fileInfos {
		remainingLimit := limitBytes - poppedSize

		// 1. 无限制，或当前文件完全小于剩余限制，直接取出整个文件
		if limitBytes <= 0 || info.dataSize <= remainingLimit {
			poppedInfos[filePath] = info
			poppedSize += info.dataSize
			poppedLine += len(info.data)
			delete(q.fileInfos, filePath)
			continue
		}

		// 2. 当前文件很大，需要按行切分
		var curBatchSize int64 = 0
		splitIndex := -1

		// 寻找切分点
		for i, line := range info.data {
			lineLen := int64(len(line))

			// 修正逻辑：
			// 只要当前累计大小 + 下一行 超过了剩余限制，就切分。
			// 不再限制 poppedSize > 0，这样即使是第一个文件很大，也能尽可能多装。
			if curBatchSize+lineLen > remainingLimit {
				// 特殊情况：如果一行就超限了
				if curBatchSize == 0 {
					// 强制取出这一行，防止死锁
					splitIndex = i + 1
					curBatchSize += lineLen
				} else {
					// 否则在当前行之前切分
					splitIndex = i
				}
				break
			}

			curBatchSize += lineLen
		}

		// 如果 splitIndex 仍为 -1，说明遍历完所有行仍未超限。
		// 这通常意味着 remainingLimit 足够大，但前面的 if info.dataSize <= remainingLimit 拦截了，
		// 或者是 info.dataSize 预估值偏大，实际计算出来没超限。
		// 此时应该取出整个文件。
		if splitIndex == -1 {
			poppedInfos[filePath] = info
			poppedSize += curBatchSize // 使用计算出的实际大小
			poppedLine += len(info.data)
			delete(q.fileInfos, filePath)
			continue
		}

		// 执行切分
		poppedInfo := &fileInfo{
			filePath: filePath,
			data:     info.data[:splitIndex],
			dataSize: curBatchSize,
		}

		poppedInfos[filePath] = poppedInfo
		poppedSize += curBatchSize
		poppedLine += splitIndex

		// 更新原队列剩余数据
		info.data = info.data[splitIndex:]
		info.dataSize -= curBatchSize
		// 不删除 key，保留剩余部分

		// 既然触发了切分，说明限制已满，结束本次取出
		break
	}

	// 更新全局统计
	q.curSize -= poppedSize
	q.line -= poppedLine

	return poppedInfos, poppedSize, poppedLine
}

// IsEmpty 是否为空
func (q *bufferQueue) IsEmpty() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.fileInfos) == 0
}

// PutBack 将数据放回队列头部
func (q *bufferQueue) PutBack(fileInfos map[string]*fileInfo) {
	if len(fileInfos) == 0 {
		return
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	for filePath, oldInfo := range fileInfos {
		if currentInfo, exists := q.fileInfos[filePath]; exists {
			// 合并数据：旧数据在前，新数据在后
			newData := make([][]byte, 0, len(oldInfo.data)+len(currentInfo.data))
			newData = append(newData, oldInfo.data...)
			newData = append(newData, currentInfo.data...)

			currentInfo.data = newData
			currentInfo.dataSize += oldInfo.dataSize
		} else {
			q.fileInfos[filePath] = oldInfo
		}

		q.curSize += oldInfo.dataSize
		q.line += len(oldInfo.data)
	}
}
