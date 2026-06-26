package ml

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSupervisorIntegration exercises spawn -> health-gate -> embed -> reap against
// a real stella-ml binary. It is skipped unless the binary and model assets are
// provided, so CI without the runtime bundle stays green:
//
//	STELLA_ML_BIN=/path/to/stella-ml \
//	STELLA_ML_RUNTIME_LIB=/path/to/onnxruntime/lib \
//	STELLA_ML_EMBED_MODEL=/path/to/model_int8.onnx \
//	STELLA_ML_TOKENIZER=/path/to/tokenizer.json \
//	go test ./internal/ml/ -run Integration -v
func TestSupervisorIntegration(t *testing.T) {
	bin := os.Getenv("STELLA_ML_BIN")
	lib := os.Getenv("STELLA_ML_RUNTIME_LIB")
	model := os.Getenv("STELLA_ML_EMBED_MODEL")
	tok := os.Getenv("STELLA_ML_TOKENIZER")
	if bin == "" || lib == "" || model == "" || tok == "" {
		t.Skip("set STELLA_ML_BIN/RUNTIME_LIB/EMBED_MODEL/TOKENIZER to run")
	}

	sock := filepath.Join(t.TempDir(), "s")
	sup := NewSupervisor(SupervisorConfig{
		BinPath:    bin,
		SocketPath: sock,
		Args: []string{
			"-runtime-lib", lib,
			"-embed-model", model,
			"-tokenizer", tok,
			"-runtime-version", "itest",
		},
		HealthGate: 60 * time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = sup.Run(ctx); close(done) }()

	readyCtx, readyCancel := context.WithTimeout(ctx, 60*time.Second)
	defer readyCancel()
	if err := sup.Ready(readyCtx); err != nil {
		cancel()
		t.Fatalf("never became ready: %v", err)
	}

	vecs, err := sup.Client().Embed(ctx, "itest", ModeQuery, []string{"hello world"})
	if err != nil {
		cancel()
		t.Fatalf("embed: %v", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 384 {
		cancel()
		t.Fatalf("unexpected vector shape: %d x %d", len(vecs), len(vecs[0]))
	}

	// Cancel and confirm the supervisor reaps the process and the socket is gone.
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("supervisor did not stop after cancel")
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("socket not cleaned up after reap: %v", err)
	}
}
