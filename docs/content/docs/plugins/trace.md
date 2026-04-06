---
title: Trace
---

## Overview

The **trace** plugin gives you visibility into what anna is doing under the hood. Every LLM call, tool execution, and memory operation is tracked with timing, token usage, and error details.

It operates in two modes:

- **Log mode** (always on) -- structured log lines via Go's `slog`, visible in stderr. Zero configuration needed.
- **OpenTelemetry mode** (opt-in) -- exports distributed traces via OTLP gRPC to any compatible backend (Jaeger, Grafana Tempo, SigNoz, Datadog, etc.). Activate by setting one environment variable.

## Configuration

### Log Mode

Log mode is always active when the plugin is enabled. Control verbosity with `LOG_LEVEL`:

| Level | What You See |
| --- | --- |
| `INFO` (default) | Every LLM call (model, tokens, duration, TTFT), tool call (name, duration, error), and memory operation |
| `DEBUG` | Same as INFO plus internal engine events |
| `TRACE` | Same as DEBUG plus full memory operation details (message content, search results, profile text) |

```bash
# Default -- LLM/tool/memory events at INFO
anna serve

# Verbose -- includes memory detail fields
LOG_LEVEL=TRACE anna serve
```

Example log output:

```
level=INFO msg=post_llm_call hook=trace provider=anthropic model=claude-sonnet-4-20250514 stop_reason=tool_use duration=3.2s ttft=450ms input_tokens=12500 output_tokens=350 cache_read=8000
level=INFO msg=post_tool_call hook=trace tool=bash call_id=call_01 is_error=false duration=1.5s result_len=256
level=INFO msg=post_memory_call hook=trace op=compact duration=200ms token_count=8000 token_delta=-4500
```

### OpenTelemetry Mode

Set `OTEL_EXPORTER_OTLP_ENDPOINT` to enable. All standard OTel environment variables are supported:

| Environment Variable | Default | Description |
| --- | --- | --- |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | *(empty -- OTel disabled)* | OTLP gRPC endpoint. Set this to enable OTel. |
| `OTEL_SERVICE_NAME` | `anna` | Service name shown in your trace backend |
| `OTEL_EXPORTER_OTLP_INSECURE` | `true` | Set to `false` to require TLS |

When OTel is enabled, both modes run simultaneously -- you get log lines and exported traces.

## Using with Jaeger

[Jaeger](https://www.jaegertracing.io/) is the simplest way to visualize anna's traces. It accepts OTLP natively and runs as a single container.

```bash
# Start Jaeger
docker run -d --name jaeger \
  -p 16686:16686 \
  -p 4317:4317 \
  jaegertracing/jaeger:latest

# Start anna with tracing
OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 anna serve
```

Open `http://localhost:16686`, select the **anna** service, and click **Find Traces**. Each chat session appears as a trace with a waterfall view of LLM calls, tool executions, and memory operations.

### Using with Other Backends

Any OTLP-compatible backend works. Examples:

```bash
# Grafana Tempo
OTEL_EXPORTER_OTLP_ENDPOINT=tempo.internal:4317 anna serve

# SigNoz
OTEL_EXPORTER_OTLP_ENDPOINT=signoz.internal:4317 anna serve

# OTel Collector (routes to multiple backends)
OTEL_EXPORTER_OTLP_ENDPOINT=otel-collector:4317 anna serve

# Cloud (with TLS)
OTEL_EXPORTER_OTLP_ENDPOINT=otlp.vendor.com:443 \
OTEL_EXPORTER_OTLP_INSECURE=false \
anna serve
```

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

A new **turn** starts each time anna calls the LLM. The **chat** root span covers the entire conversation and closes after 2 minutes of inactivity.

## Span Attributes Reference

LLM and tool spans follow [OpenTelemetry GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-spans/):

| Attribute | Spans | Description |
| --- | --- | --- |
| `gen_ai.operation.name` | all | `chat` or `execute_tool` |
| `gen_ai.provider.name` | chat | Provider identifier |
| `gen_ai.request.model` | chat | Requested model |
| `gen_ai.response.model` | chat | Actual model used |
| `gen_ai.response.finish_reasons` | chat | Why generation stopped |
| `gen_ai.conversation.id` | all | Session ID |
| `gen_ai.usage.input_tokens` | chat | Input tokens |
| `gen_ai.usage.output_tokens` | chat | Output tokens |
| `gen_ai.usage.cache_read.input_tokens` | chat | Cached input tokens |
| `gen_ai.usage.cache_creation.input_tokens` | chat | Tokens written to cache |
| `gen_ai.server.time_to_first_token` | chat | TTFT in seconds |
| `gen_ai.tool.name` | execute_tool | Tool name |
| `gen_ai.tool.call.id` | execute_tool | Tool call ID |
| `error.type` | all | Error type on failure |

Memory spans use anna-specific attributes:

| Attribute | Description |
| --- | --- |
| `anna.memory.op` | Operation (bootstrap, append, assemble, compact, search, describe, expand, etc.) |
| `anna.memory.session_id` | Memory session ID |
| `anna.memory.token_count` | Token count |
| `anna.memory.token_delta` | Tokens saved by compaction (negative = reduction) |
| `anna.memory.message_count` | Message count |

## Managing the Plugin

The trace plugin is enabled by default.

```bash
anna plugin list                   # Check status
anna plugin disable hook/trace     # Disable all tracing
anna plugin enable hook/trace      # Re-enable
```

Disabling the trace plugin turns off both log mode and OTel mode. LLM calls, tool executions, and memory operations will no longer be logged or exported.
