package server

import (
	"context"
	"testing"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	lcmmemory "github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/internal/pluginhost"
	cfgstore "github.com/CherryHQ/stella/internal/store"
)

// TestNewUsesInjectedInstances proves the server holds the exact shared
// instances it was given — it neither copies nor reconstructs them. Together
// with TestServerNewReadsNoEnvConstructsNoService (which proves New builds no
// service), this is the shared-instance-identity guarantee: the same
// credentials/email/share/recally/front-door objects the composition root wires
// into the agent tools also back the HTTP endpoints.
func TestNewUsesInjectedInstances(t *testing.T) {
	db := dbtest.New(t)
	store := cfgstore.NewDBStore(db)
	as := appdb.NewAuthStore(db)
	mem, err := lcmmemory.New(db, nil, nil)
	if err != nil {
		t.Fatalf("lcm.New: %v", err)
	}
	phost := pluginhost.New(store)

	deps := testServerDeps(t, store, as, mem, db, phost)
	srv, err := New(context.Background(), deps)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	if srv.credSvc != deps.Credentials {
		t.Error("server.credSvc is not the injected Credentials instance")
	}
	if srv.emailSvc != deps.Email {
		t.Error("server.emailSvc is not the injected Email instance")
	}
	if srv.shareSvc != deps.Share {
		t.Error("server.shareSvc is not the injected Share instance")
	}
	if srv.recallySvc != deps.Recally {
		t.Error("server.recallySvc is not the injected Recally instance")
	}
	if srv.credResolver != deps.CredentialFrontDoor {
		t.Error("server.credResolver is not the injected CredentialFrontDoor instance")
	}
	if srv.oauthAS != deps.OAuthAuthServer {
		t.Error("server.oauthAS is not the injected OAuthAuthServer instance")
	}
	if srv.poolManager != deps.PoolManager {
		t.Error("server.poolManager is not the injected PoolManager instance")
	}
}
