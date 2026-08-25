package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const officialSDKFixtureToolCount = 53

type blockingFlushRecorder struct {
	*httptest.ResponseRecorder
	flushed chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingFlushRecorder) Flush() {
	w.once.Do(func() { close(w.flushed) })
	<-w.release
}

func TestFixtureStreamableHTTPAcceptsOfficialSDKLifecycleOnHMACRoute(t *testing.T) {
	fixture, err := newFixtureListener()
	if err != nil {
		t.Fatalf("start fixture: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := fixture.Close(ctx); err != nil {
			t.Errorf("stop fixture: %v", err)
		}
	})

	route, err := fixture.routeForTrial("official-sdk-lifecycle")
	if err != nil {
		t.Fatalf("derive HMAC fixture route: %v", err)
	}
	endpoint := "http://" + fixture.Authority() + "/mcp/" + route
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "fixture-lifecycle-test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcpsdk.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		t.Fatalf("official SDK initialize then notifications/initialized: %v", err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close fixture session: %v", err)
		}
	})
	if session.ID() == "" {
		t.Fatal("official SDK did not receive a stateful MCP session ID")
	}

	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("official SDK tools/list after initialized: %v", err)
	}
	if len(tools.Tools) != officialSDKFixtureToolCount {
		t.Fatalf("official SDK tools/list count=%d, want exact %d", len(tools.Tools), officialSDKFixtureToolCount)
	}

	entries, ok := fixture.Ledger(route)
	if !ok {
		t.Fatal("fixture did not record the HMAC route")
	}
	counts := map[string]int{}
	for _, entry := range entries {
		counts[entry.Method]++
	}
	if counts["initialize"] != 1 || counts["notifications/initialized"] != 1 || counts["tools/list"] != 1 {
		t.Fatalf("fixture lifecycle ledger=%v, want initialize=1 initialized=1 tools/list=1", counts)
	}
}

func TestFixtureStreamableHTTPBindsSessionBeforeInitializeResponseFlushes(t *testing.T) {
	fixture, err := newFixtureListener()
	if err != nil {
		t.Fatalf("start fixture: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := fixture.Close(ctx); err != nil {
			t.Errorf("stop fixture: %v", err)
		}
	})

	route, err := fixture.routeForTrial("response-flush-race")
	if err != nil {
		t.Fatalf("derive HMAC fixture route: %v", err)
	}
	path := "/mcp/" + route
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	initReq := httptest.NewRequest(http.MethodPost, path, strings.NewReader(initialize))
	initReq.Header.Set("Content-Type", "application/json")
	initReq.Header.Set("Accept", "application/json, text/event-stream")
	initResp := &blockingFlushRecorder{ResponseRecorder: httptest.NewRecorder(), flushed: make(chan struct{}), release: make(chan struct{})}
	initDone := make(chan struct{})
	go func() {
		fixture.mcp.ServeHTTP(initResp, initReq)
		close(initDone)
	}()
	select {
	case <-initResp.flushed:
	case <-time.After(5 * time.Second):
		t.Fatal("initialize response did not flush")
	}
	defer func() {
		close(initResp.release)
		select {
		case <-initDone:
		case <-time.After(5 * time.Second):
			t.Error("initialize handler did not return after flush release")
		}
	}()

	sessionID := initResp.Header().Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatal("initialize did not expose a session ID before flush")
	}
	notification := `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`
	notifyReq := httptest.NewRequest(http.MethodPost, path, strings.NewReader(notification))
	notifyReq.Header.Set("Content-Type", "application/json")
	notifyReq.Header.Set("Accept", "application/json, text/event-stream")
	notifyReq.Header.Set("Mcp-Session-Id", sessionID)
	notifyResp := httptest.NewRecorder()
	fixture.mcp.ServeHTTP(notifyResp, notifyReq)
	if notifyResp.Code != http.StatusAccepted {
		t.Fatalf("notifications/initialized while initialize flush is blocked = %d, want %d", notifyResp.Code, http.StatusAccepted)
	}
}

func TestFixtureStreamableHTTPAcknowledgesInitializedWithoutJSONRPCResponse(t *testing.T) {
	fixture, err := newFixtureListener()
	if err != nil {
		t.Fatalf("start fixture: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := fixture.Close(ctx); err != nil {
			t.Errorf("stop fixture: %v", err)
		}
	})

	route, err := fixture.routeForTrial("notification-acknowledgement")
	if err != nil {
		t.Fatalf("derive HMAC fixture route: %v", err)
	}
	endpoint := "http://" + fixture.Authority() + "/mcp/" + route
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	initReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, strings.NewReader(initialize))
	if err != nil {
		t.Fatal(err)
	}
	initReq.Header.Set("Content-Type", "application/json")
	initReq.Header.Set("Accept", "application/json, text/event-stream")
	initResp, err := http.DefaultClient.Do(initReq)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	if err := initResp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if initResp.StatusCode != http.StatusOK || sessionID == "" {
		t.Fatalf("initialize status=%d session=%q, want 200 and stateful session", initResp.StatusCode, sessionID)
	}

	notification := `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`
	notifyReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, strings.NewReader(notification))
	if err != nil {
		t.Fatal(err)
	}
	notifyReq.Header.Set("Content-Type", "application/json")
	notifyReq.Header.Set("Accept", "application/json, text/event-stream")
	notifyReq.Header.Set("Mcp-Session-Id", sessionID)
	notifyResp, err := http.DefaultClient.Do(notifyReq)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(notifyResp.Body)
	closeErr := notifyResp.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read notification response: read=%v close=%v", readErr, closeErr)
	}
	if notifyResp.StatusCode != http.StatusAccepted || len(bytes.TrimSpace(body)) != 0 {
		t.Fatalf("notifications/initialized status=%d body=%q, want 202 with no JSON-RPC response", notifyResp.StatusCode, body)
	}
}

func TestFixtureStreamableHTTPRejectsUnknownMethodAndUnboundSession(t *testing.T) {
	fixture, err := newFixtureListener()
	if err != nil {
		t.Fatalf("start fixture: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := fixture.Close(ctx); err != nil {
			t.Errorf("stop fixture: %v", err)
		}
	})

	route, err := fixture.routeForTrial("rejection-lifecycle")
	if err != nil {
		t.Fatalf("derive HMAC fixture route: %v", err)
	}
	endpoint := "http://" + fixture.Authority() + "/mcp/" + route
	for _, request := range []struct {
		name      string
		body      string
		sessionID string
	}{
		{name: "unknown method", body: `{"jsonrpc":"2.0","id":1,"method":"prompts/list","params":{}}`},
		{name: "unbound tools list", body: `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`, sessionID: "not-a-fixture-session"},
	} {
		t.Run(request.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, strings.NewReader(request.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			if request.sessionID != "" {
				req.Header.Set("Mcp-Session-Id", request.sessionID)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status=%d, want %d", resp.StatusCode, http.StatusNotFound)
			}
		})
	}
}
