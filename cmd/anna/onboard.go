package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/internal/admin"
	"github.com/vaayne/anna/internal/config"
	appdb "github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/memory"
)

func onboardCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "onboard",
		Usage: "Open the admin panel to set up anna",
		Flags: []ucli.Flag{
			&ucli.IntFlag{
				Name:  "port",
				Usage: "Port to listen on (0 = random)",
				Value: 0,
			},
		},
		Action: func(c *ucli.Context) error {
			return runOnboard(c.Context, c.Int("port"))
		},
	}
}

func runOnboard(ctx context.Context, port int) error {
	// 1. Create ANNA_HOME.
	home := config.AnnaHome()
	if err := os.MkdirAll(home, 0o755); err != nil {
		return fmt.Errorf("create anna home: %w", err)
	}

	// 2. Open DB.
	dbPath := filepath.Join(home, "anna.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	// 3. Create config Store and seed defaults.
	store := config.NewDBStore(db)
	if err := store.SeedDefaults(ctx); err != nil {
		return fmt.Errorf("seed defaults: %w", err)
	}

	// 4. Create memory engine (for session listing in admin panel).
	mem := memory.NewEngineFromDB(db, nil)

	// 5. Create admin server.
	srv := admin.New(store, mem, db)

	// 6. Listen and serve.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer func() { _ = ln.Close() }()

	addr := ln.Addr().String()
	url := "http://" + addr
	fmt.Printf("Anna admin panel running at %s\n", url)

	openBrowser(url)

	httpSrv := &http.Server{Handler: srv.Handler()}

	// Shutdown on context cancellation.
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	}
	if cmd != nil {
		if err := cmd.Start(); err != nil {
			slog.Warn("failed to open browser", "error", err)
			return
		}
		go func() { _ = cmd.Wait() }()
	}
}
