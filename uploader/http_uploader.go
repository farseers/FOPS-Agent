package uploader

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"fops-agent/config"
	"fops-agent/output"

	"github.com/farseer-go/fs/flog"
)

// HTTPUploader HTTP 上传器
type HTTPUploader struct {
	name           string
	serverURL      string
	uploadURL      string
	uploadInterval int
	bufferSizeMB   int

	client   *http.Client
	buffer   *bufferQueue
	callback func(filePath string)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// bufferQueue 缓冲队列
type bufferQueue struct {
	mu        sync.Mutex
	data      []string        // 存储每行数据
	filePaths map[string]bool // 记录涉及的文件路径
	size      int64           // 当前数据大小（字节）
	maxSize   int64           // 最大大小（字节）
}

// NewBufferQueue 创建缓冲队列
func NewBufferQueue(maxSizeMB int) *bufferQueue {
	return &bufferQueue{
		data:      make([]string, 0),
		filePaths: make(map[string]bool),
		maxSize:   int64(maxSizeMB) * 1024 * 1024,
	}
}

// Add 添加数据
func (q *bufferQueue) Add(lines []string, filePath string) int64 {
	q.mu.Lock()
	defer q.mu.Unlock()

	var size int64
	for _, line := range lines {
		q.data = append(q.data, line)
		size += int64(len(line))
	}
	q.filePaths[filePath] = true
	q.size += size

	return q.size
}

// GetAndClear 获取数据并清空
func (q *bufferQueue) GetAndClear() ([]string, []string, int64) {
	q.mu.Lock()
	defer q.mu.Unlock()

	data := q.data
	paths := make([]string, 0, len(q.filePaths))
	for p := range q.filePaths {
		paths = append(paths, p)
	}
	size := q.size

	q.data = make([]string, 0)
	q.filePaths = make(map[string]bool)
	q.size = 0

	return data, paths, size
}

// IsEmpty 是否为空
func (q *bufferQueue) IsEmpty() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.data) == 0
}

// NewHTTPUploader 创建 HTTP 上传器
func NewHTTPUploader(cfg *config.CollectorConfig, httpServerURL string) *HTTPUploader {
	transport := &http.Transport{
		MaxIdleConns:    100,
		IdleConnTimeout: 90 * time.Second,
	}
	if strings.HasPrefix(httpServerURL, "https://") {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	return &HTTPUploader{
		name:           cfg.Name,
		serverURL:      httpServerURL,
		uploadURL:      httpServerURL + cfg.UploadURL,
		uploadInterval: cfg.UploadInterval,
		bufferSizeMB:   cfg.BufferSizeMB,
		client: &http.Client{
			Timeout:   10 * time.Second,
			Transport: transport,
		},
		buffer: NewBufferQueue(cfg.BufferSizeMB),
	}
}

// Name 返回输出器名称
func (u *HTTPUploader) Name() string {
	return u.name
}

// Start 启动输出器
func (u *HTTPUploader) Start() error {
	u.ctx, u.cancel = context.WithCancel(context.Background())

	// 启动定时上传协程
	u.wg.Add(1)
	go u.uploadLoop()

	flog.Infof("[HTTPUploader:%s] 启动，上传地址: %s，间隔: %ds，缓冲: %dMB", u.name, u.uploadURL, u.uploadInterval, u.bufferSizeMB)

	return nil
}

// Stop 停止输出器
func (u *HTTPUploader) Stop() {
	// 上传剩余数据
	if !u.buffer.IsEmpty() {
		u.flush()
	}

	if u.cancel != nil {
		u.cancel()
	}
	u.wg.Wait()
}

// Write 写入数据
func (u *HTTPUploader) Write(data *output.Data) {
	size := u.buffer.Add(data.Lines, data.FilePath)

	// 如果超过缓冲区大小，立即触发上传
	if size >= int64(u.bufferSizeMB)*1024*1024 {
		go u.flush()
	}
}

// SetCallback 设置成功回调
func (u *HTTPUploader) SetCallback(callback func(filePath string)) {
	u.callback = callback
}

// uploadLoop 上传循环
func (u *HTTPUploader) uploadLoop() {
	defer u.wg.Done()

	ticker := time.NewTicker(time.Duration(u.uploadInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-u.ctx.Done():
			return

		case <-ticker.C:
			u.flush()
		}
	}
}

// flush 刷新缓冲区
func (u *HTTPUploader) flush() {
	if u.buffer.IsEmpty() {
		return
	}

	data, filePaths, size := u.buffer.GetAndClear()
	if len(data) == 0 {
		return
	}

	flog.Debugf("[HTTPUploader:%s] 开始上传 %d 行数据，%.2f MB", u.name, len(data), float64(size)/1024/1024)

	// 构建 JSON
	body := u.buildJSON(data)

	// 上传
	if err := u.upload(body); err != nil {
		flog.Errorf("[HTTPUploader:%s] 上传失败: %v", u.name, err)
		return
	}

	flog.Infof("[HTTPUploader:%s] 上传成功 %d 行数据", u.name, len(data))

	// 回调通知上传成功
	if u.callback != nil {
		for _, path := range filePaths {
			u.callback(path)
		}
	}
}

// buildJSON 构建 JSON 请求体
func (u *HTTPUploader) buildJSON(lines []string) []byte {
	var buf bytes.Buffer
	buf.WriteString(`{"List":[`)

	for i, line := range lines {
		if i > 0 {
			buf.WriteByte(',')
		}
		// 检查 JSON 是否合法
		if json.Valid([]byte(line)) {
			buf.WriteString(line)
		} else {
			// 非法 JSON，转义后作为字符串
			escaped, _ := json.Marshal(line)
			buf.Write(escaped)
		}
	}

	buf.WriteString("]}")
	return buf.Bytes()
}

// upload 执行上传
func (u *HTTPUploader) upload(body []byte) error {
	req, err := http.NewRequest("POST", u.uploadURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := u.client.Do(req)
	if err != nil {
		return fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("服务端返回错误: %d", resp.StatusCode)
	}

	return nil
}
