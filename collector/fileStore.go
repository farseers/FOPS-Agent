package collector

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/farseer-go/fs/flog"
)

// FileOffset 文件偏移量信息
type FileOffset struct {
	// ContainerID 容器ID
	ContainerID string `json:"containerId"`
	// ContainerName 容器名称
	ContainerName string `json:"containerName"`
	// FilePath 文件路径
	FilePath string `json:"filePath"`
	// Offset 当前读取偏移量
	Offset int64 `json:"offset"`
	// FileSize 文件大小（上次读取时）
	FileSize int64 `json:"fileSize"`
	// LastReadTime 最后读取时间
	LastReadTime time.Time `json:"lastReadTime"`
	// LastModifyTime 文件最后修改时间
	LastModifyTime time.Time `json:"lastModifyTime"`
}

// FileStore 文件存储实现
type FileStore struct {
	dir   string
	mu    sync.RWMutex
	cache map[string]*FileOffset // 内存缓存
}

// NewFileStore 创建文件存储
func NewFileStore(dir string) (*FileStore, error) {
	// 创建目录
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("创建偏移量目录失败: %w", err)
	}

	store := &FileStore{
		dir:   dir,
		cache: make(map[string]*FileOffset),
	}

	// 加载已有数据
	if err := store.load(); err != nil {
		flog.Warningf("[警告] 加载偏移量数据失败: %v", err)
	}

	return store, nil
}

// getKey 生成缓存key
func (s *FileStore) getKey(containerID, filePath string) string {
	return containerID + ":" + filePath
}

// load 从文件加载所有偏移量
func (s *FileStore) load() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filePath := filepath.Join(s.dir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		var offset FileOffset
		if err := json.Unmarshal(data, &offset); err != nil {
			continue
		}

		key := s.getKey(offset.ContainerID, offset.FilePath)
		s.cache[key] = &offset
	}

	return nil
}

// Get 获取文件偏移量
func (s *FileStore) Get(containerID, filePath string) *FileOffset {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := s.getKey(containerID, filePath)
	offset, ok := s.cache[key]
	if !ok {
		return nil
	}

	// 返回副本
	copy := *offset
	return &copy
}

// Set 设置文件偏移量
func (s *FileStore) Set(offset *FileOffset) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := s.getKey(offset.ContainerID, offset.FilePath)
	s.cache[key] = offset

	// 异步持久化
	go s.persist(offset)
}

// persist 持久化单个偏移量
func (s *FileStore) persist(offset *FileOffset) error {
	// 生成文件名：containerID前12位 + 文件路径hash
	fileName := fmt.Sprintf("%s_%s.json", offset.ContainerID[:min(12, len(offset.ContainerID))], hashString(offset.FilePath))
	filePath := filepath.Join(s.dir, fileName)

	data, err := json.MarshalIndent(offset, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化偏移量失败: %w", err)
	}

	// 写入临时文件
	tmpFile := filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("写入偏移量文件失败: %w", err)
	}

	// 原子性重命名
	if err := os.Rename(tmpFile, filePath); err != nil {
		return fmt.Errorf("重命名偏移量文件失败: %w", err)
	}

	return nil
}

// Delete 删除文件偏移量
func (s *FileStore) Delete(containerID, filePath string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := s.getKey(containerID, filePath)
	delete(s.cache, key)

	// 删除文件
	fileName := fmt.Sprintf("%s_%s.json", containerID[:min(12, len(containerID))], hashString(filePath))
	filePathFull := filepath.Join(s.dir, fileName)
	os.Remove(filePathFull)
}

// List 列出所有偏移量
func (s *FileStore) List() []*FileOffset {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*FileOffset, 0, len(s.cache))
	for _, offset := range s.cache {
		copy := *offset
		result = append(result, &copy)
	}

	return result
}

// Clean 清理过期的偏移量记录
func (s *FileStore) Clean(before time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for key, offset := range s.cache {
		if offset.LastReadTime.Before(before) {
			delete(s.cache, key)

			// 删除文件
			fileName := fmt.Sprintf("%s_%s.json",
				offset.ContainerID[:min(12, len(offset.ContainerID))],
				hashString(offset.FilePath))
			filePath := filepath.Join(s.dir, fileName)
			os.Remove(filePath)
		}
	}
}

// Sync 同步所有脏数据到磁盘
func (s *FileStore) Sync() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, offset := range s.cache {
		if err := s.persist(offset); err != nil {
			return err
		}
	}

	return nil
}

// hashString 简单的字符串hash函数
func hashString(s string) string {
	if len(s) == 0 {
		return "0"
	}
	h := uint32(0)
	for _, c := range s {
		h = h*31 + uint32(c)
	}
	return fmt.Sprintf("%08x", h)
}
