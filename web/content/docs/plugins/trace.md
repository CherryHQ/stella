---
title: Trace
---

## Overview

The **trace** plugin gives you visibility into what stella is doing under the hood. Every LLM call, tool execution, and memory operation is tracked with timing, token usage, and error details.

It operates in two modes:

- **Log mode** (always on) -- structured log lines via Go's `slog`, visible in stderr. Zero configuration needed.
- **OpenTelemetry mode** (opt-in) -- exports distributed traces via standard OTLP environment variables. Both OTLP/gRPC and OTLP/HTTP are supported, including authenticated backends. Activate by setting an OTLP endpoint.

## Configuration

### Log Mode

Log mode is always active when the plugin is enabled. Control verbosity with `LOG_LEVEL`:

| Level            | What You See                                                                                            |
| ---------------- | ------------------------------------------------------------------------------------------------------- |
| `INFO` (default) | Every LLM call (model, tokens, duration, TTFT), tool call (name, duration, error), and memory operation |
| `DEBUG`          | Same as INFO plus internal engine events                                                                |
| `TRACE`          | Same as DEBUG plus full memory operation details (message content, search results, profile text)        |

```bash
# Default -- LLM/tool/memory events at INFO
stella serve

# Verbose -- includes memory detail fields
LOG_LEVEL=TRACE stella serve
```

Example log output:

```
level=INFO msg=post_llm_call hook=trace provider=anthropic model=claude-sonnet-4-20250514 stop_reason=tool_use duration=3.2s ttft=450ms input_tokens=12500 output_tokens=350 cache_read=8000
level=INFO msg=post_tool_call hook=trace tool=bash call_id=call_01 is_error=false duration=1.5s result_len=256
level=INFO msg=post_memory_call hook=trace op=compact duration=200ms token_count=8000 token_delta=-4500
```

### OpenTelemetry Mode

Set `OTEL_EXPORTER_OTLP_ENDPOINT` to enable. Stella delegates exporter configuration to the OpenTelemetry SDK, so standard OTel environment variables are supported:

| Environment Variable                | Default                    | Description                                                                                                                                                                               |
| ----------------------------------- | -------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `OTEL_EXPORTER_OTLP_ENDPOINT`       | _(empty -- OTel disabled)_ | OTLP base endpoint. For OTLP/HTTP, use a full URL such as `https://collector.example.com/api/default`. For OTLP/gRPC, use a URL with scheme such as `https://collector.example.com:4317`. |
| `OTEL_EXPORTER_OTLP_PROTOCOL`       | SDK default                | Export protocol. Common values: `grpc` or `http/protobuf`.                                                                                                                                |
| `OTEL_EXPORTER_OTLP_HEADERS`        | _(empty)_                  | Comma-separated headers applied to all OTLP signals, for example `authorization=Bearer <token>`.                                                                                          |
| `OTEL_EXPORTER_OTLP_TRACES_HEADERS` | _(empty)_                  | Comma-separated headers applied to traces only. Overrides generic OTLP headers for traces.                                                                                                |
| `OTEL_SERVICE_NAME`                 | `stella`                   | Service name shown in your trace backend.                                                                                                                                                 |
| `OTEL_EXPORTER_OTLP_INSECURE`       | SDK default                | Set to `false` to require TLS. Use `false` for HTTPS or secure gRPC endpoints.                                                                                                            |

When OTel is enabled, both modes run simultaneously -- you get log lines and exported traces.

### Common Pitfalls

- **Always include a scheme in `OTEL_EXPORTER_OTLP_ENDPOINT`.** Use `https://collector.example.com:4317`, not `collector.example.com:4317`. Omitting the scheme can produce malformed export URLs such as `http:///v1/traces`.
- **Use the correct protocol for your backend.** OTLP/HTTP usually needs `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf`; OTLP/gRPC usually uses `OTEL_EXPORTER_OTLP_PROTOCOL=grpc`.
- **For OTLP/HTTP, set the base path, not `/v1/traces`.** The exporter appends `/v1/traces` automatically.
- **Do not set `OTEL_EXPORTER_OTLP_INSECURE=true` for TLS endpoints.** Secure collectors should use `OTEL_EXPORTER_OTLP_INSECURE=false`.
- **Header values are comma-separated `key=value` pairs without shell quotes inside the value.** Example: `authorization=Basic abc123,organization=default`.

## Using with Jaeger

[Jaeger](https://www.jaegertracing.io/) is the simplest way to visualize stella's traces. It accepts OTLP natively and runs as a single container.

```bash
# Start Jaeger
docker run -d --name jaeger \
  -p 16686:16686 \
  -p 4317:4317 \
  jaegertracing/jaeger:latest

# Start stella with tracing over OTLP/gRPC
OTEL_EXPORTER_OTLP_PROTOCOL=grpc \
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317 \
stella serve
```

Open `http://localhost:16686`, select the **stella** service, and click **Find Traces**. Each chat session appears as a trace with a waterfall view of LLM calls, tool executions, and memory operations.

### Using with Other Backends

Any OTLP-compatible backend works. Examples:

```bash
# Grafana Tempo over OTLP/gRPC
OTEL_EXPORTER_OTLP_PROTOCOL=grpc \
OTEL_EXPORTER_OTLP_ENDPOINT=http://tempo.internal:4317 \
stella serve

# SigNoz over OTLP/gRPC
OTEL_EXPORTER_OTLP_PROTOCOL=grpc \
OTEL_EXPORTER_OTLP_ENDPOINT=http://signoz.internal:4317 \
stella serve

# OTel Collector over OTLP/gRPC
OTEL_EXPORTER_OTLP_PROTOCOL=grpc \
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4317 \
stella serve

# Cloud OTLP/gRPC with TLS and auth header
OTEL_EXPORTER_OTLP_PROTOCOL=grpc \
OTEL_EXPORTER_OTLP_ENDPOINT=https://otlp.vendor.com:443 \
OTEL_EXPORTER_OTLP_TRACES_HEADERS="authorization=Bearer <token>" \
OTEL_EXPORTER_OTLP_INSECURE=false \
stella serve

# Cloud OTLP/HTTP with TLS and auth header
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf \
OTEL_EXPORTER_OTLP_ENDPOINT=https://otlp.vendor.com/api/default \
OTEL_EXPORTER_OTLP_TRACES_HEADERS="authorization=Basic <base64>,organization=default,stream-name=default" \
OTEL_EXPORTER_OTLP_INSECURE=false \
stella serve
```

If your provider gives you an OTLP/HTTP endpoint such as `https://collector.example.com/api/default`, use that exact base URL and let the exporter append `/v1/traces`.

## What Gets Traced

### LLM Calls

Every call to an LLM provider is captured as a `gen_ai.chat` span:

- Model name (requested and actual)
- Provider (anthropic, openai, etc.)
- Token usage: input, output, cache read, cache write
- Time to first token (TTFT)
- Total duration
- Stop reason (end_turn, tool_use, max_tokens, etc.)
- Errors

### Tool Executions

Each tool call is captured as a `gen_ai.execute_tool` span:

- Tool name (bash, read, write, edit, webfetch, agent, etc.)
- Call ID
- Duration
- Success or failure

### Memory Operations

Memory operations (append, assemble, compact, search, etc.) are captured as `memory.*` spans:

- Operation type
- Duration
- Token and message counts
- Token delta (for compaction)
- Errors

### Sandbox Lifecycle

Sandbox startup is captured with `sandbox.*` spans so boxsh failures can be traced past the final broken-pipe symptom:

- Session creation in the runner
- Backend startup and overlay/session directory setup
- Boxsh client process startup
- JSON-RPC handshake
- Session close or liveness loss reason

These spans include Stella-specific attributes such as sandbox backend, source/destination roots, working directory, network mode, read-only bind count, and captured error type.

### Trace Structure

Spans are organized into a hierarchy per chat session:

```
chat
  └── turn 1
       ├── gen_ai.chat                 3.2s
       ├── gen_ai.execute_tool (bash)  1.5s
       ├── gen_ai.execute_tool (read)  0.1s
       └── memory.append               0.02s
  └── turn 2
       ├── gen_ai.chat                 2.8s
       └── memory.compact              0.2s
```

A new **turn** starts each time stella calls the LLM. The **chat** root span covers the entire conversation and closes after 2 minutes of inactivity.

## Span Attributes Reference

LLM and tool spans follow [OpenTelemetry GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-spans/):

| Attribute                                  | Spans        | Description              |
| ------------------------------------------ | ------------ | ------------------------ |
| `gen_ai.operation.name`                    | all          | `chat` or `execute_tool` |
| `gen_ai.provider.name`                     | chat         | Provider identifier      |
| `gen_ai.request.model`                     | chat         | Requested model          |
| `gen_ai.response.model`                    | chat         | Actual model used        |
| `gen_ai.response.finish_reasons`           | chat         | Why generation stopped   |
| `gen_ai.conversation.id`                   | all          | Session ID               |
| `gen_ai.usage.input_tokens`                | chat         | Input tokens             |
| `gen_ai.usage.output_tokens`               | chat         | Output tokens            |
| `gen_ai.usage.cache_read.input_tokens`     | chat         | Cached input tokens      |
| `gen_ai.usage.cache_creation.input_tokens` | chat         | Tokens written to cache  |
| `gen_ai.server.time_to_first_token`        | chat         | TTFT in seconds          |
| `gen_ai.tool.name`                         | execute_tool | Tool name                |
| `gen_ai.tool.call.id`                      | execute_tool | Tool call ID             |
| `error.type`                               | all          | Error type on failure    |

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
| `stella.sandbox.backend`            | Sandbox backend name, currently `boxsh`          |
| `stella.sandbox.agent_root`         | Agent workspace root from runner config          |
| `stella.sandbox.user_root`          | Requested sandbox user root                      |
| `stella.sandbox.resolved_user_root` | Resolved absolute user root used to build policy |
| `stella.sandbox.project_root`       | Project root when present                        |
| `stella.sandbox.work_dir`           | Requested or resolved working directory          |
| `stella.sandbox.src`                | Boxsh copy-on-write source root                  |
| `stella.sandbox.dst`                | Overlay/session destination root                 |
| `stella.sandbox.cwd`                | Remapped working directory inside the session    |
| `stella.sandbox.network.mode`       | Effective network mode                           |
| `stella.sandbox.network.allowlist`  | Network allowlist when configured                |
| `stella.sandbox.readonly_dir_count` | Number of read-only bind directories             |
| `stella.sandbox.close_reason`       | Why the session span ended                       |
| `stella.sandbox.server.name`        | Handshake-reported sandbox server name           |
| `stella.sandbox.server.version`     | Handshake-reported sandbox server version        |
| `stella.sandbox.protocol_version`   | RPC protocol version returned by boxsh           |

## Managing the Plugin

The trace plugin is enabled by default.

```bash
stella plugin list                   # Check status
stella plugin disable hook/trace     # Disable all tracing
stella plugin enable hook/trace      # Re-enable
```

Disabling the trace plugin turns off both log mode and OTel mode. LLM calls, tool executions, and memory operations will no longer be logged or exported.
