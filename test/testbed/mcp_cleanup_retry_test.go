package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCleanupLeaseSurvivesTransientAPIErrorForRetry(t *testing.T) {
	fail := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			http.Error(w, "transient", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	cleanup := &cleanupServer{baseURL: server.URL, client: server.Client(), leases: map[string]*cleanupLease{
		"lease": {agentID: "agent", registrationID: "registration", token: []byte("canary-token")},
	}}
	if _, err := cleanup.cleanup("lease"); err == nil {
		t.Fatal("transient delete succeeded")
	}
	if cleanup.leases["lease"] == nil {
		t.Fatal("transient failure discarded retry lease")
	}
	fail = false
	if _, err := cleanup.cleanup("lease"); err != nil {
		t.Fatal(err)
	}
	if cleanup.leases["lease"] == nil {
		t.Fatal("completed cleanup released the lease before provisioned-user cleanup")
	}
	if err := cleanup.release("lease"); err != nil {
		t.Fatal(err)
	}
	if cleanup.leases["lease"] != nil {
		t.Fatal("release retained the completed cleanup lease")
	}
}
