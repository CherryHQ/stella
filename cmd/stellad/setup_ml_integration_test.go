package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/document"
	"github.com/CherryHQ/stella/internal/embedding"
	"github.com/CherryHQ/stella/internal/mlruntime"
)

// TestSetupMLSidecarIntegration drives the full stellad-side wiring against a real
// stella-ml binary: resolve -> supervisor -> LocalEmbedder adapter -> e5 vector.
// Skipped unless the binary and assets are provided (CI without the runtime stays
// green):
//
//	STELLA_ML_BIN=/path/stella-ml STELLA_ML_RUNTIME_LIB=/path/lib \
//	STELLA_ML_EMBED_MODEL=/path/model_int8.onnx STELLA_ML_TOKENIZER=/path/tokenizer.json \
//	go test ./cmd/stellad/ -run MLSidecarIntegration -v
func TestSetupMLSidecarIntegration(t *testing.T) {
	bin := os.Getenv("STELLA_ML_BIN")
	lib := os.Getenv("STELLA_ML_RUNTIME_LIB")
	model := os.Getenv("STELLA_ML_EMBED_MODEL")
	tok := os.Getenv("STELLA_ML_TOKENIZER")
	if bin == "" || lib == "" || model == "" || tok == "" {
		t.Skip("set STELLA_ML_BIN/RUNTIME_LIB/EMBED_MODEL/TOKENIZER to run")
	}

	// Short STELLA_HOME so the STELLA_HOME/run/stella-ml.sock path stays under the
	// macOS unix-socket length cap.
	home, err := os.MkdirTemp("/tmp", "smlh")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(home) }()

	// Lay out a dev runtime + model dir the resolver recognizes, via symlinks.
	rtDir := filepath.Join(home, "rt")
	mdDir := filepath.Join(home, "md")
	if err := os.MkdirAll(rtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(mdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, bin, filepath.Join(rtDir, "stella-ml"))
	symlink(t, lib, filepath.Join(rtDir, "lib"))
	symlink(t, model, filepath.Join(mdDir, "model_int8.onnx"))
	symlink(t, tok, filepath.Join(mdDir, "tokenizer.json"))
	t.Setenv(mlruntime.EnvRuntimeDir, rtDir)
	t.Setenv(mlruntime.EnvModelDir, mdDir)

	// OCR is optional: only exercised when its three models + a test image are
	// provided via STELLA_ML_OCR_{DET,REC,KEYS} and STELLA_ML_OCR_IMAGE.
	det, rec, keys := os.Getenv("STELLA_ML_OCR_DET"), os.Getenv("STELLA_ML_OCR_REC"), os.Getenv("STELLA_ML_OCR_KEYS")
	ocrImage := os.Getenv("STELLA_ML_OCR_IMAGE")
	testOCR := det != "" && rec != "" && keys != "" && ocrImage != ""
	if testOCR {
		symlink(t, det, filepath.Join(mdDir, "det.onnx"))
		symlink(t, rec, filepath.Join(mdDir, "rec.onnx"))
		symlink(t, keys, filepath.Join(mdDir, "rec_keys.txt"))
		t.Setenv("STELLA_LOCAL_OCR", "1")
	}

	sup, emb := setupMLSidecar(home, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if sup == nil || emb == nil {
		t.Fatal("expected a resolved supervisor + embedder")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = sup.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	readyCtx, rc := context.WithTimeout(ctx, 60*time.Second)
	defer rc()
	if err := sup.Ready(readyCtx); err != nil {
		t.Fatalf("sidecar never ready: %v", err)
	}

	vecs, err := emb.EmbedLocal(ctx, embedding.ModeDocument, []string{"hello world"})
	if err != nil {
		t.Fatalf("EmbedLocal: %v", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 384 {
		t.Fatalf("vector shape = %dx%d, want 1x384", len(vecs), len(vecs[0]))
	}

	if testOCR {
		// setupMLSidecar installed the OCR fallback globally; drive it through the
		// real document extractor to prove the full read-tool path OCRs an image.
		img, rerr := os.ReadFile(ocrImage)
		if rerr != nil {
			t.Fatal(rerr)
		}
		res, oerr := document.NewExtractor().ExtractBytes(ctx, img, "image/png", document.Options{Timeout: 30 * time.Second})
		if oerr != nil {
			t.Fatalf("OCR ExtractBytes: %v", oerr)
		}
		if len(res.Content) == 0 {
			t.Fatal("OCR returned empty content")
		}
		t.Logf("OCR extracted %d chars", len(res.Content))
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}
