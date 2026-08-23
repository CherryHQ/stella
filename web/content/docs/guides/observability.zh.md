---
title: 可观测性
---

## 概述

可观测性让你看清 stella 在底层做了什么。每一次 LLM 调用、工具执行和记忆操作都会记录耗时、token 用量和错误详情。Web UI 与 API 的入站 HTTP 请求也会被追踪。

追踪是内置在服务里的——不需要在插件页面启用任何东西。它有两种模式：

- **日志模式**（始终开启）——通过 Go 的 `slog` 输出结构化日志行，可在 stderr 中查看。无需任何配置。
- **OpenTelemetry 模式**（可选）——通过标准 OTLP 环境变量导出分布式追踪和日志。支持 OTLP/gRPC 和 OTLP/HTTP，包括需要认证的后端。设置 OTLP 端点即可启用。

## 配置

### 日志模式

日志模式始终生效。通过 `LOG_LEVEL` 控制详细程度：

| 级别           | 你会看到                                                                         |
| -------------- | -------------------------------------------------------------------------------- |
| `INFO`（默认） | 每次 LLM 调用（模型、token、耗时、TTFT）、工具调用（名称、耗时、错误）和记忆操作 |
| `DEBUG`        | 与 INFO 相同，外加内部引擎事件                                                   |
| `TRACE`        | 与 DEBUG 相同，外加完整的记忆操作详情（消息内容、搜索结果、画像文本）            |

```bash
# 默认 —— LLM/工具/记忆事件以 INFO 级别输出
stellad server

# 详细 —— 包含记忆细节字段
LOG_LEVEL=TRACE stellad server
```

日志输出示例：

```
level=INFO msg=post_llm_call hook=trace provider=anthropic model=claude-sonnet-4-20250514 stop_reason=tool_use duration=3.2s ttft=450ms input_tokens=12500 output_tokens=350 cache_read=8000
level=INFO msg=post_tool_call hook=trace tool=bash call_id=call_01 is_error=false duration=1.5s result_len=256
level=INFO msg=post_memory_call hook=trace op=compact duration=200ms token_count=8000 token_delta=-4500
```

每个 HTTP 请求还会记录一条 INFO 日志（`msg="http request"`，包含方法、路径、状态码、耗时、响应大小，启用追踪时还带 `trace_id`，方便从日志跳到对应 trace）。返回服务端错误的请求记为 ERROR；健康检查和静态资源记为 DEBUG。

驱动定时和后台工作的内部任务队列默认只记录警告和错误，避免淹没上面的信号。调试定时任务时，可设置 `LOG_LEVEL_RIVER`（取值与 `LOG_LEVEL` 相同）打开它：

```bash
LOG_LEVEL_RIVER=DEBUG stellad server
```

### OpenTelemetry 模式

设置 `OTEL_EXPORTER_OTLP_ENDPOINT` 会同时启用追踪、日志和指标；也可以用信号专用导出器变量只启用部分信号。指标覆盖 HTTP 服务器（请求数、耗时）和 Go 运行时（内存、GC、goroutine）。如果后端不支持日志服务（如 Jaeger），Stella 会在首次失败后自动禁用日志导出。Stella 将导出器配置交给 OpenTelemetry SDK，因此支持标准的 OTel 环境变量：

| 环境变量                             | 默认值              | 说明                                                                                                                                                                            |
| ------------------------------------ | ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `OTEL_EXPORTER_OTLP_ENDPOINT`        | _(空 —— OTel 关闭)_ | 所有信号共用的 OTLP 基础端点。OTLP/HTTP 使用完整 URL，如 `https://collector.example.com/api/default`；OTLP/gRPC 使用带 scheme 的 URL，如 `https://collector.example.com:4317`。 |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | _(空)_              | Trace 专用 OTLP 端点。会覆盖 trace 使用的通用端点。                                                                                                                             |
| `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT`   | _(空)_              | Log 专用 OTLP 端点。会覆盖 log 使用的通用端点。                                                                                                                                 |
| `OTEL_EXPORTER_OTLP_PROTOCOL`        | SDK 默认            | 导出协议。常见值：`grpc` 或 `http/protobuf`。                                                                                                                                   |
| `OTEL_EXPORTER_OTLP_HEADERS`         | _(空)_              | 应用于所有 OTLP 信号的逗号分隔请求头，例如 `authorization=Bearer <token>`。                                                                                                     |
| `OTEL_EXPORTER_OTLP_TRACES_HEADERS`  | _(空)_              | 仅应用于 traces 的逗号分隔请求头。会覆盖针对 traces 的通用 OTLP 请求头。                                                                                                        |
| `OTEL_EXPORTER_OTLP_LOGS_HEADERS`    | _(空)_              | 仅应用于 logs 的逗号分隔请求头。会覆盖针对 logs 的通用 OTLP 请求头。                                                                                                            |
| `OTEL_TRACES_EXPORTER`               | SDK 默认            | Trace 导出器。设为 `none` 可只关闭 trace 导出，保留其他 OTel 信号。                                                                                                             |
| `OTEL_LOGS_EXPORTER`                 | SDK 默认            | Log 导出器。设为 `none` 可只关闭 log 导出，保留 trace。                                                                                                                         |
| `OTEL_METRICS_EXPORTER`              | SDK 默认            | 指标导出器。设为 `none` 可只关闭指标导出，保留其他 OTel 信号。                                                                                                                  |
| `OTEL_SERVICE_NAME`                  | `stella`            | 在可观测性后端显示的服务名。                                                                                                                                                    |
| `OTEL_RESOURCE_ATTRIBUTES`           | _(空)_              | 附加到所有信号的资源属性，例如 `deployment.environment=prod`。                                                                                                                  |
| `OTEL_EXPORTER_OTLP_INSECURE`        | SDK 默认            | 设为 `false` 以要求 TLS。HTTPS 或安全 gRPC 端点请使用 `false`。                                                                                                                 |
| `OTEL_STELLA_RECORD_TOOL_IO`         | `false`             | 设为 `true` 才会把工具输入(如 bash 命令)和结果文本记录到 span。默认关闭,因此这些内容永不导出;span 始终携带工具名、参数数量与结果长度。                                          |

启用 OTel 后，两种模式会同时运行——你既能看到 stderr 日志行，也能导出追踪、日志和指标。处于追踪路径上的日志行（LLM 调用、工具调用、HTTP 请求）会携带 `trace_id` 和 `span_id`，可以直接从 stderr 行跳到后端里对应的 trace。

### 常见陷阱

- **`OTEL_EXPORTER_OTLP_ENDPOINT` 必须带 scheme。** 用 `https://collector.example.com:4317`，而不是 `collector.example.com:4317`。省略 scheme 可能产生畸形的导出 URL，如 `http:///v1/traces`。
- **为后端选用正确的协议。** OTLP/HTTP 通常需要 `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf`；OTLP/gRPC 通常用 `OTEL_EXPORTER_OTLP_PROTOCOL=grpc`。
- **OTLP/HTTP 请设置基础路径，而非 `/v1/traces`。** 导出器会自动追加 `/v1/traces`。
- **TLS 端点不要设 `OTEL_EXPORTER_OTLP_INSECURE=true`。** 安全的采集器应使用 `OTEL_EXPORTER_OTLP_INSECURE=false`。
- **请求头是逗号分隔的 `key=value` 对，值内不要加 shell 引号。** 例如：`authorization=Basic abc123,organization=default`。
- **出站 URL 与错误文本不会进入 span。** 经由共享 HTTP 客户端发出的请求只记主机名，不记路径和查询串（网关可能把 API key 放在里面）。请求失败只记 Go 错误类型和一段固定文案；错误消息留在日志里，因为任何脱敏黑名单都覆盖不了上游自创的凭证字段名。这是 transport 的性质，因此对所有使用它的调用方成立，而不只是对记得申请的那些。所有模型流量都走它；部分渠道 SDK 和集成仍用各自的 HTTP 客户端，它们根本不产生客户端 span。
- **工具输入/结果默认不导出，需显式开启。** 仅在你信任采集器时才设 `OTEL_STELLA_RECORD_TOOL_IO=true`——它会把 bash 命令和工具输出送出本机,且尽力而为的密钥脱敏并非保证。

## 配合 Jaeger 使用

[Jaeger](https://www.jaegertracing.io/) 是可视化 stella 追踪最简单的方式。它原生接受 OTLP，单容器即可运行。

```bash
# 启动 Jaeger
docker run -d --name jaeger \
  -p 16686:16686 \
  -p 4317:4317 \
  jaegertracing/jaeger:latest

# 通过 OTLP/gRPC 启动带追踪和日志导出的 stella
OTEL_EXPORTER_OTLP_PROTOCOL=grpc \
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317 \
stellad server
```

打开 `http://localhost:16686`，选择 **stella** 服务，点击 **Find Traces**。每个对话会话都会显示为一条追踪，并以瀑布图展示 LLM 调用、工具执行和记忆操作。Jaeger 主要面向追踪；如果还要检索导出的日志，请使用支持日志的后端或采集器管道。

### 配合 Grafana LGTM 使用

[grafana/otel-lgtm](https://grafana.com/docs/opentelemetry/docker-lgtm/) 是 Grafana 官方为本地开发和测试提供的一体化 OTel 后端。单个 Docker 镜像内包含 OpenTelemetry Collector、Grafana、Loki（日志）、Tempo（追踪）和 Mimir（指标）——一个容器就能接收并可视化所有信号。

```bash
# 启动一体化 stack
docker run --rm -d \
  -p 3000:3000 \
  -p 4317:4317 \
  -p 4318:4318 \
  --name otel-lgtm \
  grafana/otel-lgtm

# 通过 OTLP/HTTP 启动带追踪和日志的 stella
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf \
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 \
stellad server
```

打开 `http://localhost:3000`（Grafana UI）。使用 **Explore → Tempo** 查看追踪瀑布图，使用 **Explore → Loki** 查看日志。如果日志中包含 `trace_id`，Grafana 会自动关联追踪与日志。

> **开发 Stella 本身？** 不必手动 `docker run`。`mise run dev:docker` 会用本地构建的镜像拉起整套 `docker-compose.yml` 栈——`stellad` 加一个 `otel-lgtm` 边车，OTLP 已接到 `otel` 服务——无需额外配置。Grafana 在 `http://localhost:13413`。用 `docker compose down` 停掉。

`grafana/otel-lgtm` 适合本地开发，不建议用于生产环境。生产部署请使用独立的 OTel Collector 或 Grafana Alloy，将数据转发到独立的 Loki、Tempo 和 Mimir 实例。

### 配合其他后端使用

任何兼容 OTLP 的后端都可使用。示例：

```bash
# Grafana Tempo（OTLP/gRPC）
OTEL_EXPORTER_OTLP_PROTOCOL=grpc \
OTEL_EXPORTER_OTLP_ENDPOINT=http://tempo.internal:4317 \
stellad server

# SigNoz（OTLP/gRPC）
OTEL_EXPORTER_OTLP_PROTOCOL=grpc \
OTEL_EXPORTER_OTLP_ENDPOINT=http://signoz.internal:4317 \
stellad server

# OTel Collector（OTLP/gRPC）
OTEL_EXPORTER_OTLP_PROTOCOL=grpc \
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4317 \
stellad server

# 云端 OTLP/gRPC，带 TLS 和认证头
OTEL_EXPORTER_OTLP_PROTOCOL=grpc \
OTEL_EXPORTER_OTLP_ENDPOINT=https://otlp.vendor.com:443 \
OTEL_EXPORTER_OTLP_TRACES_HEADERS="authorization=Bearer <token>" \
OTEL_EXPORTER_OTLP_INSECURE=false \
stellad server

# 云端 OTLP/HTTP，带 TLS 和认证头
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf \
OTEL_EXPORTER_OTLP_ENDPOINT=https://otlp.vendor.com/api/default \
OTEL_EXPORTER_OTLP_TRACES_HEADERS="authorization=Basic <base64>,organization=default,stream-name=default" \
OTEL_EXPORTER_OTLP_INSECURE=false \
stellad server
```

如果你的服务商给的是 OTLP/HTTP 端点，如 `https://collector.example.com/api/default`，请直接使用该基础 URL，让导出器自动追加 `/v1/traces`。

## 追踪了哪些内容

## 会话用量 API

`GET /api/agents/{agentId}/sessions/{sessionId}/usage` 按服务商和模型分组，返回服务商实际报告的输入、输出、缓存读取、缓存写入 token 以及美元成本。它不会使用消息长度估算 token。四类 token 互不重叠：输入只统计未命中缓存的部分，因此每一类都按各自的价格计费。

响应包含全部调用次数。任一次调用未报告用量时，token 总计为 `null`；任一次调用未报告用量或模型没有配置费率时，成本为 `null`。这样不会把服务商未报告的数据误读为免费。用量写入不在聊天路径上，而是在有界内存队列中运行：正常关闭会排空队列；进程丢失最多会丢失 1,024 条已接受的观测；数据库持续过载时会丢弃新观测，而不会拖慢用户回合。

### LLM 调用

每次对 LLM 服务商的调用都会记录为 `gen_ai.chat` span：

- 模型名（请求的与实际的）
- 服务商（anthropic、openai 等）
- token 用量：输入、输出、缓存读取、缓存写入
- 首 token 时间（TTFT）
- 总耗时
- 停止原因（end_turn、tool_use、max_tokens 等）
- 服务商请求次数与重试次数
- 错误

服务商 SDK 会在一次调用内部重试，因此每次网络请求都有自己的子 span
`gen_ai.chat.request`，带上尝试序号、响应状态码与服务器主机名。它的耗时是请求
本身（连接、发送、首字节），不含流式响应——那是父 span 的耗时。

经由共享 HTTP 客户端的每个请求恰好产生一个 span，在响应头返回时结束。非模型调用的请求走同一个
span，只是名字是通用的 `HTTP <METHOD>`；模型调用的 context 额外补上 `gen_ai`
属性和尝试序号，仅此而已。这样分层是刻意的:调用方忘了标记，损失的是一个 span
的语义，而不是一个密钥。

### 工具执行

每次工具调用都会记录为 `gen_ai.execute_tool` span：

- 工具名（bash、vllm、webfetch、agent 等）
- 调用 ID
- 耗时
- 成功或失败，并带错误类别：`tool_error`（工具本身坏了）或 `command_nonzero`
  （命令跑完并以非零码退出）
- 命令退出码（如果有）

`command_nonzero` 不会把 span 状态标记为错误。工具是好的，是命令说了不。
把它算作失败，会让正常的探索（比如没匹配到的 `grep`）在错误率视图里变成故障。

### 记忆操作

记忆操作（append、assemble、compact、search 等）会记录为 `memory.*` span：

- 操作类型
- 耗时
- token 与消息数
- token delta（压缩时）
- 错误

### 沙箱生命周期

沙箱启动会记录为 `sandbox.*` span，这样沙箱失败可以追溯到最终的 broken-pipe 现象之前：

- 运行器中的会话创建
- 后端启动及 overlay/会话目录设置
- 沙箱客户端进程启动
- JSON-RPC 握手
- 会话关闭或失活原因

这些 span 包含 Stella 特定属性，如沙箱后端、源/目标根目录、工作目录、网络模式、只读绑定数量和捕获的错误类型。

### HTTP 请求

Web UI 与 API 的入站请求会记录为 `http.server` span，让你可以端到端追踪面向用户的延迟。

经由共享 HTTP 客户端发出的出站请求——所有模型服务商，以及嵌入、技能拉取和使用它的渠道——会记录为 `HTTP <METHOD>` 客户端 span，带方法、目标主机和响应状态。它们在响应头返回时结束，并会传播 W3C trace context 与 baggage，下游服务因此接在同一条 trace 上。

部分集成仍通过各自的 HTTP 客户端出网（几个渠道 SDK、OIDC provider、MCP 客户端），它们只能借外层 span 出现在 trace 里。

### 追踪结构

Span 按每个对话会话组织成层级结构：

```
chat
  └── turn 1
       ├── gen_ai.chat                 3.2s
       │    └── gen_ai.chat.request    0.4s
       ├── gen_ai.execute_tool (bash)  1.5s
       ├── gen_ai.execute_tool (bash)  0.1s
       └── memory.append               0.02s
  └── turn 2
       ├── gen_ai.chat                 2.8s
       └── memory.compact              0.2s
```

每次 stella 调用 LLM 都会开始一个新的 **turn**。**chat** 根 span 覆盖整个对话，在 2 分钟无活动后关闭。

## Span 属性参考

LLM 与工具 span 遵循 [OpenTelemetry GenAI 语义约定](https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-spans/)：

| 属性                                       | Span         | 说明                             |
| ------------------------------------------ | ------------ | -------------------------------- |
| `gen_ai.operation.name`                    | 全部         | `chat` 或 `execute_tool`         |
| `gen_ai.request.attempt`                   | chat.request | 尝试序号，从 1 开始              |
| `gen_ai.request.attempts`                  | chat         | 服务商 HTTP 请求次数             |
| `gen_ai.request.retry_count`               | chat         | 请求次数减一                     |
| `gen_ai.provider.name`                     | chat         | 服务商标识                       |
| `gen_ai.request.model`                     | chat         | 请求的模型                       |
| `gen_ai.response.model`                    | chat         | 实际使用的模型                   |
| `gen_ai.response.finish_reasons`           | chat         | 生成停止的原因                   |
| `gen_ai.conversation.id`                   | 全部         | 会话 ID                          |
| `gen_ai.usage.input_tokens`                | chat         | 输入 token                       |
| `gen_ai.usage.output_tokens`               | chat         | 输出 token                       |
| `gen_ai.usage.cache_read.input_tokens`     | chat         | 缓存命中的输入 token             |
| `gen_ai.usage.cache_creation.input_tokens` | chat         | 写入缓存的 token                 |
| `gen_ai.server.time_to_first_token`        | chat         | TTFT（秒）                       |
| `gen_ai.tool.name`                         | execute_tool | 工具名                           |
| `gen_ai.tool.call.id`                      | execute_tool | 工具调用 ID                      |
| `gen_ai.tool.error_kind`                   | execute_tool | `tool_error` / `command_nonzero` |
| `gen_ai.tool.exit_code`                    | execute_tool | 命令退出码                       |
| `error.type`                               | 全部         | 失败时的错误类型                 |

记忆 span 使用 stella 特定属性：

| 属性                          | 说明                                                                      |
| ----------------------------- | ------------------------------------------------------------------------- |
| `stella.memory.op`            | 操作（bootstrap、append、assemble、compact、search、describe、expand 等） |
| `stella.memory.session_id`    | 记忆会话 ID                                                               |
| `stella.memory.token_count`   | token 数                                                                  |
| `stella.memory.token_delta`   | 压缩节省的 token（负值表示减少）                                          |
| `stella.memory.message_count` | 消息数                                                                    |

沙箱生命周期 span 使用以下 Stella 特定属性：

| 属性                                | 说明                                    |
| ----------------------------------- | --------------------------------------- |
| `stella.sandbox.backend`            | 沙箱后端名（`local`、`docker`、`none`） |
| `stella.sandbox.agent_root`         | 运行器配置中的 agent 工作区根目录       |
| `stella.sandbox.user_root`          | 请求的沙箱用户根目录                    |
| `stella.sandbox.resolved_user_root` | 用于构建策略的解析后绝对用户根目录      |
| `stella.sandbox.project_root`       | 存在时的项目根目录                      |
| `stella.sandbox.work_dir`           | 请求的或解析后的工作目录                |
| `stella.sandbox.src`                | 写时复制源根目录                        |
| `stella.sandbox.dst`                | overlay/会话目标根目录                  |
| `stella.sandbox.cwd`                | 会话内重映射的工作目录                  |
| `stella.sandbox.network.mode`       | 生效的网络模式                          |
| `stella.sandbox.network.allowlist`  | 配置时的网络白名单                      |
| `stella.sandbox.readonly_dir_count` | 只读绑定目录数量                        |
| `stella.sandbox.close_reason`       | 会话 span 结束的原因                    |
| `stella.sandbox.server.name`        | 握手返回的沙箱服务名                    |
| `stella.sandbox.server.version`     | 握手返回的沙箱服务版本                  |
| `stella.sandbox.protocol_version`   | 沙箱返回的 RPC 协议版本                 |

## 如何关闭

没有可禁用的插件。日志模式跟随 `LOG_LEVEL`，设为 `WARN` 或 `ERROR` 即可静默每次调用的 INFO 行。除非设置了 `OTEL_EXPORTER_OTLP_ENDPOINT` 或信号专用导出器，否则 OTel 导出默认关闭；留空这些变量即可完全停用分布式遥测。设 `OTEL_TRACES_EXPORTER=none`、`OTEL_LOGS_EXPORTER=none` 或 `OTEL_METRICS_EXPORTER=none` 可以按信号单独关闭。
