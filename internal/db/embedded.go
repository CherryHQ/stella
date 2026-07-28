package db

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
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
	return startEmbedded(dataDir, port, "", "")
}

// StartTestNetworkEmbedded starts an ephemeral PostgreSQL cluster that also
// listens on one explicitly selected container-network gateway and accepts
// password-authenticated clients only from that network's CIDR. It exists only
// for release deployment tests whose candidate Docker container or kind pod
// must reach the pinned Stella PostgreSQL runtime running on the host. Binding
// every host interface or authorizing every IPv4 source is deliberately
// forbidden.
func StartTestNetworkEmbedded(gateway, subnet string, port uint32) (*Embedded, error) {
	gatewayIP := net.ParseIP(gateway)
	_, network, err := net.ParseCIDR(subnet)
	switch {
	case gatewayIP == nil || gatewayIP.IsUnspecified() || gatewayIP.To4() == nil:
		return nil, fmt.Errorf("db: test PostgreSQL gateway %q must be a concrete IPv4 address", gateway)
	case err != nil || network.IP.To4() == nil:
		return nil, fmt.Errorf("db: test PostgreSQL subnet %q must be an IPv4 CIDR", subnet)
	case !network.Contains(gatewayIP):
		return nil, fmt.Errorf("db: test PostgreSQL gateway %q is outside subnet %q", gateway, subnet)
	}
	prefix, bits := network.Mask.Size()
	if bits != 32 || prefix == 0 {
		return nil, fmt.Errorf("db: test PostgreSQL subnet %q cannot authorize every IPv4 source", subnet)
	}
	return startEmbedded("", port, gatewayIP.To4().String(), network.String())
}

func startEmbedded(dataDir string, port uint32, networkGateway, networkSubnet string) (*Embedded, error) {
	requestedPort := port
	for attempt := 0; ; attempt++ {
		e, err := startEmbeddedOnce(dataDir, port, networkGateway, networkSubnet)
		if err == nil {
			return e, nil
		}
		if requestedPort != 0 || attempt >= 4 || !strings.Contains(err.Error(), "process already listening on port") {
			return nil, err
		}
		port = 0
	}
}

func startEmbeddedOnce(dataDir string, port uint32, networkGateway, networkSubnet string) (*Embedded, error) {
	if port == 0 {
		p, err := freePort()
		if err != nil {
			return nil, fmt.Errorf("db: pick embedded postgres port: %w", err)
		}
		port = p
	}

	var tmpDir string
	if dataDir == "" {
		// Ephemeral mode: give each instance its own extraction and data dirs so
		// parallel test binaries never share a runtime path (racing the binary
		// extraction) or a cluster directory.
		d, err := os.MkdirTemp("", "stella-pg-")
		if err != nil {
			return nil, fmt.Errorf("db: embedded postgres scratch dir: %w", err)
		}
		tmpDir = d
	}
	rt, err := newPostgresRuntimeInfo(dataDir, tmpDir)
	if err != nil {
		if tmpDir != "" {
			_ = os.RemoveAll(tmpDir)
		}
		return nil, err
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
		RuntimePath(rt.RuntimePath).
		DataPath(rt.DataPath).
		Username("postgres").
		Password("postgres").
		Database("stella").
		Port(port).
		StartTimeout(45 * time.Second)
	if rt.BinariesPath != "" {
		cfg = cfg.BinariesPath(rt.BinariesPath)
	}
	params := rt.startParameters()
	if networkGateway != "" {
		// Keep localhost for embedded-postgres' own startup health check while
		// exposing only the Docker/kind bridge selected by the release runner.
		params["listen_addresses"] = "localhost," + networkGateway
	}
	if len(params) > 0 {
		cfg = cfg.StartParameters(params)
	}

	pg := embeddedpostgres.NewDatabase(cfg)
	if err := pg.Start(); err != nil {
		if tmpDir != "" {
			_ = os.RemoveAll(tmpDir)
		}
		return nil, fmt.Errorf("db: start embedded postgres: %w", err)
	}
	if networkSubnet != "" {
		if err := authorizeTestNetwork(rt.DataPath, port, networkSubnet); err != nil {
			stopErr := pg.Stop()
			if tmpDir != "" {
				_ = os.RemoveAll(tmpDir)
			}
			return nil, fmt.Errorf("db: authorize test PostgreSQL network: %w", errors.Join(err, stopErr))
		}
	}

	return &Embedded{pg: pg, port: port, tmpDir: tmpDir}, nil
}

// authorizeTestNetwork appends one narrow host rule after initdb has created
// pg_hba.conf, then asks the running server to reload it. The rule is scoped to
// the ephemeral Docker/kind subnet supplied by the release test.
func authorizeTestNetwork(dataPath string, port uint32, subnet string) error {
	hbaPath := filepath.Join(dataPath, "pg_hba.conf")
	hba, err := os.OpenFile(hbaPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", hbaPath, err)
	}
	_, writeErr := fmt.Fprintf(
		hba,
		"\n# Stella release deployment test network\nhost all all %s scram-sha-256\n",
		subnet,
	)
	syncErr := hba.Sync()
	closeErr := hba.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("write %s: %w", hbaPath, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dsn := fmt.Sprintf(
		"postgres://postgres:postgres@%s/stella?sslmode=disable",
		net.JoinHostPort("127.0.0.1", strconv.FormatUint(uint64(port), 10)),
	)
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect for pg_hba reload: %w", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	var reloaded bool
	if err := conn.QueryRow(ctx, "SELECT pg_reload_conf()").Scan(&reloaded); err != nil {
		return fmt.Errorf("reload pg_hba.conf: %w", err)
	}
	if !reloaded {
		return fmt.Errorf("reload pg_hba.conf returned false")
	}
	return nil
}

// DSN returns the libpq connection string for the server's default "stella"
// database.
func (e *Embedded) DSN() string { return e.DSNFor("stella") }

// DSNFor returns the connection string for a named database on the server. Tests
// use it to reach the maintenance "postgres" database (to create and drop
// per-test databases) and the isolated databases they clone.
func (e *Embedded) DSNFor(database string) string {
	return e.DSNForHost("localhost", database)
}

// DSNForHost returns the fixed test-credential DSN through a caller-selected
// host. Release deployment tests use the Docker or kind bridge gateway while
// host-side assertions continue to use DSN().
func (e *Embedded) DSNForHost(host, database string) string {
	address := net.JoinHostPort(host, strconv.FormatUint(uint64(e.port), 10))
	return fmt.Sprintf("postgres://postgres:postgres@%s/%s?sslmode=disable", address, database)
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
