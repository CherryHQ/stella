// Module stella-ml is the native ML sidecar: a separate CGO binary hosting
// onnxruntime + HF tokenizers + models. It is intentionally a SEPARATE module so
// the CGO dependency graph never leaks into the main `github.com/CherryHQ/stella`
// module, which ships CGO_ENABLED=0. The parent module's `go build ./...` skips
// this nested module automatically.
module github.com/CherryHQ/stella/cmd/stella-ml

go 1.26.4

require (
	github.com/daulet/tokenizers v1.27.0
	github.com/yalue/onnxruntime_go v1.31.0
)

require golang.org/x/image v0.43.0
