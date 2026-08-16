package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	httppprof "net/http/pprof"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"time"

	"github.com/CherryHQ/stella/internal/config"
)

// startDiagnostics wires runtime introspection for diagnosing hangs:
//
//   - On Unix, SIGUSR1 writes a full goroutine dump to $STELLA_HOME/dumps/.
//     Zero overhead, no pre-configuration: when the server wedges, run
//     `kill -USR1 <pid>` and inspect where goroutines are parked (e.g.
//     database/sql.(*DB).conn means connection-pool exhaustion).
//   - A non-empty pprofAddr (STELLA_PPROF_ADDR, e.g. 127.0.0.1:6060) starts a
//     localhost pprof server exposing goroutine, heap, block, and mutex
//     profiles. Block and mutex profiling carry overhead, so they activate only
//     when the address is set.
//
// Both stop when ctx is cancelled.
func startDiagnostics(ctx context.Context, pprofAddr string, admission func(context.Context) error) {
	installGoroutineDumpHandler(ctx, admission)
	startPprofServer(ctx, pprofAddr)
}

func dumpGoroutines(ctx context.Context, admission func(context.Context) error) {
	if admission != nil {
		if err := admission(ctx); err != nil {
			slog.Error("goroutine dump: Home storage admission closed", "error", err)
			return
		}
	}
	dir := filepath.Join(config.StellaHome(), "dumps")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		slog.Error("goroutine dump: create dir", "error", err)
		return
	}
	path := filepath.Join(dir, fmt.Sprintf("goroutine-%s.txt", time.Now().UTC().Format("20060102T150405Z")))
	f, err := os.Create(path)
	if err != nil {
		slog.Error("goroutine dump: create file", "error", err)
		return
	}
	defer func() { _ = f.Close() }()

	// debug=2 emits one stack per goroutine, the form most useful for
	// spotting where goroutines are blocked.
	if err := pprof.Lookup("goroutine").WriteTo(f, 2); err != nil {
		slog.Error("goroutine dump: write", "error", err)
		return
	}
	slog.Warn("goroutine dump written", "path", path, "goroutines", runtime.NumGoroutine())
}

func startPprofServer(ctx context.Context, addr string) {
	if addr == "" {
		return
	}

	// Capture contention data so block/mutex profiles are populated. Sampling
	// rates are modest to bound overhead.
	runtime.SetBlockProfileRate(int(time.Millisecond))
	runtime.SetMutexProfileFraction(100)

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", httppprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", httppprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", httppprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", httppprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", httppprof.Trace)

	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	go func() {
		slog.Info("pprof server listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("pprof server", "error", err)
		}
	}()
}
