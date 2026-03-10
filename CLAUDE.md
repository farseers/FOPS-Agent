# FOPS-Agent 项目文档

## 项目概述

FOPS-Agent 是一个运行在 Docker Swarm 集群上的代理程序，用于采集容器日志和链路追踪数据，并上传到 FOPS 服务器。

## 架构设计

```
┌─────────────────────────────────────────────────────────────────┐
│                         FOPS-Agent                               │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌──────────────────┐     ┌──────────────────────────────────┐  │
│  │ ContainerManager │────>│ WatcherManager (Observer)    │  │
│  │                  │     │                                  │  │
│  │ - Docker Events  │     │ - outputs (全局上传器)            │  │
│  │ - Stats 收集     │     │ - watchers (容器监视器)           │  │
│  └──────────────────┘     └──────────────┬───────────────────┘  │
│                                          │                       │
│                           ┌──────────────┴───────────────┐       │
│                           ▼                              ▼       │
│                  ┌────────────────┐            ┌────────────────┐│
│                  │ContainerWatcher│            │ContainerWatcher││
│                  │                │            │                ││
│                  │ - collectors   │            │ - collectors   ││
│                  └───────┬────────┘            └───────┬────────┘│
│                          │                             │         │
│                          ▼                             ▼         │
│                  ┌────────────────┐            ┌────────────────┐│
│                  │ FileCollector  │            │ FileCollector  ││
│                  │                │            │                ││
│                  │ - fsnotify     │            │ - fsnotify     ││
│                  │ - readFile     │            │ - readFile     ││
│                  └───────┬────────┘            └───────┬────────┘│
│                          │                             │         │
│                          └──────────────┬──────────────┘         │
│                                         ▼                        │
│                              ┌────────────────────┐              │
│                              │ HTTPUploader (全局) │              │
│                              │                    │              │
│                              │ - buffer           │              │
│                              │ - uploadLoop       │              │
│                              └─────────┬──────────┘              │
└────────────────────────────────────────┼────────────────────────┘
                                         │
                                         ▼
                              ┌────────────────────┐
                              │   FOPS Server      │
                              │                    │
                              │ /flog/upload       │
                              │ /linkTrace/upload  │
                              └────────────────────┘
```

## 核心组件

### 1. ContainerManager (`container/manager.go`)

容器管理器，负责：
- 监听 Docker 事件（容器创建/销毁）
- 收集容器资源统计信息（CPU、内存）
- 通知 Observer 容器变化

**关键方法：**
- `Start(ctx)` - 启动管理器，加载现有容器，启动事件监听和资源收集
- `Stop()` - 停止管理器
- `Subscribe(observer)` - 订阅容器变化事件
- `GetStats(containerID)` - 获取单个容器资源
- `GetAllStats()` - 获取所有容器资源

### 2. WatcherManager (`watcher/manager.go`)

文件监视器管理器，实现 `container.Observer` 接口：
- 预创建全局 HTTPUploader（每个 collector 一个）
- 管理每个容器的 ContainerWatcher
- 容器创建时创建对应的监视器
- 容器销毁时清理对应的监视器

### 3. ContainerWatcher (`watcher/container_watcher.go`)

容器文件监视器：
- 检测应用名称（通过扫描目录）
- 为每个 collector 创建 FileCollector
- 注册回调到全局上传器

### 4. FileCollector (`collector/file_collector.go`)

文件采集器：
- 使用 fsnotify 监听目录变化
- 增量读取文件内容（字节偏移量）
- 处理文件轮转（rotate）
- 支持超大行（无 Scanner 限制）

**关键特性：**
- 增量读取：记录字节偏移量，只读取新增内容
- 文件轮转：自动检测文件变小并重置偏移量
- 删除策略：输出成功后，等待新文件出现再删除旧文件

### 5. HTTPUploader (`uploader/http_uploader.go`)

HTTP 上传器：
- 全局共享，多个容器复用
- 缓冲区管理
- 定时批量上传
- 支持 HTTPS（跳过证书验证）

**配置参数：**
- `UploadInterval` - 上传间隔（秒）
- `BufferSizeMB` - 缓冲区大小（MB）

## 数据流

```
1. 容器启动事件
   Docker Events → ContainerManager.Handle() → notifyAdd()
   → WatcherManager.OnContainerAdd() → NewContainerWatcher()
   → FileCollector.Start() → scanExistingFiles() → readFile()

2. 文件写入事件
   fsnotify.Write → FileCollector.handleFileWrite()
   → readFile() → HTTPUploader.Write()

3. 数据上传
   HTTPUploader.uploadLoop() → flush() → upload()
   → 回调 OnOutputSuccess() → 尝试删除已输出文件
```

## 配置说明

```yaml
Fops:
  WsServer: "ws://127.0.0.1:8889"  # FOPS 服务器地址

Container:
  OffsetDir: /var/lib/fops-agent/offset  # 偏移量存储目录
  IgnoreNames:                           # 忽略的容器（前缀匹配）
    - "portainer-agent"
    - "traefik"
  StatsInterval: 3                       # 资源收集间隔（秒）

Collectors:
  - Name: log                            # 采集器名称
    WatchDir: /var/log/flog/{app}/       # 监听目录（支持 {app} 占位符）
    FileExt: log                         # 文件扩展名
    UploadURL: /flog/upload              # 上传路径
    UploadInterval: 5                    # 上传间隔（秒）
    BufferSizeMB: 10                     # 缓冲区大小（MB）
```

## 部署方式

### Docker Swarm 部署

```bash
docker service create \
  --name fops-agent \
  --mount type=bind,source=/var/run/docker.sock,target=/var/run/docker.sock \
  --mount type=bind,source=/proc,target=/host/proc,readonly \
  --cap-add SYS_PTRACE \
  -e TZ=Asia/Shanghai \
  fops-agent:latest
```

### 主机直接运行

```bash
./fops-agent
```

程序会自动检测运行环境：
- 存在 `/host/proc` → Docker 环境
- 不存在 `/host/proc` → 主机环境

## 关键接口

### container.Observer

```go
type Observer interface {
    OnContainerAdd(container *docker.ContainerIdInspectJson)
    OnContainerRemove(containerID string)
}
```

### collector.Collector

```go
type Collector interface {
    Name() string
    Start(ctx context.Context) error
    Stop()
    OnOutputSuccess(filePath string)
}
```

### output.Output

```go
type Output interface {
    Name() string
    Start() error
    Stop()
    Write(data *Data)
    RegisterCallback(collectorName string, callback func(filePath string))
}
```

## 依赖

- `github.com/farseer-go/docker` - Docker 客户端
- `github.com/farseer-go/fs` - 日志和配置
- `github.com/fsnotify/fsnotify` - 文件系统监控

## 注意事项

1. **偏移量管理**：偏移量按字节计算，确保增量读取
2. **文件删除**：只有在新文件出现后才删除已输出的旧文件
3. **全局上传器**：多个容器共享同一个上传器，批量上传效率更高
4. **资源收集**：启动时立即同步收集一次，之后定时收集