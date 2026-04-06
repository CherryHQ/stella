---
title: Trace
---

The **trace** hook plugin provides observability for anna's engine loop. It logs all LLM calls, tool executions, and memory operations via structured logging (`slog`), and optionally exports OpenTelemetry traces via OTLP gRPC to any compatible backend (Jaeger, Grafana Tempo, SigNoz, etc.).

## How It Works

The trace hook implements all five hook points:

| Hook Point | What It Captures |
| --- | --- |
| PreLLMCall | Model, message count, tool definitions, system prompt length |
| PostLLMCall | Provider, model, stop reason, duration, TTFT, token usage (input/output/cache) |
| PreToolCall | Tool name, call ID, summarized arguments |
| PostToolCall | Tool name, call ID, error status, duration, result snippet |
| PostMemoryCall | Operation type, duration, token/message counts, detail (at TRACE level) |

## Structured Logging

Structured logging is always active. All events are emitted via Go's `log/slog` at `INFO` level. Set `LOG_LEVEL=TRACE` to include verbose memory detail fields.

```bash
# Standard logging
LOG_LEVEL=INFO anna serve

# Verbose memory traces
LOG_LEVEL=TRACE anna serve
```

## OpenTelemetry Tracing

OTel tracing activates when `OTEL_EXPORTER_OTLP_ENDPOINT` is set. Without it, the hook uses a no-op tracer and no spans are created.

### Quick Start with Jaeger

```bash
# 1. Start Jaeger with OTLP ingest
docker run -d --name jaeger \
  -p 16686:16686 \
  -p 4317:4317 \
  jaegertracing/jaeger:latest

# 2. Start anna with OTel enabled
OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 anna serve

# 3. Open Jaeger UI
open http://localhost:16686
```

### Configuration

OTel is configured via standard OpenTelemetry environment variables:

| Environment Variable | Default | Description |
| --- | --- | --- |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | *(empty -- OTel disabled)* | OTLP gRPC endpoint (e.g. `localhost:4317`) |
| `OTEL_SERVICE_NAME` | `anna` | Service name in traces |
| `OTEL_EXPORTER_OTLP_INSECURE` | `true` | Set `false` to require TLS |

Sampling rate is controlled by the OTel SDK's default behavior (all traces sampled). For production, configure a collector-side sampling strategy.

### Span Hierarchy

When OTel is active, the hook builds a structured trace tree per chat session:

```
chat {sessionID}
  └── turn 1
       ├── gen_ai.chat              (LLM call)
       ├── gen_ai.execute_tool      (tool: bash)
       ├── gen_ai.execute_tool      (tool: read)
       └── memory.append
  └── turn 2
       ├── gen_ai.chat
       └── memory.compact
```

- **chat** -- root span, created on first LLM call for a session, ended after 2 minutes of inactivity
- **turn N** -- one per LLM turn (rotated on each PreLLMCall)
- **gen_ai.chat** -- started in PreLLMCall, ended in PostLLMCall (real timing)
- **gen_ai.execute_tool** -- started in PreToolCall, ended in PostToolCall (real timing)
- **memory.\*** -- backdated spans (post-hook only, duration from memory layer)

### Span Attributes

LLM spans follow [OpenTelemetry GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-spans/):

| Attribute | Description |
| --- | --- |
| `gen_ai.operation.name` | `chat` or `execute_tool` |
| `gen_ai.provider.name` | Provider identifier (e.g. `anthropic`) |
| `gen_ai.request.model` | Requested model name |
| `gen_ai.response.model` | Model that generated the response |
| `gen_ai.response.finish_reasons` | Why generation stopped |
| `gen_ai.conversation.id` | Session ID |
| `gen_ai.usage.input_tokens` | Input tokens consumed |
| `gen_ai.usage.output_tokens` | Output tokens generated |
| `gen_ai.usage.cache_read.input_tokens` | Tokens served from provider cache |
| `gen_ai.usage.cache_creation.input_tokens` | Tokens written to provider cache |
| `gen_ai.server.time_to_first_token` | TTFT in seconds |
| `gen_ai.tool.name` | Tool name (execute_tool spans) |
| `gen_ai.tool.call.id` | Tool call ID (execute_tool spans) |
| `error.type` | Error type when operation fails |

Memory spans use `anna.memory.*` attributes:

| Attribute | Description |
| --- | --- |
| `anna.memory.op` | Operation name (bootstrap, append, assemble, compact, search, etc.) |
| `anna.memory.session_id` | Memory session ID |
| `anna.memory.token_count` | Token count (when applicable) |
| `anna.memory.token_delta` | Token delta from compaction |
| `anna.memory.message_count` | Message count (when applicable) |

## Plugin Management

The trace hook is enabled by default. Manage it via CLI or admin panel:

```bash
anna plugin list                   # Check status
anna plugin disable hook/trace     # Disable trace hook
anna plugin enable hook/trace      # Re-enable trace hook
```
