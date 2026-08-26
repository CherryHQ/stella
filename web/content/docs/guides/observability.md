---
title: Observability
---

## Overview

Observability gives you visibility into what stella is doing under the hood. Every LLM call, tool execution, and memory operation is tracked with timing, token usage, and error details. Inbound HTTP requests to the Web UI and API are traced too.

Tracing is built into the server — there is nothing to enable on the Plugins page. It operates in two modes:

- **Log mode** (always on) -- structured log lines via Go's `slog`, visible in stderr. Zero configuration needed.
- **OpenTelemetry mode** (opt-in) -- exports distributed traces and logs via standard OTLP environment variables. Both OTLP/gRPC and OTLP/HTTP are supported, including authenticated backends. Activate by setting an OTLP endpoint.

## Configuration

### Log Mode

Log mode is always active. Control verbosity with `LOG_LEVEL`:

| Level            | What You See                                                                                            |
| ---------------- | ------------------------------------------------------------------------------------------------------- |
| `INFO` (default) | Every LLM call (model, tokens, duration, TTFT), tool call (name, duration, error), and memory operation |
| `DEBUG`          | Same as INFO plus internal engine events                                                                |
| `TRACE`          | Same as DEBUG plus full memory operation details (message content, search results, profile text)        |

```bash
# Default -- LLM/tool/memory events at INFO
stellad server

# Verbose -- includes memory detail fields
LOG_LEVEL=TRACE stellad server
```

Example log output:

```
level=INFO msg=post_llm_call hook=trace provider=anthropic model=claude-sonnet-4-20250514 stop_reason=tool_use duration=3.2s ttft=450ms input_tokens=12500 output_tokens=350 cache_read=8000
level=INFO msg=post_tool_call hook=trace tool=bash call_id=call_01 is_error=false duration=1.5s result_len=256
level=INFO msg=post_memory_call hook=trace op=compact duration=200ms token_count=8000 token_delta=-4500
```

Every HTTP request also logs one INFO line (`msg="http request"` with method, path, status, duration, response size, and — when tracing is enabled — the request's `trace_id`, so you can jump from a log line to its trace). Requests that fail with a server error log at ERROR; health probes and static assets log at DEBUG.

The internal job queue that drives scheduled and background work logs only warnings and errors by default, so it does not drown the signals above. Set `LOG_LEVEL_RIVER` (same values as `LOG_LEVEL`) to open it up when debugging scheduled work:

```bash
LOG_LEVEL_RIVER=DEBUG stellad server
```

### OpenTelemetry Mode

Set `OTEL_EXPORTER_OTLP_ENDPOINT` to enable traces, logs, and metrics together, or use signal-specific exporter variables to enable only some signals. Metrics cover the HTTP server (request counts, durations) and the Go runtime (memory, GC, goroutines). If the backend does not support the logs service (e.g. Jaeger), Stella detects the first failure and silently disables log export. Stella delegates exporter configuration to the OpenTelemetry SDK, so standard OTel environment variables are supported:

| Environment Variable                 | Default                    | Description                                                                                                                                                                                               |
| ------------------------------------ | -------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `OTEL_EXPORTER_OTLP_ENDPOINT`        | _(empty -- OTel disabled)_ | OTLP base endpoint for all signals. For OTLP/HTTP, use a full URL such as `https://collector.example.com/api/default`. For OTLP/gRPC, use a URL with scheme such as `https://collector.example.com:4317`. |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | _(empty)_                  | Trace-specific OTLP endpoint. Overrides the generic endpoint for traces.                                                                                                                                  |
| `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT`   | _(empty)_                  | Log-specific OTLP endpoint. Overrides the generic endpoint for logs.                                                                                                                                      |
| `OTEL_EXPORTER_OTLP_PROTOCOL`        | SDK default                | Export protocol. Common values: `grpc` or `http/protobuf`.                                                                                                                                                |
| `OTEL_EXPORTER_OTLP_HEADERS`         | _(empty)_                  | Comma-separated headers applied to all OTLP signals, for example `authorization=Bearer <token>`.                                                                                                          |
| `OTEL_EXPORTER_OTLP_TRACES_HEADERS`  | _(empty)_                  | Comma-separated headers applied to traces only. Overrides generic OTLP headers for traces.                                                                                                                |
| `OTEL_EXPORTER_OTLP_LOGS_HEADERS`    | _(empty)_                  | Comma-separated headers applied to logs only. Overrides generic OTLP headers for logs.                                                                                                                    |
| `OTEL_TRACES_EXPORTER`               | SDK default                | Trace exporter. Set to `none` to disable trace export while keeping other OTel signals available.                                                                                                         |
| `OTEL_LOGS_EXPORTER`                 | SDK default                | Log exporter. Set to `none` to disable log export while keeping traces available.                                                                                                                         |
| `OTEL_METRICS_EXPORTER`              | SDK default                | Metric exporter. Set to `none` to disable metric export while keeping other OTel signals available.                                                                                                       |
| `OTEL_SERVICE_NAME`                  | `stella`                   | Service name shown in your observability backend.                                                                                                                                                         |
| `OTEL_RESOURCE_ATTRIBUTES`           | _(empty)_                  | Extra resource attributes attached to every signal, for example `deployment.environment=prod`.                                                                                                            |
| `OTEL_EXPORTER_OTLP_INSECURE`        | SDK default                | Set to `false` to require TLS. Use `false` for HTTPS or secure gRPC endpoints.                                                                                                                            |
| `OTEL_STELLA_RECORD_TOOL_IO`         | `false`                    | Set to `true` to record tool input (e.g. bash commands) and result text on spans. Off by default so this content is never exported; spans always carry tool name, argument count, and result length.      |

When OTel is enabled, both modes run simultaneously -- you get stderr log lines plus exported traces, logs, and metrics. Log lines written on a traced path (LLM calls, tool calls, HTTP requests) carry `trace_id` and `span_id`, so you can jump from a stderr line straight to the trace in your backend.

### Common Pitfalls

- **Always include a scheme in `OTEL_EXPORTER_OTLP_ENDPOINT`.** Use `https://collector.example.com:4317`, not `collector.example.com:4317`. Omitting the scheme can produce malformed export URLs such as `http:///v1/traces`.
- **Use the correct protocol for your backend.** OTLP/HTTP usually needs `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf`; OTLP/gRPC usually uses `OTEL_EXPORTER_OTLP_PROTOCOL=grpc`.
- **For OTLP/HTTP, set the base path, not `/v1/traces`.** The exporter appends `/v1/traces` automatically.
- **Do not set `OTEL_EXPORTER_OTLP_INSECURE=true` for TLS endpoints.** Secure collectors should use `OTEL_EXPORTER_OTLP_INSECURE=false`.
- **Header values are comma-separated `key=value` pairs without shell quotes inside the value.** Example: `authorization=Basic abc123,organization=default`.
- **Outbound URLs and error text never reach a span.** A request made through the shared HTTP client records the host only, never the path or query, which can carry the API key on a gateway. A failed request records the Go error type and a fixed description; the message stays in the logs, because no redaction blacklist can cover every credential-shaped field an upstream invents. This is a property of the transport, so it holds for every caller that uses it rather than for the ones that remembered to ask. All model traffic goes through it; some channel SDKs and integrations still use their own HTTP clients, which produce no client span at all.
- **Tool input/result is not exported unless you opt in.** Set `OTEL_STELLA_RECORD_TOOL_IO=true` only when you trust the collector — it ships bash commands and tool output off-box, and the best-effort secret redaction is not a guarantee.

## Using with Jaeger

[Jaeger](https://www.jaegertracing.io/) is the simplest way to visualize stella's traces. It accepts OTLP natively and runs as a single container.

```bash
# Start Jaeger
docker run -d --name jaeger \
  -p 16686:16686 \
  -p 4317:4317 \
  jaegertracing/jaeger:latest

# Start stella with tracing and logs over OTLP/gRPC
OTEL_EXPORTER_OTLP_PROTOCOL=grpc \
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317 \
stellad server
```

Open `http://localhost:16686`, select the **stella** service, and click **Find Traces**. Each chat session appears as a trace with a waterfall view of LLM calls, tool executions, and memory operations. Jaeger focuses on traces; use a logs-capable backend or collector pipeline when you also want to search exported log records.

### Using with Grafana LGTM

[grafana/otel-lgtm](https://grafana.com/docs/opentelemetry/docker-lgtm/) is Grafana's all-in-one OTel backend for local development and testing. A single Docker image bundles an OpenTelemetry Collector, Grafana, Loki (logs), Tempo (traces), and Mimir (metrics) — one container that can ingest and visualize everything.

```bash
# Start the all-in-one stack
docker run --rm -d \
  -p 3000:3000 \
  -p 4317:4317 \
  -p 4318:4318 \
  --name otel-lgtm \
  grafana/otel-lgtm

# Start stella with traces and logs over OTLP/HTTP
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf \
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 \
stellad server
```

Open `http://localhost:3000` (Grafana UI). Use **Explore → Tempo** for trace waterfalls and **Explore → Loki** for logs. If logs carry a `trace_id`, Grafana links them automatically.

> **Working on Stella itself?** Skip the manual `docker run`. `mise run dev:docker` brings up the full `docker-compose.yml` stack — `stellad` plus an `otel-lgtm` sidecar with OTLP already wired to the `otel` service — using your locally built image. Grafana is available at `http://localhost:13413`. Stop it with `docker compose down`.

`grafana/otel-lgtm` is designed for local development — not production. For production deployments, use a dedicated OTel Collector or Grafana Alloy forwarding to standalone Loki, Tempo, and Mimir instances.

### Using with Other Backends

Any OTLP-compatible backend works. Examples:

```bash
# Grafana Tempo over OTLP/gRPC
OTEL_EXPORTER_OTLP_PROTOCOL=grpc \
OTEL_EXPORTER_OTLP_ENDPOINT=http://tempo.internal:4317 \
stellad server

# SigNoz over OTLP/gRPC
OTEL_EXPORTER_OTLP_PROTOCOL=grpc \
OTEL_EXPORTER_OTLP_ENDPOINT=http://signoz.internal:4317 \
stellad server

# OTel Collector over OTLP/gRPC
OTEL_EXPORTER_OTLP_PROTOCOL=grpc \
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4317 \
stellad server

# Cloud OTLP/gRPC with TLS and auth header
OTEL_EXPORTER_OTLP_PROTOCOL=grpc \
OTEL_EXPORTER_OTLP_ENDPOINT=https://otlp.vendor.com:443 \
OTEL_EXPORTER_OTLP_TRACES_HEADERS="authorization=Bearer <token>" \
OTEL_EXPORTER_OTLP_INSECURE=false \
stellad server

# Cloud OTLP/HTTP with TLS and auth header
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf \
OTEL_EXPORTER_OTLP_ENDPOINT=https://otlp.vendor.com/api/default \
OTEL_EXPORTER_OTLP_TRACES_HEADERS="authorization=Basic <base64>,organization=default,stream-name=default" \
OTEL_EXPORTER_OTLP_INSECURE=false \
stellad server
```

If your provider gives you an OTLP/HTTP endpoint such as `https://collector.example.com/api/default`, use that exact base URL and let the exporter append `/v1/traces`.

## What Gets Traced

## Session usage API

`GET /api/agents/{agentId}/sessions/{sessionId}/usage` returns provider-reported input, output, cache-read, and cache-write tokens plus USD cost, grouped by provider and model. It does not use message-length token estimates. The four token categories are disjoint: input counts only tokens that were not served from cache, so each category is priced at its own rate.

The response includes all call counts. Token totals are `null` if any call did not report usage; cost is `null` if any call was unreported or had no configured model rate. This prevents an unavailable provider report from looking free. Usage writes run off the chat path in a bounded in-memory queue: a clean shutdown drains it, while process loss can lose at most 1,024 accepted observations; sustained database overload drops new observations rather than slowing a user turn.

### LLM Calls

Every call to an LLM provider is captured as a `gen_ai.chat` span:

- Model name (requested and actual)
- Provider (anthropic, openai, etc.)
- Token usage: input, output, cache read, cache write
- Time to first token (TTFT)
- Total duration
- Stop reason (end_turn, tool_use, max_tokens, etc.)
- Provider attempt count and retry count
- Errors

Provider SDKs retry inside a single call, so each network attempt gets its own
child `gen_ai.chat.request` span carrying the attempt number, the response
status code, and the server host. Its duration is the request itself (connect,
send, first byte), not the streamed response — that is the parent's duration.

Every request through the shared HTTP client gets exactly one span, ending at the response headers. Requests that are not model calls get the same span under a generic `HTTP <METHOD>` name; the model-call context adds the `gen_ai` attributes and the attempt number, nothing more. Splitting it that way is deliberate: a caller that forgets to mark its request loses a span's meaning, never a secret.

### Tool Executions

Each tool call is captured as a `gen_ai.execute_tool` span:

- Tool name (bash, view_image, webfetch, agent, etc.)
- Call ID
- Duration
- Success or failure, with the error kind: `tool_error` (the tool broke) or
  `command_nonzero` (the command ran and exited nonzero)
- Command exit code, when there was one

A `command_nonzero` result does not set the span status to error. The tool
worked; the command said no. Marking it as a failure would report normal
exploration — a `grep` that matched nothing — as breakage.

### Memory Operations

Memory operations (append, assemble, compact, search, etc.) are captured as `memory.*` spans:

- Operation type
- Duration
- Token and message counts
- Token delta (for compaction)
- Errors

### Sandbox Lifecycle

Sandbox startup is captured with `sandbox.*` spans so sandbox failures can be traced past the final broken-pipe symptom:

- Session creation in the runner
- Backend startup and overlay/session directory setup
- Sandbox client process startup
- JSON-RPC handshake
- Session close or liveness loss reason

These spans include Stella-specific attributes such as sandbox backend, source/destination roots, working directory, network mode, read-only bind count, and captured error type.

### HTTP Requests

Inbound requests to the Web UI and API are captured as `http.server` spans, so you can trace user-facing latency end to end.

Outbound requests made through the shared HTTP client — every model provider, plus embeddings, skill fetches, and the channels that use it — are captured as `HTTP <METHOD>` client spans carrying the method, the destination host, and the response status. They end at the response headers, and they propagate W3C trace context and baggage so a downstream service continues the same trace.

Some integrations still call out through their own HTTP clients (several channel SDKs, the OIDC provider, the MCP client), so they appear in a trace only through whatever span encloses them.

### Trace Structure

Spans are organized into a hierarchy per chat session:

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

A new **turn** starts each time stella calls the LLM. The **chat** root span covers the entire conversation and closes after 2 minutes of inactivity.

## Span Attributes Reference

LLM and tool spans follow [OpenTelemetry GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-spans/):

| Attribute                                  | Spans        | Description                      |
| ------------------------------------------ | ------------ | -------------------------------- |
| `gen_ai.operation.name`                    | all          | `chat` or `execute_tool`         |
| `gen_ai.request.attempt`                   | chat.request | Attempt number, from 1           |
| `gen_ai.request.attempts`                  | chat         | Provider HTTP attempts           |
| `gen_ai.request.retry_count`               | chat         | Attempts minus one               |
| `gen_ai.provider.name`                     | chat         | Provider identifier              |
| `gen_ai.request.model`                     | chat         | Requested model                  |
| `gen_ai.response.model`                    | chat         | Actual model used                |
| `gen_ai.response.finish_reasons`           | chat         | Why generation stopped           |
| `gen_ai.conversation.id`                   | all          | Session ID                       |
| `gen_ai.usage.input_tokens`                | chat         | Input tokens                     |
| `gen_ai.usage.output_tokens`               | chat         | Output tokens                    |
| `gen_ai.usage.cache_read.input_tokens`     | chat         | Cached input tokens              |
| `gen_ai.usage.cache_creation.input_tokens` | chat         | Tokens written to cache          |
| `gen_ai.server.time_to_first_token`        | chat         | TTFT in seconds                  |
| `gen_ai.tool.name`                         | execute_tool | Tool name                        |
| `gen_ai.tool.call.id`                      | execute_tool | Tool call ID                     |
| `gen_ai.tool.error_kind`                   | execute_tool | `tool_error` / `command_nonzero` |
| `gen_ai.tool.exit_code`                    | execute_tool | Command exit status              |
| `error.type`                               | all          | Error type on failure            |

Memory spans use stella-specific attributes:

| Attribute                     | Description                                                                      |
| ----------------------------- | -------------------------------------------------------------------------------- |
| `stella.memory.op`            | Operation (bootstrap, append, assemble, compact, search, describe, expand, etc.) |
| `stella.memory.session_id`    | Memory session ID                                                                |
| `stella.memory.token_count`   | Token count                                                                      |
| `stella.memory.token_delta`   | Tokens saved by compaction (negative = reduction)                                |
| `stella.memory.message_count` | Message count                                                                    |

Sandbox lifecycle spans use these Stella-specific attributes:

| Attribute                           | Description                                      |
| ----------------------------------- | ------------------------------------------------ |
| `stella.sandbox.backend`            | Sandbox backend name (`local`, `docker`, `none`) |
| `stella.sandbox.agent_root`         | Agent workspace root from runner config          |
| `stella.sandbox.user_root`          | Requested sandbox user root                      |
| `stella.sandbox.resolved_user_root` | Resolved absolute user root used to build policy |
| `stella.sandbox.project_root`       | Project root when present                        |
| `stella.sandbox.work_dir`           | Requested or resolved working directory          |
| `stella.sandbox.src`                | Copy-on-write source root                        |
| `stella.sandbox.dst`                | Overlay/session destination root                 |
| `stella.sandbox.cwd`                | Remapped working directory inside the session    |
| `stella.sandbox.network.mode`       | Effective network mode                           |
| `stella.sandbox.network.allowlist`  | Network allowlist when configured                |
| `stella.sandbox.readonly_dir_count` | Number of read-only bind directories             |
| `stella.sandbox.close_reason`       | Why the session span ended                       |
| `stella.sandbox.server.name`        | Handshake-reported sandbox server name           |
| `stella.sandbox.server.version`     | Handshake-reported sandbox server version        |
| `stella.sandbox.protocol_version`   | RPC protocol version returned by the sandbox     |

## Turning It Off

There is no plugin to disable. Log mode follows `LOG_LEVEL`; set it to `WARN` or `ERROR` to quiet the per-call INFO lines. OTel export is off unless `OTEL_EXPORTER_OTLP_ENDPOINT` or a signal-specific exporter is set, so leaving those variables empty disables distributed telemetry entirely. Set `OTEL_TRACES_EXPORTER=none`, `OTEL_LOGS_EXPORTER=none`, or `OTEL_METRICS_EXPORTER=none` to disable individual signals.
