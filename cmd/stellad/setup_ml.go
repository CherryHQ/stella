package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/CherryHQ/stella/internal/document"
	"github.com/CherryHQ/stella/internal/embedding"
	"github.com/CherryHQ/stella/internal/ml"
	"github.com/CherryHQ/stella/internal/mlruntime"
)

// setupMLSidecar resolves the native ML runtime and, when installed, builds (but
// does not start) the sidecar supervisor plus a LocalEmbedder adapter for the
// embedding lane. It returns (nil, nil) when no runtime is present so local ML
// features stay disabled with no error — the same graceful-degrade contract as an
// unconfigured lane. Start the returned supervisor on the app context after the
// background task group exists.
func setupMLSidecar(stellaHome string, logger *slog.Logger) (*ml.Supervisor, embedding.LocalEmbedder) {
	r, found, err := mlruntime.Resolve(stellaHome)
	if err != nil {
		logger.Warn("ml runtime resolve failed; local ML disabled", "err", err)
		return nil, nil
	}
	if !found {
		return nil, nil
	}

	// Socket lives under STELLA_HOME/run. Keep STELLA_HOME shallow on macOS: unix
	// socket paths are capped near 104 bytes and the sidecar errors clearly if the
	// resolved path is too long.
	sockPath := filepath.Join(stellaHome, "run", "stella-ml.sock")
	args := []string{
		"-runtime-lib", r.LibDir,
		"-embed-model", r.EmbedModelPath,
		"-tokenizer", r.TokenizerPath,
		"-runtime-version", r.RuntimeVersion,
	}
	ocrWired := false
	if r.HasOCR() && localOCREnabled() {
		args = append(args,
			"-ocr-det-model", r.OCRDetPath,
			"-ocr-rec-model", r.OCRRecPath,
			"-ocr-keys", r.OCRKeysPath,
		)
		ocrWired = true
	}
	sup := ml.NewSupervisor(ml.SupervisorConfig{
		BinPath:    r.BinPath,
		SocketPath: sockPath,
		Args:       args,
	}, logger)

	// Install the document-extraction OCR fallback as a process-wide capability so
	// every read-tool extractor picks it up. Gated by STELLA_LOCAL_OCR for the MVP;
	// the toggle moves to deployment config + the settings UI in a later phase.
	if ocrWired {
		document.SetLocalOCR(mlOCR{client: sup.Client(), tenant: "ocr"})
	}

	logger.Info("native ML sidecar resolved", "bin", r.BinPath, "runtime", r.RuntimeVersion, "model", r.ModelVersion, "ocr", ocrWired)
	return sup, mlLocalEmbedder{client: sup.Client(), tenant: "embedding"}
}

// localOCREnabled reports whether the operator opted into local OCR. Explicit
// opt-in keeps OCR off by default even when the models happen to be installed.
func localOCREnabled() bool {
	switch os.Getenv("STELLA_LOCAL_OCR") {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// mlOCR adapts the sidecar client to document.OCR, mapping the sidecar's extract
// result to a document.Result and pinning a fixed fairness tenant. // fixed tenant;
// per-account once the read tool carries account context.
type mlOCR struct {
	client *ml.Client
	tenant string
}

func (o mlOCR) Extract(ctx context.Context, mime string, data []byte, forceOCR bool) (*document.Result, error) {
	res, err := o.client.Extract(ctx, o.tenant, mime, data, forceOCR)
	if err != nil {
		return nil, err
	}
	return &document.Result{Content: res.Content, MimeType: res.MimeType}, nil
}

// mlLocalEmbedder adapts the sidecar client to embedding.LocalEmbedder, mapping
// the embedding query/document mode to the sidecar's query/passage prefix.
type mlLocalEmbedder struct {
	client *ml.Client
	tenant string
}

func (e mlLocalEmbedder) EmbedLocal(ctx context.Context, mode embedding.Mode, texts []string) ([][]float32, error) {
	m := ml.ModeQuery
	if mode == embedding.ModeDocument {
		m = ml.ModePassage
	}
	return e.client.Embed(ctx, e.tenant, m, texts)
}
