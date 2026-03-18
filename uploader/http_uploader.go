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
	name           string                   // 名称
	uploadURL      string                   // 上传接口地址
	uploadInterval int                      // 上传间隔
	client         *http.Client             // 上传client
	buffer         *bufferQueue             // 缓冲区
	callbacks      []output.SuccessCallback // collectorName -> callback 上传成功后的回调
	cbMu           sync.RWMutex

	lastFailTime time.Time // 上次上传失败时间
	failMu       sync.RWMutex

	flushing bool       // 是否正在刷新
	flushMu  sync.Mutex // 保护 flushing 标志

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewHTTPUploader 创建 HTTP 上传器
func NewHTTPUploader(name string, uploadURL string, httpServerURL string, uploadInterval int, bufferSizeMB int64) *HTTPUploader {
	transport := &http.Transport{
		MaxIdleConns:    100,
		IdleConnTimeout: 90 * time.Second,
	}
	if strings.HasPrefix(httpServerURL, "https://") {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	return &HTTPUploader{
		name:           name,
		uploadURL:      httpServerURL + uploadURL,
		uploadInterval: uploadInterval,
		client: &http.Client{
			Timeout:   10 * time.Second,
			Transport: transport,
		},
		buffer: NewBufferQueue(bufferSizeMB * 1024 * 1024),
	}
}

// Name 返回输出器名称
func (u *HTTPUploader) Name() string {
	return u.name
}

// Start 启动输出器
func (u *HTTPUploader) Start() {
	u.ctx, u.cancel = context.WithCancel(context.Background())

	// 启动定时上传协程
	u.wg.Add(1)
	go u.uploadLoop()

	flog.Infof("[HTTPUploader:%s] 启动，上传地址: %s，间隔: %ds，缓冲: %dMB", u.name, u.uploadURL, u.uploadInterval, u.buffer.curSize/1024/1024)
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
	totalSize := u.buffer.Add(data.FilePath, data.Lines, data.CurSize)

	// 如果超过缓冲区大小，检查是否可以上传
	if totalSize >= u.buffer.maxSize {
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
func (u *HTTPUploader) RegisterCallback(callback output.SuccessCallback) {
	u.cbMu.Lock()
	defer u.cbMu.Unlock()

	u.callbacks = append(u.callbacks, callback)
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
	// 检查是否已经在刷新，避免并发调用
	u.flushMu.Lock()
	if u.flushing {
		u.flushMu.Unlock()
		return
	}
	u.flushing = true
	u.flushMu.Unlock()

	// 确保在函数结束时清除 flushing 标志
	defer func() {
		u.flushMu.Lock()
		u.flushing = false
		u.flushMu.Unlock()
	}()

	if u.buffer.IsEmpty() {
		return
	}

	// 获取缓冲区数据并清空
	fileInfos, size, line := u.buffer.GetAndClear(2)
	if len(fileInfos) == 0 {
		return
	}

	// 构建 JSON
	body := u.buildJSON(fileInfos)

	// 上传
	if err := u.upload(body); err != nil {
		flog.Warningf("[HTTPUploader:%s] 上传失败 %d 行数据，%.2f MB: %v", u.name, line, float64(size)/1024/1024, err)

		// 将数据放回缓冲区，避免数据丢失
		u.buffer.PutBack(fileInfos)

		// 记录失败时间，触发5秒冷却期
		u.failMu.Lock()
		u.lastFailTime = time.Now()
		u.failMu.Unlock()
		return
	}

	flog.Infof("[HTTPUploader:%s] 上传成功 %d 行数据，%.2f MB", u.name, line, float64(size)/1024/1024)

	// 回调通知上传成功
	u.cbMu.RLock()
	defer u.cbMu.RUnlock()

	for _, fileInfo := range fileInfos {
		for _, cb := range u.callbacks {
			cb(fileInfo.filePath, fileInfo.dataSize)
		}
	}
}

// buildJSON 构建 JSON 请求体
func (u *HTTPUploader) buildJSON(fileInfos map[string]*fileInfo) []byte {
	var buf bytes.Buffer
	buf.WriteString(`{"List":[`)

	for _, fileInfo := range fileInfos {
		for i, line := range fileInfo.data {
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
