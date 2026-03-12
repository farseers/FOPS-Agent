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

	client    *http.Client
	buffer    *bufferQueue
	callbacks map[string]func(filePath string) // collectorName -> callback
	cbMu      sync.RWMutex

	lastFailTime time.Time // 上次上传失败时间
	failMu       sync.RWMutex

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewHTTPUploader 创建 HTTP 上传器
func NewHTTPUploader(name string, uploadURL string, httpServerURL string, uploadInterval int, bufferSizeMB int) *HTTPUploader {
	transport := &http.Transport{
		MaxIdleConns:    100,
		IdleConnTimeout: 90 * time.Second,
	}
	if strings.HasPrefix(httpServerURL, "https://") {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	return &HTTPUploader{
		name:           name,
		serverURL:      httpServerURL,
		uploadURL:      httpServerURL + uploadURL,
		uploadInterval: uploadInterval,
		bufferSizeMB:   bufferSizeMB,
		client: &http.Client{
			Timeout:   10 * time.Second,
			Transport: transport,
		},
		buffer:    NewBufferQueue(bufferSizeMB),
		callbacks: make(map[string]func(filePath string)),
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
	size := u.buffer.Add(data.Lines, data.FilePath, data.CollectorName)

	// 如果超过缓冲区大小，检查是否可以上传
	if size >= int64(u.bufferSizeMB)*1024*1024 {
		// 检查上次失败后是否已过5秒
		u.failMu.RLock()
		lastFail := u.lastFailTime
		u.failMu.RUnlock()

		if time.Since(lastFail) >= 5*time.Second {
			go u.flush()
		}
	}
}

// RegisterCallback 注册回调
func (u *HTTPUploader) RegisterCallback(collectorName string, callback func(filePath string)) {
	u.cbMu.Lock()
	defer u.cbMu.Unlock()
	u.callbacks[collectorName] = callback
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

	// 获取缓冲区数据并清空
	data, fileInfos, size := u.buffer.GetAndClear()
	if len(data) == 0 {
		return
	}

	// 构建 JSON
	body := u.buildJSON(data)

	// 上传
	if err := u.upload(body); err != nil {
		flog.Warningf("[HTTPUploader:%s] 上传失败 %d 行数据: %v", u.name, len(data), err)
		// 记录失败时间，触发5秒冷却期
		u.failMu.Lock()
		u.lastFailTime = time.Now()
		u.failMu.Unlock()
		return
	}

	flog.Infof("[HTTPUploader:%s] 上传成功 %d 行数据，%.2f MB", u.name, len(data), float64(size)/1024/1024)

	// 回调通知上传成功
	u.cbMu.RLock()
	defer u.cbMu.RUnlock()

	for filePath, collectorName := range fileInfos {
		if cb, ok := u.callbacks[collectorName]; ok {
			cb(filePath)
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
