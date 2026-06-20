package db

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

// Embedded is a managed PostgreSQL server that stellad runs as a child process.
// It lets the server and its tests run against real PostgreSQL with nothing to
// install or operate: the server binary is downloaded once and cached under the
// user home, then started on demand. Start it, open the DSN with OpenDB, and
// Stop it on shutdown.
type Embedded struct {
	pg     *embeddedpostgres.EmbeddedPostgres
	port   uint32
	tmpDir string // per-instance scratch dir to remove on Stop (ephemeral mode only)
}

// StartEmbedded boots a PostgreSQL server on the given TCP port and returns a
// ready-to-use handle. A port of 0 picks a free ephemeral port — use it for
// tests so parallel test binaries never collide. dataDir is where the cluster's
// files live: a stable path persists data across restarts (the server runtime),
// while an empty string uses a throwaway temp dir that is removed on Stop (tests).
func StartEmbedded(dataDir string, port uint32) (*Embedded, error) {
	if port == 0 {
		p, err := freePort()
		if err != nil {
			return nil, fmt.Errorf("db: pick embedded postgres port: %w", err)
		}
		port = p
	}

	// Pin PostgreSQL 18 explicitly: the schema baseline defaults ids with
	// uuidv7(), a server built-in only since PG18 and with no extension fallback.
	// Left unpinned the cluster version silently tracks embedded-postgres'
	// default, so a future dependency bump that shifts that default off 18 would
	// fail migrate() with "function uuidv7() does not exist" on fresh installs and
	// refuse to start an existing PG18 cluster. (The external-DSN OpenDB path
	// carries the same PG>=18 requirement; see OpenDB.)
	cfg := embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V18).
		Username("postgres").
		Password("postgres").
		Database("stella").
		Port(port).
		StartTimeout(45 * time.Second)

	var tmpDir string
	if dataDir != "" {
		cfg = cfg.DataPath(dataDir)
	} else {
		// Ephemeral mode: give each instance its own extraction and data dirs so
		// parallel test binaries never share a runtime path (racing the binary
		// extraction) or a cluster directory. The downloaded archive (BinariesPath)
		// stays the shared default cache, so only the cheap extraction repeats.
		d, err := os.MkdirTemp("", "stella-pg-")
		if err != nil {
			return nil, fmt.Errorf("db: embedded postgres scratch dir: %w", err)
		}
		tmpDir = d
		cfg = cfg.RuntimePath(filepath.Join(d, "runtime")).DataPath(filepath.Join(d, "data"))
	}

	pg := embeddedpostgres.NewDatabase(cfg)
	if err := pg.Start(); err != nil {
		if tmpDir != "" {
			_ = os.RemoveAll(tmpDir)
		}
		return nil, fmt.Errorf("db: start embedded postgres: %w", err)
	}

	return &Embedded{pg: pg, port: port, tmpDir: tmpDir}, nil
}

// DSN returns the libpq connection string for the server's default "stella"
// database.
func (e *Embedded) DSN() string { return e.DSNFor("stella") }

// DSNFor returns the connection string for a named database on the server. Tests
// use it to reach the maintenance "postgres" database (to create and drop
// per-test databases) and the isolated databases they clone.
func (e *Embedded) DSNFor(database string) string {
	return fmt.Sprintf("postgres://postgres:postgres@localhost:%d/%s?sslmode=disable", e.port, database)
}

// Stop shuts the server down. It is safe to call once; a stopped server cannot
// be restarted (create a new one with StartEmbedded).
func (e *Embedded) Stop() error {
	stopErr := e.pg.Stop()
	if e.tmpDir != "" {
		_ = os.RemoveAll(e.tmpDir)
	}
	if stopErr != nil {
		return fmt.Errorf("db: stop embedded postgres: %w", stopErr)
	}
	return nil
}

// freePort asks the OS for an unused TCP port by binding to :0 and reading back
// the assigned port. The listener is closed immediately, so there is a small
// window before the server binds it; in practice this is fine for picking a
// per-process test port.
func freePort() (uint32, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return uint32(l.Addr().(*net.TCPAddr).Port), nil
}
