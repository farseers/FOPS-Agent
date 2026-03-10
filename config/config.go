package config

import (
	"os"
	"strings"

	"github.com/farseer-go/fs/configure"
	"github.com/farseer-go/fs/flog"
)

// ProcPrefix /proc 路径前缀（默认主机环境，检测到 /host/proc 则为 Docker 环境）
var ProcPrefix = "/proc"

// Config 全局配置
type Config struct {
	// Fops 服务配置
	FopsHttpServer string `yaml:"Fops.WsServer"`
	FopsWsServer   string `yaml:"Fops.WsServer"`
	// Container 容器配置
	Container ContainerConfig `yaml:"Container"`
	// Collectors 采集器配置列表
	Collectors []CollectorConfig `yaml:"Collectors"`
}

// ContainerConfig 容器配置
type ContainerConfig struct {
	// OffsetDir 偏移量存储目录
	OffsetDir string `yaml:"OffsetDir"`
	// IgnoreNames 忽略的容器名称（前缀匹配）
	IgnoreNames []string `yaml:"IgnoreNames"`
	// StatsInterval 资源收集间隔（秒）
	StatsInterval int `yaml:"StatsInterval"`
}

// CollectorConfig 采集器配置
type CollectorConfig struct {
	// Name 采集器名称
	Name string `yaml:"Name"`
	// WatchDir 监听目录（支持 {app} 占位符）
	WatchDir string `yaml:"WatchDir"`
	// FileExt 文件扩展名
	FileExt string `yaml:"FileExt"`
	// UploadURL 上传路径
	UploadURL string `yaml:"UploadURL"`
	// UploadInterval 上传间隔（秒）
	UploadInterval int `yaml:"UploadInterval"`
	// BufferSizeMB 缓冲区大小（MB）
	BufferSizeMB int `yaml:"BufferSizeMB"`
}

// Load 从文件加载配置
func Load() *Config {
	// 检测运行环境
	if _, err := os.Stat("/host/proc"); err == nil {
		ProcPrefix = "/host/proc"
		flog.Infof("检测到 Docker 环境，使用 /host/proc")
	}

	cfg := Config{
		FopsWsServer: configure.GetString("Fops.WsServer"),
		Container:    configure.ParseConfig[ContainerConfig]("Container"),
		Collectors:   configure.ParseConfigs[CollectorConfig]("Collectors"),
	}

	// 转成http url
	if strings.HasPrefix(cfg.FopsWsServer, "wss://") {
		cfg.FopsHttpServer = "https://" + cfg.FopsWsServer[6:] + "/linkTrace/upload"
	} else if strings.HasPrefix(cfg.FopsWsServer, "ws://") {
		cfg.FopsHttpServer = "http://" + cfg.FopsWsServer[5:] + "/linkTrace/upload"
	}

	if cfg.FopsHttpServer == "" {
		panic("请配置Fops.WsServer")
	}

	// 设置默认值
	if cfg.Container.OffsetDir == "" {
		cfg.Container.OffsetDir = "/var/lib/fops-agent/offset"
	}
	if cfg.Container.StatsInterval == 0 {
		cfg.Container.StatsInterval = 3
	}

	// 设置采集器默认值
	for i := range cfg.Collectors {
		if cfg.Collectors[i].UploadInterval == 0 {
			cfg.Collectors[i].UploadInterval = 5
		}
		if cfg.Collectors[i].BufferSizeMB == 0 {
			cfg.Collectors[i].BufferSizeMB = 10
		}
	}

	return &cfg
}

// ShouldIgnore 判断容器是否应该被忽略
func (c *Config) ShouldIgnore(containerName string) bool {
	for _, prefix := range c.Container.IgnoreNames {
		if strings.HasPrefix(containerName, prefix) {
			return true
		}
	}
	return false
}

// GetCollectorConfig 根据名称获取采集器配置
func (c *Config) GetCollectorConfig(name string) *CollectorConfig {
	for i := range c.Collectors {
		if c.Collectors[i].Name == name {
			return &c.Collectors[i]
		}
	}
	return nil
}
