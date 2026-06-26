# multilingual-e5-small + ONNX Runtime Go POC

Proof of concept for a Go-native text embedding engine: `intfloat/multilingual-e5-small`
run through ONNX Runtime via [`onnxruntime_go`](https://github.com/yalue/onnxruntime_go),
with XLM-RoBERTa tokenization via [`daulet/tokenizers`](https://github.com/daulet/tokenizers)
(the real Hugging Face Rust tokenizer, statically linked).

**Status: verified correct against a Python reference** (darwin/arm64).

## What it proves

- ONNX Runtime runs the e5 encoder with no Python at inference time.
- The Go output is **bit-for-bit equivalent** to the reference (same ONNX model +
  HF tokenizer in Python): `cosine = 0.99999963`, max element diff `5e-7`.
- The hard part — exact XLM-RoBERTa tokenization (SentencePiece unigram +
  `precompiled_charsmap` normalizer) — is handled correctly by loading
  `tokenizer.json` through the official HF Rust tokenizer.
- Cross-lingual semantics work: EN/ZH/JA "reset password" sentences cluster at
  cosine 0.86–0.90, clearly above unrelated pairs (0.68–0.74).
- int8 quantization keeps accuracy: `cosine_vs_fp32 = 0.99897`.

## Run

```bash
export CGO_LDFLAGS="-L$(pwd)/lib"   # static libtokenizers.a
go build -o e5 .

./e5 demo                              # cross-lingual cosine matrix
./e5 vec query "How do I reset my password?"   # raw 384-d vector
E5_MODEL=model_int8.onnx ./e5 demo     # use the quantized model
```

e5 requires an instruction prefix: use `query` for search queries and `passage`
for documents (`Embed("query"|"passage", text)` prepends `"<prefix>: "`).
Embeddings are mean-pooled over tokens (attention-masked) and L2-normalized, so
similarity is a plain dot product.

## Model size — the one thing to weigh

e5-small has a 250k-token vocabulary, so the embedding table dominates; the model
is large despite being "small" by layer count (12 layers, hidden 384).

| Variant | Size | Accuracy vs fp32 | Note |
| --- | --- | --- | --- |
| `model.onnx` (fp32) | 448M | 1.00000 | reference; too big to ship |
| `model_int8.onnx` | **113M** | **0.99897** | **recommended for built-in** |
| `model_fp16.onnx` | 224M | — | Xenova export fails to load on ORT 1.27 (graph fusion) |

The statically-linked `libtokenizers.a` adds ~19M to the Go binary (it links in,
no runtime sidecar, unlike the onnxruntime dylib which is `dlopen`-ed).

## Assets (not committed)

| Path | Source |
| --- | --- |
| `model/model.onnx`, `model/tokenizer.json`, `config.json` | HF `intfloat/multilingual-e5-small` (`onnx/` dir) |
| `model/model_int8.onnx` | HF `Xenova/multilingual-e5-small` → `onnx/model_quantized.onnx` |
| `lib/libtokenizers.a` | [daulet/tokenizers v1.27.0 release](https://github.com/daulet/tokenizers/releases/tag/v1.27.0), `libtokenizers.darwin-arm64.tar.gz` |
| `runtime/` | symlink to the OCR POC's onnxruntime 1.27.0 (shared) |

## Files

| File | Role |
| --- | --- |
| `e5.go` | tokenize → encoder → mean pool → L2 normalize |
| `ort.go` | ORT init (shared onnxruntime dylib) |
| `main.go` | CLI (`demo` / `vec`), `E5_MODEL` env to switch model file |

## Known POC simplifications

1. **Single-text inference.** No batching; production should pad a batch and run
   once. Embeddings are cheap (sub-10ms/short sentence on CPU) so this matters
   only at ingestion scale.
2. **No length cap.** Should truncate to 512 tokens (e5's max) before encode.
3. **darwin/arm64 only.** Needs onnxruntime + libtokenizers for other platforms.
4. **fp32 model checked in tests; ship int8.**

## Next steps toward built-in embeddings

1. Define `embed.Embedder` interface (`Embed(ctx, prefix, texts) ([][]float32, error)`)
   alongside the existing remote/cloud embedding path (local as offline fallback).
2. Batch encode; truncate to 512 tokens.
3. Ship `model_int8.onnx` (113M) + `tokenizer.json` (16M) as sidecar assets,
   gated behind the same `ocr_onnx`-style build tag (call it `embed_onnx`).
4. Wire onnxruntime + libtokenizers for linux/amd64, linux/arm64, windows/amd64.
