package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const cleanupSocketFilename = "mcp-fixture-cleanup.sock"

type cleanupClaim struct {
	Action         string `json:"action"`
	Token          string `json:"token"`
	Trial          string `json:"trial"`
	UserID         string `json:"user_id"`
	AgentID        string `json:"agent_id"`
	RegistrationID string `json:"registration_id"`
}

type cleanupRequest struct {
	Action string `json:"action"`
	Lease  string `json:"lease"`
}

type cleanupResponse struct {
	Lease    string          `json:"lease,omitempty"`
	Error    string          `json:"error,omitempty"`
	Outcomes []string        `json:"outcomes,omitempty"`
	Inspect  *fixtureInspect `json:"inspect,omitempty"`
}

type fixtureInspect struct {
	Version             int      `json:"version"`
	Complete            bool     `json:"complete"`
	RouteDigest         string   `json:"route_digest"`
	CatalogCount        int      `json:"catalog_count"`
	InitializeCount     int      `json:"initialize_count"`
	ToolsListCount      int      `json:"tools_list_count"`
	RequiredToolNames   []string `json:"required_tool_names"`
	AckWriteCount       int      `json:"ack_write_count"`
	DuplicateWriteCount int      `json:"duplicate_write_count"`
	ChainComplete       bool     `json:"chain_complete"`
}

type cleanupLease struct {
	trial, userID, agentID, registrationID string
	token                                  []byte
	registrationDeleted                    bool
	agentDeleted                           bool
}

// cleanupServer is not a registration control plane. The driver creates the
// MCP row through the ordinary authenticated API, then this server validates
// and retains one already-created tuple solely so the Python parent can clean
// it after SIGKILL without ever receiving a user token.
type cleanupServer struct {
	listener *net.UnixListener
	baseURL  string
	fixture  *fixtureListener
	client   *http.Client
	mu       sync.Mutex
	leases   map[string]*cleanupLease
}

func newCleanupServer(root, baseURL string, fixture *fixtureListener) (*cleanupServer, error) {
	path := filepath.Join(root, cleanupSocketFilename)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	s := &cleanupServer{listener: listener, baseURL: baseURL, fixture: fixture, client: &http.Client{Timeout: 15 * time.Second}, leases: map[string]*cleanupLease{}}
	go s.serve()
	return s, nil
}

func (s *cleanupServer) Socket() string { return s.listener.Addr().String() }
func (s *cleanupServer) Close() error   { return s.listener.Close() }

func (s *cleanupServer) serve() {
	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *cleanupServer) handle(conn *net.UnixConn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	var raw json.RawMessage
	if err := json.NewDecoder(io.LimitReader(conn, 32<<10)).Decode(&raw); err != nil {
		_ = json.NewEncoder(conn).Encode(cleanupResponse{Error: "invalid request"})
		return
	}
	var req cleanupRequest
	if json.Unmarshal(raw, &req) != nil {
		_ = json.NewEncoder(conn).Encode(cleanupResponse{Error: "invalid request"})
		return
	}
	switch req.Action {
	case "claim":
		var claim cleanupClaim
		if json.Unmarshal(raw, &claim) != nil {
			_ = json.NewEncoder(conn).Encode(cleanupResponse{Error: "invalid claim"})
			return
		}
		lease, err := s.Claim(context.Background(), claim)
		if err != nil {
			_ = json.NewEncoder(conn).Encode(cleanupResponse{Error: "claim rejected"})
			return
		}
		_ = json.NewEncoder(conn).Encode(cleanupResponse{Lease: lease})
	case "inspect":
		inspect, err := s.inspect(req.Lease)
		if err != nil {
			_ = json.NewEncoder(conn).Encode(cleanupResponse{Error: "inspect failed"})
			return
		}
		_ = json.NewEncoder(conn).Encode(cleanupResponse{Inspect: inspect})
	case "cleanup":
		out, err := s.cleanup(req.Lease)
		if err != nil {
			_ = json.NewEncoder(conn).Encode(cleanupResponse{Error: "cleanup failed"})
			return
		}
		_ = json.NewEncoder(conn).Encode(cleanupResponse{Outcomes: out})
	default:
		_ = json.NewEncoder(conn).Encode(cleanupResponse{Error: "invalid action"})
	}
}

// Claim is called in-process by the host driver through the Unix protocol in a
// later adapter step. It is kept explicit here so tests can prove tuple checks
// without accepting arbitrary cleanup inputs.
func (s *cleanupServer) Claim(ctx context.Context, in cleanupClaim) (string, error) {
	if in.Action != "claim" || in.Token == "" || in.Trial == "" || in.UserID == "" || in.AgentID == "" || in.RegistrationID == "" {
		return "", errors.New("invalid claim")
	}
	if err := s.validateClaim(ctx, in); err != nil {
		return "", err
	}
	lease := randomFixtureID()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.leases[lease] = &cleanupLease{trial: in.Trial, userID: in.UserID, agentID: in.AgentID, registrationID: in.RegistrationID, token: []byte(in.Token)}
	return lease, nil
}

func (s *cleanupServer) validateClaim(ctx context.Context, in cleanupClaim) error {
	var me struct {
		ID string `json:"id"`
	}
	if err := s.api(ctx, http.MethodGet, "/api/auth/me", in.Token, nil, &me); err != nil || me.ID != in.UserID {
		return errors.New("claim identity")
	}
	var list struct {
		Servers []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Url  string `json:"url"`
		} `json:"servers"`
	}
	path := "/api/mcp/servers?scope=user_agent&agent_id=" + url.QueryEscape(in.AgentID)
	if err := s.api(ctx, http.MethodGet, path, in.Token, nil, &list); err != nil {
		return errors.New("claim registration")
	}
	wantRoute, err := s.fixture.routeForTrial(in.Trial)
	if err != nil {
		return errors.New("claim route")
	}
	wantURL := "http://" + s.fixture.Authority() + "/mcp/" + wantRoute
	for _, server := range list.Servers {
		if server.ID == in.RegistrationID && server.Name == fixtureRegistrationName && server.Url == wantURL {
			return nil
		}
	}
	return errors.New("claim ownership")
}

func (s *cleanupServer) inspect(leaseID string) (*fixtureInspect, error) {
	s.mu.Lock()
	lease := s.leases[leaseID]
	s.mu.Unlock()
	if lease == nil {
		return nil, errors.New("unknown lease")
	}
	route, err := s.fixture.routeForTrial(lease.trial)
	if err != nil {
		return nil, err
	}
	entries, ok := s.fixture.Ledger(route)
	if !ok {
		return nil, errors.New("missing ledger")
	}
	out := &fixtureInspect{Version: 1, CatalogCount: fixtureToolCount}
	seen := map[string]bool{}
	for _, entry := range entries {
		switch entry.Method {
		case "initialize":
			out.InitializeCount++
		case "tools/list":
			out.ToolsListCount++
		case "tools/call":
			if entry.Tool != "" && entry.Outcome == "success" {
				seen[entry.Tool] = true
				out.RequiredToolNames = append(out.RequiredToolNames, entry.Tool)
				if entry.Tool == "commit_brief" {
					out.AckWriteCount++
				}
			}
		}
	}
	out.Complete = out.InitializeCount > 0 && out.ToolsListCount > 0
	if out.AckWriteCount > 1 {
		out.DuplicateWriteCount = out.AckWriteCount - 1
	}
	out.ChainComplete = seen["lookup_brief"] && seen["transform_brief"] && seen["commit_brief"] && out.AckWriteCount == 1
	return out, nil
}

func (s *cleanupServer) cleanup(leaseID string) ([]string, error) {
	s.mu.Lock()
	lease := s.leases[leaseID]
	s.mu.Unlock()
	if lease == nil {
		return nil, errors.New("unknown lease")
	}
	// Keep the lease until every idempotent API phase succeeds. A transient
	// failure then remains retryable by the parent instead of orphaning the PAT.
	token := string(lease.token)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out := []string{}
	if !lease.registrationDeleted {
		path := "/api/mcp/servers/" + lease.registrationID + "?scope=user_agent&agent_id=" + url.QueryEscape(lease.agentID)
		if err := s.api(ctx, http.MethodDelete, path, token, nil, nil); err != nil && !isNotFound(err) {
			return out, err
		}
		lease.registrationDeleted = true
	}
	out = append(out, "registration")
	if !lease.agentDeleted {
		if err := s.api(ctx, http.MethodDelete, "/api/agents/"+lease.agentID, token, nil, nil); err != nil && !isNotFound(err) {
			return out, err
		}
		lease.agentDeleted = true
	}
	out = append(out, "agent")
	s.mu.Lock()
	delete(s.leases, leaseID)
	s.mu.Unlock()
	for i := range lease.token {
		lease.token[i] = 0
	}
	return out, nil
}

func isNotFound(err error) bool {
	var e *cleanupHTTPError
	return errors.As(err, &e) && e.status == http.StatusNotFound
}

type cleanupHTTPError struct{ status int }

func (e *cleanupHTTPError) Error() string { return "http" }
func (s *cleanupServer) api(ctx context.Context, method, path, token string, body any, out any) error {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.baseURL+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &cleanupHTTPError{resp.StatusCode}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
