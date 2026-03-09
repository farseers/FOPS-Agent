package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"fops-agent/collector"
	"net/http"
	"strings"
	"time"

	"github.com/farseer-go/collections"
	"github.com/farseer-go/fs/core"
	"github.com/farseer-go/fs/flog"
)

type UploadRequest struct {
	List collections.List[flog.LogData]
}

// 采集日志
func CollectLog(wsServer string, ignoreNames []string) {
	var url string
	if strings.HasPrefix(wsServer, "wss://") {
		url = "https://" + wsServer[6:] + "/flog/upload"
	} else if strings.HasPrefix(wsServer, "ws://") {
		url = "http://" + wsServer[5:] + "/flog/upload"
	}

	// 1. 定义一个全局复用的 Client（只需要初始化一次）
	var logHttpClient = &http.Client{
		Timeout: 10 * time.Second, // 必须设置总超时
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // 不验证 HTTPS 证书
			MaxIdleConns:    100,
			IdleConnTimeout: 90 * time.Second,
		},
	}

	// 采集容器日志并上传到fops
	logCollector := collector.NewCollector("/var/log/flog/", "log", 5*time.Second, 2, ignoreNames)
	logCollector.OnLogFile(func(logFile *collector.CollectFile) error {
		// 使用 strings.Builder 高效拼接
		var builder strings.Builder
		builder.WriteString(`{"List":[`) // 开始构造 JSON 对象

		first := true
		logFile.Lines.Foreach(func(line *string) {
			jsonContent := []byte(*line)
			// 校验 JSON 格式合法性
			if !json.Valid([]byte(jsonContent)) {
				flog.Warningf("上传日志,发现内容结构不符: %s", *line)
				return
			}

			if !first {
				builder.WriteString(",") // 元素之间加逗号
			}
			first = false
			builder.Write(jsonContent) // 直接写入原始 JSON 字符串
		})

		builder.WriteString("]}") // 结束构造

		// 转为 byte
		bodyByte := []byte(builder.String())

		newRequest, _ := http.NewRequest("POST", url, bytes.NewReader(bodyByte))
		newRequest.Header.Set("Content-Type", "application/json")

		rsp, err := logHttpClient.Do(newRequest)
		if err != nil {
			return fmt.Errorf("上传日志失败: %s", err.Error())
		}
		defer rsp.Body.Close()

		apiRsp := core.NewApiResponseByReader[any](rsp.Body)
		if apiRsp.StatusCode != 200 {
			return fmt.Errorf("上传日志失败 (%v) : %s", rsp.StatusCode, apiRsp.StatusMessage)
		}

		return nil
	})

	// logCollector.OnLogFile(func(logFile *collector.CollectFile) error {
	// 	lstData := collections.NewList[flog.LogData]()
	// 	logFile.Lines.Foreach(func(line *string) {
	// 		var logData flog.LogData
	// 		snc.Unmarshal([]byte(*line), &logData)
	// 		lstData.Add(logData)
	// 	})

	// 	bodyByte, _ := snc.Marshal(UploadRequest{List: lstData})

	// 	newRequest, _ := http.NewRequest("POST", url, bytes.NewReader(bodyByte))
	// 	newRequest.Header.Set("Content-Type", "application/json")

	// 	// 2. 使用全局 Client 发起请求
	// 	rsp, err := logHttpClient.Do(newRequest)
	// 	if err != nil {
	// 		return fmt.Errorf("上传日志失败: %s", err.Error())
	// 	}

	// 	// 3. 关键点：使用 defer 确保 Body 最终被关闭
	// 	// 即使后面的业务逻辑报错，连接也会回到池中
	// 	defer rsp.Body.Close()

	// 	// 4. 读取数据（注意：NewApiResponseByReader 内部读取完后，外面依然要 Close）
	// 	apiRsp := core.NewApiResponseByReader[any](rsp.Body)
	// 	if apiRsp.StatusCode != 200 {
	// 		return fmt.Errorf("上传日志失败 (%v) : %s", rsp.StatusCode, apiRsp.StatusMessage)
	// 	}

	// 	return nil
	// })

	// 启动
	logCollector.Start()
}
