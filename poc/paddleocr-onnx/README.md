# PaddleOCR + ONNX Runtime Go POC

Proof of concept for a Go-native OCR engine: PP-OCRv5 mobile det/rec models run
through ONNX Runtime via [`onnxruntime_go`](https://github.com/yalue/onnxruntime_go),
with all preprocessing and postprocessing implemented in pure Go.

**Status: end-to-end image → text works** (darwin/arm64).

## What it proves

The whole risky stack is validated:

- ONNX Runtime loads and runs from a bundled shared library (no Python).
- PP-OCRv5 det + rec ONNX models run with dynamic input shapes.
- Charset alignment is correct (CTC blank at 0, 18383 dict chars, space at end).
- BGR channel order + normalization match training, so output is accurate.
- Pure-Go DB postprocess (connected components + score + unclip) finds text boxes.
- Pure-Go greedy CTC decode produces correct Chinese / Latin / digit text.

## Run

Prereqs are downloaded into `runtime/` and `models/` (gitignored, see below):

```bash
go build -o poc .

# recognize a single pre-cropped text line
./poc rec testdata/line.png

# detect + recognize a full image
./poc ocr testdata/page.png
```

Example output (~110ms cold, CPU, includes model load):

```
detected 4 text regions
[ 0] (  18,  18)-( 293,  60) det=0.85 rec=1.00  第一行：你好世界
[ 1] (  17,  74)-( 410, 118) det=0.79 rec=0.90  第二行：Stella OCR 引擎
...
```

## Assets (not committed)

| Path                                    | Source                                                                                                                             |
| --------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------- |
| `runtime/onnxruntime-osx-arm64-1.27.0/` | [ONNX Runtime v1.27.0 release](https://github.com/microsoft/onnxruntime/releases/tag/v1.27.0) (`onnxruntime-osx-arm64-1.27.0.tgz`) |
| `models/det.onnx`                       | HF `PaddlePaddle/PP-OCRv5_mobile_det_onnx` → `inference.onnx`                                                                      |
| `models/rec.onnx`                       | HF `PaddlePaddle/PP-OCRv5_mobile_rec_onnx` → `inference.onnx`                                                                      |
| `models/rec_keys.txt`                   | extracted from rec `inference.yml` `PostProcess.character_dict`                                                                    |

Model hyperparameters (rec shape `3x48x320`, det `resize_long 960`, DB
`thresh 0.3 / box_thresh 0.6 / unclip 1.5`) were read from each model's
`inference.yml` — they are **not** hardcoded guesses.

## Files

| File           | Role                                                               |
| -------------- | ------------------------------------------------------------------ |
| `ort.go`       | ORT init + single-input/output dynamic session helper              |
| `charset.go`   | CTC charset table builder                                          |
| `rec.go`       | rec preprocess (BGR, [-1,1], pad to 320) + greedy CTC decode       |
| `det.go`       | det preprocess + pure-Go DB postprocess + crop + full OCR pipeline |
| `imageutil.go` | RGBA conversion + bilinear resize                                  |
| `main.go`      | CLI (`rec` / `ocr`)                                                |

## Known POC simplifications (gaps vs production)

These are deliberate shortcuts, not bugs — each maps to a follow-up:

1. **Axis-aligned boxes only.** DB postprocess uses bounding rects of connected
   components, not contours + `minAreaRect`. Rotated/skewed text is cropped as
   its bounding box. Production needs rotated crops (perspective transform).
2. **No angle classifier.** 180°-flipped lines won't be corrected.
3. **darwin/arm64 only.** `resolveORTLib` needs the other platform libs wired up.
4. **Fixed rec width 320, single-image rec.** No batching of crops — each box is
   a separate `Run`. Batching crops by similar aspect ratio is the main speedup.
5. **No PDF.** OCR consumes images only; PDF rasterization is a separate adapter.
6. **No model manifest.** Params are constants here; production should load them
   from a per-model-bundle manifest (see design doc §11).

## Next steps toward the real `internal/ocr`

1. Replace bounding-rect postprocess with contour + `minAreaRect` + polygon
   unclip (pure Go, or OpenCV CGO for the first cut).
2. Add rotated-crop (perspective warp) before rec.
3. Batch rec crops.
4. Wire `runtime/` libs for linux/amd64, linux/arm64, windows/amd64; gate the
   whole engine behind a build tag (`ocr_onnx`) so lite builds skip CGO.
5. Define the `ocr.Engine` interface + `OCRRouter` (text-layer → remote → local).
