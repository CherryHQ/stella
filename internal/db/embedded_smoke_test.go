package db

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedSmoke proves the Phase 6 backbone end-to-end: an embedded server
// starts, OpenDB migrates the full baseline (and ensures the pgvector and
// pg_search extensions) against it, and a query round-trips. If this passes,
// every other PG test can
// rely on the same path.
func TestEmbeddedSmoke(t *testing.T) {
	e, err := StartEmbedded("", 0)
	if err != nil {
		t.Fatalf("StartEmbedded: %v", err)
	}
	t.Cleanup(func() { _ = e.Stop() })

	db, err := OpenDB(e.DSN(), WithMaxConns(4))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	var n int
	if err := db.QueryRow(context.Background(), "SELECT 1").Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 {
		t.Fatalf("SELECT 1 = %d, want 1", n)
	}
}

func TestStartTestNetworkEmbeddedUsesSelectedGateway(t *testing.T) {
	// Loopback is sufficient for a package test; release Docker/kind runners pass
	// their concrete bridge gateway through the same validated path.
	const subnet = "127.0.0.0/8"
	e, err := StartTestNetworkEmbedded(net.IPv4(127, 0, 0, 1).String(), subnet, 0)
	if err != nil {
		t.Fatalf("StartTestNetworkEmbedded: %v", err)
	}
	t.Cleanup(func() { _ = e.Stop() })

	hba, err := os.ReadFile(filepath.Join(e.tmpDir, "data", "pg_hba.conf"))
	if err != nil {
		t.Fatalf("read pg_hba.conf: %v", err)
	}
	if rule := "host all all " + subnet + " scram-sha-256"; !strings.Contains(string(hba), rule) {
		t.Fatalf("pg_hba.conf does not contain selected-network rule %q", rule)
	}

	db, err := OpenDB(e.DSNForHost("127.0.0.1", "stella"), WithMaxConns(2))
	if err != nil {
		t.Fatalf("OpenDB through selected gateway: %v", err)
	}
	t.Cleanup(db.Close)

	var n int
	if err := db.QueryRow(context.Background(), "SELECT 1").Scan(&n); err != nil {
		t.Fatalf("query through selected gateway: %v", err)
	}
	if n != 1 {
		t.Fatalf("SELECT 1 = %d, want 1", n)
	}
}

func TestStartTestNetworkEmbeddedRejectsUnsafeNetwork(t *testing.T) {
	tests := []struct {
		name    string
		gateway string
		subnet  string
	}{
		{name: "unspecified gateway", gateway: "0.0.0.0", subnet: "172.18.0.0/16"},
		{name: "gateway outside subnet", gateway: "172.19.0.1", subnet: "172.18.0.0/16"},
		{name: "all IPv4 sources", gateway: "172.18.0.1", subnet: "0.0.0.0/0"},
		{name: "IPv6 is not a Docker release target", gateway: "::1", subnet: "::1/128"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Validation must fail before a PostgreSQL runtime is acquired.
			if e, err := StartTestNetworkEmbedded(tt.gateway, tt.subnet, 0); err == nil {
				_ = e.Stop()
				t.Fatal("StartTestNetworkEmbedded unexpectedly accepted an unsafe network")
			}
		})
	}
}
