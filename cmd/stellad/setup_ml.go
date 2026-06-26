package main

import (
	"context"
	"log/slog"
	"path/filepath"

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
	sup := ml.NewSupervisor(ml.SupervisorConfig{
		BinPath:    r.BinPath,
		SocketPath: sockPath,
		Args: []string{
			"-runtime-lib", r.LibDir,
			"-embed-model", r.EmbedModelPath,
			"-tokenizer", r.TokenizerPath,
			"-runtime-version", r.RuntimeVersion,
		},
	}, logger)
	logger.Info("native ML sidecar resolved", "bin", r.BinPath, "runtime", r.RuntimeVersion, "model", r.ModelVersion)
	return sup, mlLocalEmbedder{client: sup.Client(), tenant: "embedding"}
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
