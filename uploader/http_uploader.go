package uploader

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"fops-agent/output"

	"github.com/farseer-go/fs/flog"
	"github.com/vmihailenco/msgpack/v5"
)

// HTTPUploader HTTP 上传器
type HTTPUploader struct {
	name           string                   // 名称
	uploadURL      string                   // 上传接口地址
	uploadInterval int                      // 上传间隔
	serializeType  string                   // 序列化格式：json 或 messagePack
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
func NewHTTPUploader(name string, uploadURL string, httpServerURL string, uploadInterval int, bufferSizeMB int64, serializeType string) *HTTPUploader {
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     30 * time.Second, // 短于服务端/nginx 的 keep-alive 超时（通常 60-75s），避免复用僵尸连接
	}
	if strings.HasPrefix(httpServerURL, "https://") {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	return &HTTPUploader{
		name:           name,
		uploadURL:      httpServerURL + uploadURL,
		uploadInterval: uploadInterval,
		serializeType:  serializeType,
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

	flog.Infof("[HTTPUploader:%s] 启动，上传地址: %s，间隔: %ds，缓冲: %dMB", u.name, u.uploadURL, u.uploadInterval, u.buffer.maxSize/1024/1024)
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

	// 根据序列化格式构建请求体
	var body []byte
	var contentType string
	if u.serializeType == "messagePack" {
		var err error
		body, err = u.buildMsgPack(fileInfos, line)
		if err != nil {
			flog.Warningf("[HTTPUploader:%s] 构建 MessagePack 失败: %v", u.name, err)
			u.buffer.PutBack(fileInfos)
			return
		}
		contentType = "application/x-msgpack"
	} else {
		body = u.buildJSON(fileInfos)
		contentType = "application/json"
	}

	// 上传
	if err := u.upload(body, contentType); err != nil {
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

// buildMsgPack 构建 MessagePack 请求体
func (u *HTTPUploader) buildMsgPack(fileInfos map[string]*fileInfo, lineCount int) ([]byte, error) {
	keys := make([]string, 0, len(fileInfos))
	for k := range fileInfos {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// 使用 msgpack.RawMessage 避开反序列化
	// 这里的 items 只是保存了指向原始二进制数据的“指针/引用”
	items := make([]msgpack.RawMessage, 0, lineCount)
	for _, k := range keys {
		for _, dataBytes := range fileInfos[k].data {
			// dataBytes 本身就是 []byte (从 readMsgPackFile 读出的 payload)
			// 直接强制类型转换为 RawMessage，不产生内存拷贝，不进行 Unmarshal
			items = append(items, msgpack.RawMessage(dataBytes))
		}
	}

	// 最终打包：List 字段会被编码成数组头，而 items 内部的二进制数据会被直接 Copy 进去
	return msgpack.Marshal(map[string]any{
		"List": items,
	})
}

// buildJSON 构建 JSON 请求体
func (u *HTTPUploader) buildJSON(fileInfos map[string]*fileInfo) []byte {
	// 对 key 排序，保证同批次内文件顺序稳定
	keys := make([]string, 0, len(fileInfos))
	for k := range fileInfos {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	buf.WriteString(`{"List":[`)

	first := true
	for _, k := range keys {
		for _, line := range fileInfos[k].data {
			if !first {
				buf.WriteByte(',')
			}
			first = false
			if json.Valid([]byte(line)) {
				buf.Write(line)
			} else {
				escaped, _ := json.Marshal(line)
				buf.Write(escaped)
			}
		}
	}

	buf.WriteString("]}")
	return buf.Bytes()
}

// upload 执行上传
func (u *HTTPUploader) upload(body []byte, contentType string) error {
	req, err := http.NewRequest("POST", u.uploadURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("Content-Type", contentType)
	resp, err := u.client.Do(req)
	if err != nil {
		return fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()
	// 读取并丢弃响应体，确保连接可被复用
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("服务端返回错误: %d", resp.StatusCode)
	}

	return nil
}
