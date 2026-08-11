package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// deviceAuthServer returns an httptest server whose device-auth endpoint issues
// a code and whose token endpoint responds via tokenHandler.
func deviceAuthServer(t *testing.T, tokenHandler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":      "dev-code",
			"user_code":        "USER-CODE",
			"verification_uri": "https://example.com/activate",
			"expires_in":       600,
			"interval":         1,
		})
	})
	mux.HandleFunc("/token", tokenHandler)
	return httptest.NewServer(mux)
}

func deviceBrokerConfig(srv *httptest.Server) *oauth2.Config {
	return &oauth2.Config{
		ClientID: "client-id",
		Endpoint: oauth2.Endpoint{
			TokenURL:      srv.URL + "/token",
			DeviceAuthURL: srv.URL + "/device",
			AuthStyle:     oauth2.AuthStyleInParams,
		},
	}
}

// waitForState polls the store until flowID reaches state or the deadline passes.
func waitForState(t *testing.T, store *FlowStore, flowID string, state FlowState) FlowStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fs, ok := store.Get(flowID); ok && fs.State == state {
			return fs
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("flow %q did not reach state %q within deadline", flowID, state)
	return FlowStatus{}
}

func TestDeviceCodeBroker_PollFailureSetsError(t *testing.T) {
	// A terminal token error must surface as FlowStateFailed with a reason (D5).
	srv := deviceAuthServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "access_denied"})
	})
	defer srv.Close()

	store := NewFlowStore()
	broker := NewDeviceCodeBroker(deviceBrokerConfig(srv), store, nil)

	status, err := broker.StartFlow(context.Background(), ProviderGitHub, "user-1", []string{"repo"})
	if err != nil {
		t.Fatalf("StartFlow: %v", err)
	}
	if len(status.DesiredScopes) != 1 || status.DesiredScopes[0] != "repo" {
		t.Fatalf("DesiredScopes = %v, want [repo]", status.DesiredScopes)
	}

	failed := waitForState(t, store, status.FlowID, FlowStateFailed)
	if failed.Error == "" {
		t.Error("expected non-empty Error on device-flow poll failure")
	}
}

func TestDeviceCodeBroker_PersistFailureSetsError(t *testing.T) {
	// Token exchange succeeds but persistence fails; the failure reason must
	// propagate to the flow status (D5).
	srv := deviceAuthServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	})
	defer srv.Close()

	store := NewFlowStore()
	persistErr := errors.New("vault write boom")
	broker := NewDeviceCodeBroker(deviceBrokerConfig(srv), store, func(string, *oauth2.Token) error {
		return persistErr
	})

	status, err := broker.StartFlow(context.Background(), ProviderGitHub, "user-1", []string{"repo"})
	if err != nil {
		t.Fatalf("StartFlow: %v", err)
	}

	failed := waitForState(t, store, status.FlowID, FlowStateFailed)
	if failed.Error != persistErr.Error() {
		t.Errorf("Error = %q, want %q", failed.Error, persistErr.Error())
	}
}

func TestDeviceCodeBrokerSupersededFlowDoesNotPersistToken(t *testing.T) {
	enteredTokenEndpoint := make(chan struct{})
	releaseToken := make(chan struct{})
	srv := deviceAuthServer(t, func(w http.ResponseWriter, _ *http.Request) {
		close(enteredTokenEndpoint)
		<-releaseToken
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "superseded-token", "token_type": "Bearer", "expires_in": 3600,
		})
	})
	defer srv.Close()

	store := NewFlowStore()
	persisted := make(chan struct{}, 1)
	broker := NewDeviceCodeBroker(deviceBrokerConfig(srv), store, func(string, *oauth2.Token) error {
		persisted <- struct{}{}
		return nil
	})
	oldFlow, err := broker.StartFlow(context.Background(), ProviderGitHub, "user-1", []string{"repo"})
	if err != nil {
		t.Fatalf("StartFlow: %v", err)
	}
	select {
	case <-enteredTokenEndpoint:
	case <-time.After(3 * time.Second):
		t.Fatal("device token endpoint was not called")
	}
	replacement := FlowStatus{
		Provider: ProviderGitHub, FlowID: "replacement", UserID: "user-1",
		State: FlowStatePending, ExpiresAt: time.Now().Add(time.Minute),
	}
	if !store.CreateExclusive(replacement) {
		t.Fatal("replacement flow was rejected")
	}
	close(releaseToken)
	select {
	case <-persisted:
		t.Fatal("superseded device flow persisted its token")
	case <-time.After(300 * time.Millisecond):
	}
	if _, ok := store.Get(oldFlow.FlowID); ok {
		t.Fatal("superseded device flow remains in store")
	}
}

func TestAuthCodeBrokerCompleteClaimsFlowOnce(t *testing.T) {
	tokenCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access", "token_type": "Bearer", "expires_in": 3600,
		})
	}))
	defer srv.Close()

	store := NewFlowStore()
	broker := NewAuthCodeBroker(&oauth2.Config{
		ClientID: "client",
		Endpoint: oauth2.Endpoint{AuthURL: "https://example.com/authorize", TokenURL: srv.URL},
	}, store, false)
	flow, err := broker.StartFlow(context.Background(), ProviderGitHub, "user-1", []string{"repo"})
	if err != nil {
		t.Fatalf("StartFlow: %v", err)
	}
	if _, err := broker.Complete(context.Background(), flow.FlowID, "code"); err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	if _, err := broker.Complete(context.Background(), flow.FlowID, "code"); err == nil {
		t.Fatal("replayed Complete succeeded")
	}
	if tokenCalls != 1 {
		t.Fatalf("token endpoint calls = %d, want 1", tokenCalls)
	}
}
