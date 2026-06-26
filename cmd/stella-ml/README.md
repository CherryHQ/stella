# stella-ml — native ML sidecar

A separate **CGO** binary that hosts onnxruntime + the HF tokenizer + the
embedding/OCR models, and serves them to the pure-Go `stellad` over an
HTTP-on-unix-socket contract (`protocol.go`). `stellad` stays `CGO_ENABLED=0`; this
is the only CGO artifact. See the design plan (`native-ml-sidecar`).

It is a **separate Go module** on purpose: the CGO dependency graph
(`onnxruntime_go`, `daulet/tokenizers`) never leaks into the main module, and the
parent's `go build ./...` skips this nested module automatically.

## Endpoints (protocol v1)

| Method + path      | Body in                                      | Body out                                                             |
| ------------------ | -------------------------------------------- | -------------------------------------------------------------------- |
| `GET /healthz`     | —                                            | JSON: runtime/protocol version, model-manifest digest, loaded models |
| `POST /v1/embed`   | JSON `{texts[], mode}`                       | `application/octet-stream`: little-endian f32, `count*dim`           |
| `POST /v1/extract` | image bytes (octet-stream) + `X-Stella-Mime` | JSON `{content, mime_type}` — PP-OCRv5 OCR                           |

Every request carries `X-Stella-Tenant`, `X-Stella-Request-Id`, and an optional
`X-Stella-Deadline-Unix-Ms`. Per-endpoint lanes + a per-tenant in-flight cap keep a
shared sidecar fair; request caps bound batch size and body bytes.

The embedding and OCR engines are independently optional: pass `-embed-model`
without the `-ocr-*` flags for an embedding-only sidecar (extract then reports
503), or vice versa. `/v1/extract` is image-only (JPEG/PNG/GIF/WebP/BMP/TIFF);
detection uses axis-aligned bounding rects (rotated-crop is a follow-up).

## Build (local dev, darwin/arm64)

The native libs are not vendored here; for local dev they come from the POCs. From
the repo root:

```sh
cd cmd/stella-ml
CGO_ENABLED=1 \
  CGO_LDFLAGS="-L$(cd ../../poc/e5-embedding-onnx/lib && pwd)" \
  go build -o /tmp/stella-ml .
```

Release builds resolve `libtokenizers.a` + `libonnxruntime` from the
`stella-ml-runtime` bundle (Phase 2). Linux is built natively in CI (no zig shim);
`poc/zig-crosscompile-spike` is the dev-only "one mac builds linux" path.

## Run

```sh
/tmp/stella-ml \
  -socket /tmp/sml/ml.sock \
  -runtime-lib ../../poc/e5-embedding-onnx/runtime/onnxruntime-osx-arm64-1.27.0/lib \
  -embed-model ../../poc/e5-embedding-onnx/model/model_int8.onnx \
  -tokenizer  ../../poc/e5-embedding-onnx/model/tokenizer.json \
  -ocr-det-model ../../poc/paddleocr-onnx/models/det.onnx \
  -ocr-rec-model ../../poc/paddleocr-onnx/models/rec.onnx \
  -ocr-keys      ../../poc/paddleocr-onnx/models/rec_keys.txt
```

Then `curl --unix-socket /tmp/sml/ml.sock http://x/healthz`, or OCR an image:

```sh
curl --unix-socket /tmp/sml/ml.sock -X POST http://x/v1/extract \
  -H 'X-Stella-Mime: image/png' --data-binary @page.png
```

Keep the socket path short — macOS caps unix socket paths at ~104 bytes.
