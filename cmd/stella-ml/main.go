// Command stella-ml is Stella's native ML sidecar. It hosts onnxruntime + the HF
// tokenizer + the embedding/OCR models in a single CGO process and serves them to
// the (pure-Go) stellad over an HTTP-on-unix-socket contract. See protocol.go.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// config holds the resolved flags. The embedding model is required; OCR is
// optional (its three paths are all-or-nothing) so an embedding-only bundle still
// boots and /v1/extract reports 503 until OCR models are installed.
type config struct {
	socketPath  string
	runtimeLib  string
	embedModel  string
	tokenizer   string
	ocrDet      string
	ocrRec      string
	ocrKeys     string
	runtimeVer  string
	modelDigest string
	intraOp     int
	interOp     int
	embedConc   int
	extractConc int
}

func main() {
	var (
		cfg         config
		socketPath  = flag.String("socket", "", "unix socket path to listen on (required)")
		runtimeLib  = flag.String("runtime-lib", "", "path to libonnxruntime (dir or file) (required)")
		modelPath   = flag.String("embed-model", "", "path to the e5 embedding model.onnx (required)")
		tokenizer   = flag.String("tokenizer", "", "path to tokenizer.json (required)")
		ocrDet      = flag.String("ocr-det-model", "", "path to the PP-OCR detection model.onnx (optional; enables /v1/extract)")
		ocrRec      = flag.String("ocr-rec-model", "", "path to the PP-OCR recognition model.onnx (optional)")
		ocrKeys     = flag.String("ocr-keys", "", "path to the PP-OCR rec character dictionary (optional)")
		runtimeVer  = flag.String("runtime-version", "dev", "runtime version reported on /healthz")
		modelDigest = flag.String("model-digest", "", "model-manifest digest reported on /healthz")
		intraOp     = flag.Int("intra-op-threads", 1, "onnxruntime intra-op thread count (pinned)")
		interOp     = flag.Int("inter-op-threads", 1, "onnxruntime inter-op thread count (pinned)")
		embedConc   = flag.Int("embed-concurrency", 2, "max concurrent embed requests (lane size)")
		extractConc = flag.Int("extract-concurrency", 2, "max concurrent extract requests (lane size)")
	)
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if *socketPath == "" || *runtimeLib == "" || *modelPath == "" || *tokenizer == "" {
		log.Error("missing required flag", "need", "-socket -runtime-lib -embed-model -tokenizer")
		os.Exit(2)
	}

	cfg = config{
		socketPath: *socketPath, runtimeLib: *runtimeLib, embedModel: *modelPath, tokenizer: *tokenizer,
		ocrDet: *ocrDet, ocrRec: *ocrRec, ocrKeys: *ocrKeys,
		runtimeVer: *runtimeVer, modelDigest: *modelDigest,
		intraOp: *intraOp, interOp: *interOp, embedConc: *embedConc, extractConc: *extractConc,
	}
	if err := run(log, cfg); err != nil {
		log.Error("stella-ml exited", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, cfg config) error {
	if err := initORT(cfg.runtimeLib); err != nil {
		return err
	}
	defer shutdownORT()

	embed, err := newE5Engine(cfg.embedModel, cfg.tokenizer, cfg.intraOp, cfg.interOp)
	if err != nil {
		return err
	}
	defer embed.close()
	log.Info("embed engine loaded", "model", embedModelID)

	// OCR is optional and all-or-nothing across its three assets.
	var ocr *ocrEngine
	if cfg.ocrDet != "" && cfg.ocrRec != "" && cfg.ocrKeys != "" {
		ocr, err = newOCREngine(cfg.ocrDet, cfg.ocrRec, cfg.ocrKeys, cfg.intraOp, cfg.interOp)
		if err != nil {
			return err
		}
		defer ocr.close()
		log.Info("ocr engine loaded", "model", ocrModelID)
	}

	ln, err := listenUnix(cfg.socketPath)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(cfg.socketPath) }()

	srv := newServer(embed, ocr, cfg.runtimeVer, cfg.modelDigest, defaultLimits(), cfg.embedConc, cfg.extractConc, log)
	httpSrv := &http.Server{
		Handler:           srv.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Shut down gracefully on signal so the supervisor's restart logic sees a clean
	// exit and the socket is removed.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		log.Info("shutting down")
		shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shCtx)
	}()

	log.Info("listening", "socket", cfg.socketPath, "runtime_version", cfg.runtimeVer)
	if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// listenUnix binds the unix socket with a private dir (0700) and socket (0600),
// removing a stale socket left by a crashed predecessor. If a live sidecar is
// already listening, it refuses to start.
func listenUnix(socketPath string) (net.Listener, error) {
	dir := filepath.Dir(socketPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(socketPath); err == nil {
		// A socket file exists. Probe it: a successful dial means another instance
		// owns it; a failed dial means it is stale and safe to remove.
		if c, derr := net.DialTimeout("unix", socketPath, 500*time.Millisecond); derr == nil {
			_ = c.Close()
			return nil, errors.New("stella-ml already running on " + socketPath)
		}
		if err := os.Remove(socketPath); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}
