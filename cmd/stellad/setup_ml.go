package main

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/CherryHQ/stella/internal/config"
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
//
// store backs the runtime OCR toggle: when the OCR models are installed the engine
// is always loaded, but each OCR-eligible read re-reads config.LoadOCRSettings, so
// an admin can flip local OCR on/off from the settings UI without a restart.
func setupMLSidecar(stellaHome string, store config.SettingStore, logger *slog.Logger) (*ml.Supervisor, embedding.LocalEmbedder) {
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
	// Load OCR whenever the models are present; the enable/disable decision is a
	// per-request config check below, not a boot-time flag, so the toggle is live.
	// // always-load when installed; lazy load/unload if sidecar memory matters.
	if r.HasOCR() {
		args = append(args,
			"-ocr-det-model", r.OCRDetPath,
			"-ocr-rec-model", r.OCRRecPath,
			"-ocr-keys", r.OCRKeysPath,
		)
	}
	sup := ml.NewSupervisor(ml.SupervisorConfig{
		BinPath:    r.BinPath,
		SocketPath: sockPath,
		Args:       args,
	}, logger)

	// Install the document-extraction OCR fallback as a process-wide capability so
	// every read-tool extractor picks it up. The adapter gates each call on the
	// stored OCR setting, so this is a no-op until an admin enables it.
	if r.HasOCR() {
		document.SetLocalOCR(mlOCR{client: sup.Client(), tenant: "ocr", store: store})
	}

	logger.Info("native ML sidecar resolved", "bin", r.BinPath, "runtime", r.RuntimeVersion, "model", r.ModelVersion, "ocr", r.HasOCR())
	return sup, mlLocalEmbedder{client: sup.Client(), tenant: "embedding"}
}

// mlOCR adapts the sidecar client to document.OCR. It maps the sidecar's extract
// result to a document.Result and gates each call on the stored OCR toggle so the
// fallback stays off until an admin enables it. // fixed tenant; per-account once
// the read tool carries account context.
type mlOCR struct {
	client *ml.Client
	tenant string
	store  config.SettingStore
}

func (o mlOCR) Extract(ctx context.Context, mime string, data []byte, forceOCR bool) (*document.Result, error) {
	s, err := config.LoadOCRSettings(ctx, o.store)
	if err != nil {
		return nil, err
	}
	if !s.Enabled {
		// Off by config: behave as if no OCR backend is installed so the composite
		// extractor falls through to its base result.
		return nil, document.ErrUnavailable
	}
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
