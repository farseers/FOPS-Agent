---
name: fops-agent-proxy-upload
description: 处理 fops-agent 采集 nginx/traefik 代理日志并上传到 FOPS /flog/proxyLog 的配置、代码或排查时使用。
---

# fops-agent 代理日志上传技能

当任务涉及 fops-agent 采集 nginx、traefik 等代理日志，或排查日志如何上传到 FOPS `/flog/proxyLog` 时，优先使用本技能。

## 当前数据链路

1. `main.go` 加载配置并启动采集管理器。
2. `watcher/collectorManager.go` 为每个 collector 创建一个全局 `HTTPUploader`，并启动主机级采集器。
3. `watcher/containerCollector.go` 为匹配的容器级 collector 创建 `FileCollector`。
4. `collector/file_collector.go` 监听日志文件，按增量读取新增行，并写入对应输出器。
5. `uploader/http_uploader.go` 批量组装 JSON 或 MessagePack 请求体，超过阈值时使用 zstd 压缩，然后 POST 到 FOPS。
6. FOPS 在 `/flog/proxyLog` 接收代理日志；其中 `AppName=traefik` 的日志会继续解析并用于访问统计页面。

## 关键文件

- `config.yaml`：默认 collector 配置。
- `dist/config.yaml`、`dist/config-master.yaml`：发布配置副本；修改采集器配置样例时要同步考虑。
- `config/config.go`：collector 配置字段、默认值、采集范围判断、`ws/wss` 到 `http/https` 的 FOPS 地址转换。
- `main.go`：启动入口。
- `watcher/collectorManager.go`：全局 uploader 创建、主机级采集器启动。
- `watcher/containerCollector.go`：容器级采集器匹配逻辑。
- `collector/file_collector.go`：监听路径解析、文件增量读取。
- `uploader/http_uploader.go`：请求体结构、压缩逻辑、HTTP 上传逻辑。

## Collector 配置字段

- `Name`：采集器名称。
- `Scope`：采集范围，可选 `Container`、`Host`、`Both`；默认 `Container`。
- `AppName`：上传时携带的应用名；同一批次应用名一致时会写入请求体顶层 `AppName`。
- `ContainerNames`：容器名或 Swarm 服务名白名单；仅容器级采集器使用。
- `WatchDir`：监听目录；Host 模式直接使用，Container 模式会解析到容器根目录下。
- `FileExt`：监听的文件扩展名。
- `UploadURL`：FOPS 上传路径。
- `UploadInterval`：上传间隔秒数；默认 `5`。
- `BufferSizeMB`：上传缓冲区大小；默认 `10`。
- `SerializeType`：序列化方式，常见为 `json`、`txt`、`messagePack`；默认 `json`。
- `CompressThresholdKB`：zstd 压缩阈值；默认 `128` KB。

## 现有代理日志采集器

`config.yaml` 当前包含这些代理日志 collector：

- `nginx-log`
  - `Scope: Host`
  - `AppName: nginx`
  - `WatchDir: /var/log/nginx/`
  - `FileExt: log`
  - `UploadURL: /flog/proxyLog`
  - `SerializeType: json`

- `nginx-errorlog`
  - `Scope: Host`
  - `AppName: nginx-error`
  - `WatchDir: /var/log/nginx-error/`
  - `FileExt: log`
  - `UploadURL: /flog/proxyLog`
  - `SerializeType: txt`

- `traefik-log`
  - `Scope: Container`
  - `AppName: traefik`
  - `ContainerNames: ["traefik", "traefik-worker"]`
  - `WatchDir: /var/log/traefik/`
  - `FileExt: log`
  - `UploadURL: /flog/proxyLog`
  - `SerializeType: json`

## 上传请求格式

JSON 上传时，`HTTPUploader.buildJSON` 的请求体规则是：

- 同一批次所有数据都有同一个 app name 时：
  - `{ "AppName": "traefik", "List": [ ...原始日志行... ] }`
- 批次内 app name 不一致时：
  - `{ "List": [ ...原始日志行... ] }`

`List` 中每一项的规则：
- 如果原始行本身是合法 JSON，则按原始 JSON 写入。
- 如果不是合法 JSON，则作为字符串进行 JSON 转义。

MessagePack 上传时，请求体是只包含 `List` 的 map。除非确认 FOPS 接收端支持，否则不要把 `/flog/proxyLog` 改为 MessagePack。

HTTP 行为：
- `Fops.WsServer` 会转换成 HTTP 基础地址：`ws://` 转 `http://`，`wss://` 转 `https://`。
- 最终上传地址为转换后的基础地址加 `UploadURL`。
- `Content-Type` 为 `application/json` 或 `application/x-msgpack`。
- 启用压缩时设置 `Content-Encoding: zstd`。
- 只有 HTTP `200 OK` 算上传成功；失败批次会放回 buffer，避免丢日志。

## Traefik 特别注意

- FOPS 只有在上传请求的 `AppName` 等于 `traefik` 时才解析 Traefik 访问日志。
- Traefik 访问日志应为 JSON 行，常用字段包括：`StartUTC`、`ClientAddr`、`ClientHost`、`RequestHost`、`RequestPath`、`RequestLine`、`DownstreamStatus`、`ServiceName`、`Duration`、`OriginDuration`。
- 容器级 Traefik 采集依赖 `ContainerNames` 匹配容器名或 Swarm 服务名，且容器内存在 `WatchDir` 指向的日志目录。
- Container 模式实际监听路径是 `/proc/<pid>/root/<WatchDir>`；在 Docker 部署并挂载 `/host/proc` 时是 `/host/proc/<pid>/root/<WatchDir>`。
- 如果 Traefik 日志写在宿主机而不是容器内，应有意识地调整 `Scope` 和 `WatchDir`，不要改变 FOPS 对 `AppName=traefik` 的解析约定。

## 排查清单

1. 确认 `Fops.WsServer` 能转换出正确的 FOPS HTTP 地址。
2. 确认 Traefik collector 使用 `UploadURL: /flog/proxyLog` 和 `AppName: traefik`。
3. 对容器级采集，确认 `ContainerNames` 能匹配实际 Docker 容器名或 Swarm 服务名。
4. 确认实际监听路径存在：
   - Host 模式：`WatchDir`。
   - Container 模式：`/proc/<pid>/root/<WatchDir>` 或 `/host/proc/<pid>/root/<WatchDir>`。
5. 确认 Traefik 访问日志是合法 JSON 行，没有被上游重复转义。
6. 查看 uploader 日志中是否有非 200 响应或持续上传失败。
7. 如果 FOPS 已收到原始日志但 Vue 没有 Traefik 统计数据，切换到 FOPS 仓库并使用 `fops-proxy-log` 技能检查解析和 ClickHouse 写入。

## 建议检查

- 修改 agent 代码后，在可行时执行：`go test ./...`。
- 修改采集器配置样例时，同步检查 `config.yaml`、`dist/config.yaml`、`dist/config-master.yaml`。
