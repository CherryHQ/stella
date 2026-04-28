---
name: kreuzberg
description: >-
  Extract text, tables, metadata, and images from 91+ document formats
  (PDF, Office, images, HTML, email, archives, academic) using Kreuzberg CLI.
license: Elastic-2.0
metadata:
  author: kreuzberg-dev
  version: "1.0"
  repository: https://github.com/kreuzberg-dev/kreuzberg
---

# Kreuzberg Document Extraction

Kreuzberg extracts text, tables, metadata, and images from 91+ file formats. Use it for document processing, OCR, batch extraction, structured LLM extraction, and embeddings.

Run `kreuzberg --help` for all commands, `kreuzberg <command> --help` for full flag reference.

## Core Usage

```bash
# Single file → stdout (text) or JSON
kreuzberg extract document.pdf
kreuzberg extract document.pdf --content-format markdown --format json

# Batch
kreuzberg batch *.pdf --content-format markdown

# Detect MIME type
kreuzberg detect unknown-file
```

## LLM Structured Extraction

Extracts typed JSON from a document using a schema + LLM model. API key falls back to `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, etc.

```bash
kreuzberg extract-structured invoice.pdf \
  --schema schema.json \
  --model openai/gpt-4o \
  --strict
```

## Embeddings

Local ONNX (no API key) or provider-hosted. **Not available in Homebrew** — use Docker.

```bash
kreuzberg embed --text "hello world" --preset balanced
echo "some text" | kreuzberg embed --provider llm --model openai/text-embedding-3-small
```

## Chunking

```bash
kreuzberg chunk --text "..." --chunk-size 500 --chunk-overlap 50
kreuzberg chunk --chunker-type markdown --text "# Heading\n\nParagraph..."
cat file.txt | kreuzberg chunk --chunker-type semantic --topic-threshold 0.8
```

## Configuration

Only `kreuzberg.toml` is auto-discovered. YAML/JSON require `--config <path>`.

```bash
kreuzberg extract doc.pdf                          # finds kreuzberg.toml automatically
kreuzberg extract doc.pdf --config my.yaml         # explicit for non-TOML
kreuzberg extract doc.pdf --config-json '{"ocr":{"language":"deu"}}'
```

Config file skeleton (field names are snake_case — `max_chars` not `max_characters`):

```toml
use_cache = true
enable_quality_processing = true
output_format = "markdown"       # content format for file output

[ocr]
backend = "tesseract"            # tesseract | paddle-ocr | easyocr
language = "eng"                 # ISO 639-3 for tesseract; short codes for paddle/easyocr

[chunking]
max_chars = 1000                 # NOT max_characters
max_overlap = 200                # NOT overlap

[pdf_options]
extract_images = true

[server]                         # for `kreuzberg serve`
host = "127.0.0.1"
port = 8000
```

## Extracting Images from PDFs

Images are **not written to disk** — they come back as byte arrays in JSON output. Two-step process required:

```bash
# Step 1: capture JSON
kreuzberg extract doc.pdf --pdf-extract-images true --format json > out.json

# Step 2: save images
python3 -c "
import json, pathlib
d = json.load(open('out.json'))
pathlib.Path('images').mkdir(exist_ok=True)
for img in d.get('images', []):
    pathlib.Path(f'images/image_{img[\"image_index\"]}.{img[\"format\"]}').write_bytes(bytes(img['data']))
"
```

## Key Flags (Non-Obvious)

| Flag | Note |
|------|------|
| `--format` | Wire format for CLI output: `text` (default for extract), `json`, `toon` (token-efficient JSON) |
| `--content-format` | Format of extracted text: `plain`, `markdown`, `djot`, `html`. `--output-format` is a deprecated alias. |
| `--token-reduction` | `off/light/moderate/aggressive/maximum` — reduce tokens before LLM consumption |
| `--acceleration` | ONNX provider: `auto`, `cpu`, `coreml` (macOS), `cuda`, `tensorrt` |
| `--pdf-extract-images` | Embeds image bytes in JSON result (see above) |

## Common Pitfalls

1. `--format` ≠ `--content-format`: one controls the serialization envelope, the other the text inside it.
2. Config auto-discovery only finds `kreuzberg.toml` — not YAML or JSON.
3. PDF images land in `result.images[]` as byte arrays; nothing is written to disk automatically.
4. `embed` is excluded from the Homebrew build; use Docker or `cargo install`.
5. For large docs with `extract-structured`, use `--token-reduction` to stay within LLM context limits.

## References

- Supported formats: grep `references/supported-formats.md` instead of reading it whole
  e.g. `grep '.mdoc' references/supported-formats.md`
