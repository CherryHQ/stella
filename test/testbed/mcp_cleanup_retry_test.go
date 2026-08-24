package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestCleanupLeaseDeletesOwnedLibraryFilesBeforeAgent(t *testing.T) {
	var calls []string
	agentAttempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.RequestURI())
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/api/library-files":
			_, _ = w.Write([]byte(`{"library_files":[{"id":"fixture-file"}]}`))
		case req.Method == http.MethodDelete && req.URL.Path == "/api/agents/agent":
			agentAttempts++
			if agentAttempts == 1 {
				http.Error(w, "library cleanup pending", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case req.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", req.Method, req.URL.RequestURI())
		}
	}))
	defer server.Close()
	cleanup := &cleanupServer{baseURL: server.URL, client: server.Client(), leases: map[string]*cleanupLease{
		"lease": {agentID: "agent", registrationID: "registration", token: []byte("canary-token"), libraryFixture: true},
	}}

	out, err := cleanup.cleanup("lease")
	if err != nil {
		t.Fatalf("cleanup lease: %v", err)
	}
	if want := []string{"registration", "library_files", "agent"}; !reflect.DeepEqual(out, want) {
		t.Fatalf("cleanup outcomes = %v, want %v", out, want)
	}
	wantCalls := []string{
		"DELETE /api/mcp/servers/registration?scope=user_agent&agent_id=agent",
		"GET /api/library-files?scope=user_agent&agent_id=agent",
		"DELETE /api/library-files/fixture-file",
		"DELETE /api/agents/agent",
		"DELETE /api/agents/agent",
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("cleanup calls = %v, want %v", calls, wantCalls)
	}
}

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
		"lease": {agentID: "agent", registrationID: "registration", token: []byte("canary-token"), libraryFilesDeleted: true},
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
