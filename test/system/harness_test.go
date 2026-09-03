//go:build system

package system

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/cookiejar"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/db/pgruntime"
	"github.com/CherryHQ/stella/test/testbed"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	readyTimeout    = 120 * time.Second
	gracefulTimeout = 45 * time.Second
)

type harness struct {
	owner   *testing.T
	runID   string
	baseURL string
	client  *http.Client
	db      *pgxpool.Pool
	proc    *testbed.Instance
}

var sharedFake *testbed.Instance

func newHarness(t *testing.T) *harness {
	t.Helper()
	skipUnsupportedHost(t)
	runID := newRunID(t)
	instance, err := testbed.Start(context.Background(), testbed.Options{RepoRoot: repoRoot(t), Port: 0, FakeModel: true, Bootstrap: false})
	if err != nil {
		t.Fatalf("system: start testbed: %v", err)
	}
	t.Cleanup(func() {
		if err := instance.Stop(); err != nil {
			t.Errorf("system: stop testbed: %v", err)
		}
	})
	db, err := pgxpool.New(context.Background(), instance.DatabaseURL())
	if err != nil {
		t.Fatalf("system: connect assertion pool: %v", err)
	}
	t.Cleanup(db.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("system: cookie jar: %v", err)
	}
	h := &harness{owner: t, runID: runID, baseURL: instance.BaseURL(), client: &http.Client{Jar: jar}, db: db, proc: instance}
	sharedFake = instance
	return h
}

func (h *harness) restartAfterForcedCrash(t *testing.T) *testbed.Instance {
	t.Helper()
	old := h.proc
	if err := old.Kill(); err != nil {
		t.Fatalf("system: forced crash: %v", err)
	}
	if err := old.Restart(context.Background()); err != nil {
		t.Fatalf("system: restart testbed: %v", err)
	}
	h.baseURL = old.BaseURL()
	return old
}

func skipUnsupportedHost(t *testing.T) {
	t.Helper()
	if _, ok := pgruntime.DefaultRuntimeSource(); !ok {
		t.Skipf("system: no embedded PostgreSQL runtime is published for %s/%s; %s", runtime.GOOS, runtime.GOARCH, pgruntime.MissingRuntimeHint())
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("system: cannot locate source file")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// newRunID returns a short random ID that scopes every fixture name of one
// run, so re-runs and shared infrastructure can never collide on business data.
func newRunID(t *testing.T) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("system: run id: %v", err)
	}
	return hex.EncodeToString(b[:])
}
