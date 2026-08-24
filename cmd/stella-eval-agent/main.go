// stella-eval-agent drives one Harbor evaluation trial through Stella's public
// HTTP API. It intentionally has no direct database or in-process server access:
// a passing benchmark must exercise the shipped service boundary.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/CherryHQ/stella/internal/config"
)

const (
	exitAdapter                        = 10
	exitProduct                        = 11
	exitTimeout                        = 12
	cleanupMargin                      = 2 * time.Minute
	usageSettleTimeout                 = 30 * time.Second
	usageSettlePoll                    = 100 * time.Millisecond
	specializedCatalogCount            = 53
	specializedFixtureRegistrationName = "harbor-specialized-fixture"
)

type binding struct {
	Socket  string `json:"socket"`
	Nonce   string `json:"nonce"`
	Workdir string `json:"workdir"`
	Home    string `json:"home,omitempty"`
	TempDir string `json:"temp_dir,omitempty"`
	Path    string `json:"path,omitempty"`
}

type result struct {
	SessionID                       string         `json:"session_id,omitempty"`
	AgentID                         string         `json:"agent_id,omitempty"`
	Model                           string         `json:"model,omitempty"`
	CandidateCommit                 string         `json:"candidate_commit,omitempty"`
	UserID                          string         `json:"user_id,omitempty"`
	TaskID                          string         `json:"task_id,omitempty"`
	FixturePlanDigest               string         `json:"fixture_plan_digest,omitempty"`
	TurnTerminalState               string         `json:"turn_terminal_state,omitempty"`
	ToolCalls                       map[string]int `json:"tool_calls"`
	StellaToolCalls                 []toolCall     `json:"stella_tool_calls"`
	TokenCount                      int64          `json:"token_count"`
	ElapsedSec                      float64        `json:"elapsed_sec"`
	BridgeNonce                     string         `json:"bridge_nonce"`
	DisabledToolsCount              int            `json:"disabled_tools_count"`
	ExcludedTools                   []string       `json:"excluded_tools"`
	MCPTools                        []string       `json:"mcp_tools,omitempty"`
	MCPRegistrationID               string         `json:"mcp_registration_id,omitempty"`
	SpecializedCatalogCount         int            `json:"specialized_catalog_count,omitempty"`
	SpecializedCatalogDigest        string         `json:"specialized_catalog_digest,omitempty"`
	RuntimeSpecializedCatalogDigest string         `json:"runtime_specialized_catalog_digest,omitempty"`
	ToolStrategy                    string         `json:"tool_strategy,omitempty"`
	ProviderSurfaceCount            int            `json:"provider_surface_count,omitempty"`
	ProviderSurfaceJSONBytes        int            `json:"provider_surface_json_bytes,omitempty"`
	ProviderSurfaceDigest           string         `json:"provider_surface_digest,omitempty"`
	CapabilityProfileDigest         string         `json:"capability_profile_digest"`
	SandboxBackend                  string         `json:"sandbox_backend,omitempty"`
	TimedOut                        bool           `json:"timed_out"`
	StreamErrors                    []string       `json:"stream_errors,omitempty"`
	StreamEvents                    int            `json:"stream_events"`
	Metrics                         metrics        `json:"metrics"`
	TrajectoryPath                  string         `json:"trajectory_path,omitempty"`
	TrajectoryTruncated             bool           `json:"trajectory_truncated,omitempty"`
	FailureClass                    string         `json:"failure_class,omitempty"`
	// HostVerdict is deliberately kept separate from model trajectory data. The
	// Harbor adapter uploads it only after the model process has exited.
	HostVerdict *hostVerdict `json:"host_verdict,omitempty"`
	Errors      []string     `json:"errors,omitempty"`
}

type hostVerdict struct {
	Version int      `json:"version"`
	TaskID  string   `json:"task_id"`
	Valid   bool     `json:"valid"`
	Reward  int      `json:"reward"`
	Reasons []string `json:"reasons,omitempty"`
	Nonce   string   `json:"nonce"`
}

type toolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	// IsError marks a call that failed. A call that never reached the sandbox
	// leaves no bridge ledger entry by definition, so the evidence predicate
	// must not demand one for it.
	IsError bool `json:"is_error,omitempty"`
}

type fixtureConfig struct {
	Version              int    `json:"version"`
	Authority            string `json:"authority"`
	RouteKey             string `json:"route_key"`
	CleanupSocket        string `json:"cleanup_socket"`
	CatalogDigest        string `json:"catalog_digest"`
	ArticleCanonicalURL  string `json:"article_canonical_url"`
	ArticleTitle         string `json:"article_title"`
	ArticleContentDigest string `json:"article_content_digest"`
	FixturePlanDigest    string `json:"fixture_plan_digest"`
}

type specializedTask string

const (
	taskSkillBashGuard     specializedTask = "skill-bash-guard"
	taskMemoryLibraryShare specializedTask = "memory-library-share"
	taskMCPRecally         specializedTask = "mcp-recally"
	skillArtifactPath                      = "/workspace/report.txt"
	shareArtifactPath                      = "/workspace/evidence.txt"
)

type specializedFixture struct {
	task     specializedTask
	skill    string
	artifact []byte
	memory   string
	library  string
}

type cleanupState struct {
	Lease             string `json:"lease"`
	ProvisionedUserID string `json:"provisioned_user_id"`
}

type apiClient struct {
	baseURL, token string
	http           *http.Client
}
type apiError struct {
	Status int
	Body   string
}

func (e *apiError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body) }

func (c apiClient) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.baseURL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &apiError{resp.StatusCode, strings.TrimSpace(string(b))}
	}
	if out != nil && len(b) != 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

// streamTurn posts the instruction and consumes the SSE turn stream until the
// server closes it. It returns the error events the turn emitted; the caller
// decides whether those are product failures. Any transport error is returned.
func (c apiClient) uploadLibraryFixture(ctx context.Context, agentID, content string) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "evidence.txt")
	if err != nil {
		return err
	}
	if _, err = part.Write([]byte(content + "\n")); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.baseURL, "/")+"/api/library-files?scope=user_agent&agent_id="+url.QueryEscape(agentID), &body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c apiClient) streamTurn(ctx context.Context, agentID, sessionID, instruction string, excludedTools []string) (events int, streamErrors []string, err error) {
	payload := map[string]any{"parts": []map[string]string{{"type": "text", "text": instruction}}}
	if len(excludedTools) > 0 {
		payload["excluded_tools"] = excludedTools
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.baseURL, "/")+"/api/agents/"+agentID+"/sessions/"+sessionID+"/messages", bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return 0, nil, &apiError{resp.StatusCode, strings.TrimSpace(string(b))}
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			break
		}
		events++
		var evt struct {
			Type      string `json:"type"`
			ErrorText string `json:"errorText"`
		}
		if json.Unmarshal([]byte(payload), &evt) == nil && evt.Type == "error" {
			streamErrors = append(streamErrors, evt.ErrorText)
		}
	}
	if err := scanner.Err(); err != nil {
		return events, streamErrors, err
	}
	return events, streamErrors, nil
}

func writeBinding(dir, userID string, b binding) (string, error) {
	if dir == "" || userID == "" || b.Socket == "" || b.Nonce == "" || !strings.HasPrefix(b.Workdir, "/") {
		return "", errors.New("invalid bridge binding")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	target := filepath.Join(dir, userID+".json")
	data, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".binding-*")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", err
	}
	err = tmp.Close()
	if err != nil {
		return "", err
	}
	if err := os.Rename(name, target); err != nil {
		return "", err
	}
	return target, nil
}

// readBridgeArtifact asks the already-authenticated host bridge to fetch an
// exact container file. The evaluator never mounts or guesses a task path; the
// nonce-bound bridge is the same authority used by the agent's core tools.
func readBridgeArtifact(ctx context.Context, b binding, path string) ([]byte, error) {
	if b.Socket == "" || b.Nonce == "" || !strings.HasPrefix(path, "/") {
		return nil, errors.New("invalid bridge artifact request")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", b.Socket)
	if err != nil {
		return nil, fmt.Errorf("connect bridge artifact reader: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if err := json.NewEncoder(conn).Encode(map[string]any{"nonce": b.Nonce, "op": "read_file", "path": path, "verifier": true}); err != nil {
		return nil, fmt.Errorf("write bridge artifact request: %w", err)
	}
	var response struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
		Data string `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(conn, 33<<20)).Decode(&response); err != nil {
		return nil, fmt.Errorf("read bridge artifact response: %w", err)
	}
	if !response.OK || response.Data == "" {
		return nil, fmt.Errorf("bridge artifact read rejected: %s", response.Code)
	}
	data, err := base64.StdEncoding.DecodeString(response.Data)
	if err != nil || len(data) > 32<<20 {
		return nil, errors.New("invalid bridge artifact bytes")
	}
	return data, nil
}

func digestProfile(disabled []string, bundleDigest string) string {
	copy := append([]string(nil), disabled...)
	sortStrings(copy)
	b, _ := json.Marshal(struct {
		Disabled []string `json:"disabled_tools"`
		Bundle   string   `json:"bundle_digest"`
	}{copy, bundleDigest})
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func sortStrings(v []string) {
	for i := range v {
		for j := i + 1; j < len(v); j++ {
			if v[j] < v[i] {
				v[i], v[j] = v[j], v[i]
			}
		}
	}
}

func parseSpecializedTask(value string) (specializedTask, error) {
	switch specializedTask(value) {
	case taskSkillBashGuard, taskMemoryLibraryShare, taskMCPRecally:
		return specializedTask(value), nil
	default:
		return "", errors.New("unknown specialized task identity")
	}
}

func newSpecializedFixture(task specializedTask) (specializedFixture, error) {
	switch task {
	case taskSkillBashGuard:
		// A fresh token makes copying a repository-visible fixture insufficient.
		// It only appears in the private Skill and the driver's in-memory digest.
		raw := make([]byte, 18)
		if _, err := rand.Read(raw); err != nil {
			return specializedFixture{}, fmt.Errorf("generate skill fixture token: %w", err)
		}
		token := base64.RawURLEncoding.EncodeToString(raw)
		artifact := []byte("status=restored\ntoken=" + token + "\n")
		return specializedFixture{task: task, skill: "# Repair report\n\nWrite exactly this file, with no extra bytes:\n\n" + string(artifact), artifact: artifact}, nil
	case taskMemoryLibraryShare:
		return specializedFixture{task: task, memory: "Memory fact: cobalt lantern.", library: "Library fact: amber meadow.", artifact: []byte("memory=cobalt lantern\nlibrary=amber meadow\n")}, nil
	case taskMCPRecally:
		return specializedFixture{task: task}, nil
	default:
		return specializedFixture{}, errors.New("unknown specialized task fixture")
	}
}

func specializedFixtureDigest(task specializedTask) string {
	// Random per-trial skill tokens are capabilities, not fixture-plan identity.
	// Keeping them out of the digest preserves an A/A-compatible plan while the
	// token still prevents a task-image shortcut.
	value := "specialized-tools-v1:" + string(task)
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func parseFixtureConfig(path string) (fixtureConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return fixtureConfig{}, errors.New("read MCP fixture config")
	}
	var cfg fixtureConfig
	if json.Unmarshal(data, &cfg) != nil || cfg.Version != 1 {
		return fixtureConfig{}, errors.New("invalid MCP fixture config")
	}
	u, err := url.Parse(cfg.Authority)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return fixtureConfig{}, errors.New("invalid MCP fixture authority")
	}
	key, err := base64.RawURLEncoding.DecodeString(cfg.RouteKey)
	if err != nil || len(key) != 32 || cfg.CleanupSocket == "" || !strings.HasSuffix(cfg.CleanupSocket, ".sock") {
		return fixtureConfig{}, errors.New("invalid MCP fixture route key")
	}
	if cfg.CatalogDigest == "" || cfg.ArticleCanonicalURL == "" || cfg.ArticleTitle == "" || !strings.HasPrefix(cfg.ArticleContentDigest, "sha256:") || !strings.HasPrefix(cfg.FixturePlanDigest, "sha256:") {
		return fixtureConfig{}, errors.New("invalid MCP fixture plan")
	}
	return cfg, nil
}

func fixtureRouteForTrial(routeKey, trial string) (string, error) {
	key, err := base64.RawURLEncoding.DecodeString(routeKey)
	if err != nil || len(key) != 32 || len(trial) == 0 || len(trial) > 64 {
		return "", errors.New("invalid MCP fixture route")
	}
	payload := append([]byte{byte(len(trial))}, []byte(trial)...)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	payload = append(payload, mac.Sum(nil)[:16]...)
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func claimCleanupLease(ctx context.Context, socket string, claim map[string]string) (string, error) {
	conn, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		return "", errors.New("connect cleanup lease")
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if err := json.NewEncoder(conn).Encode(claim); err != nil {
		return "", errors.New("write cleanup lease")
	}
	var response struct {
		Lease string `json:"lease"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(conn, 32<<10)).Decode(&response); err != nil || response.Lease == "" || response.Error != "" {
		return "", errors.New("claim cleanup lease")
	}
	return response.Lease, nil
}

type fixtureInspection struct {
	Version             int  `json:"version"`
	Complete            bool `json:"complete"`
	CatalogCount        int  `json:"catalog_count"`
	InitializeCount     int  `json:"initialize_count"`
	ToolsListCount      int  `json:"tools_list_count"`
	AckWriteCount       int  `json:"ack_write_count"`
	DuplicateWriteCount int  `json:"duplicate_write_count"`
	ChainComplete       bool `json:"chain_complete"`
}

func inspectCleanupLease(ctx context.Context, socket, lease string) (fixtureInspection, error) {
	var out struct {
		Inspect *fixtureInspection `json:"inspect"`
		Error   string             `json:"error"`
	}
	conn, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		return fixtureInspection{}, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if err = json.NewEncoder(conn).Encode(map[string]string{"action": "inspect", "lease": lease}); err != nil {
		return fixtureInspection{}, err
	}
	if err = json.NewDecoder(io.LimitReader(conn, 32<<10)).Decode(&out); err != nil || out.Error != "" || out.Inspect == nil {
		return fixtureInspection{}, errors.New("invalid fixture inspection")
	}
	return *out.Inspect, nil
}

func writeCleanupState(path string, state cleanupState) error {
	if path == "" {
		return errors.New("cleanup state path is required")
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func digestNames(names []string) string {
	copy := append([]string(nil), names...)
	sortStrings(copy)
	s := sha256.Sum256([]byte(strings.Join(copy, "\n")))
	return "sha256:" + hex.EncodeToString(s[:])
}

func parseExcludedTools(value string) []string {
	seen := make(map[string]struct{})
	tools := make([]string, 0)
	for name := range strings.SplitSeq(value, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		tools = append(tools, name)
	}
	sortStrings(tools)
	return tools
}

func waitForTerminal(ctx context.Context, c apiClient, agentID, sessionID string) (string, error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		var detail struct {
			ActivityStatus string `json:"activity_status"`
		}
		if err := c.call(ctx, http.MethodGet, "/api/agents/"+agentID+"/sessions/"+sessionID, nil, &detail); err != nil {
			return "", err
		}
		if detail.ActivityStatus != "working" {
			if detail.ActivityStatus == "error" {
				return "errored", nil
			}
			return "completed", nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

// stopAndConfirm is deliberately separate from SSE handling: closing a stream
// only detaches its observer, while this endpoint cancels the admitted turn.
// stopConfirmBudget bounds how long a trial that hit its deadline waits for the
// session to actually stop. Stop cannot land while a tool call is in flight, so
// a budget shorter than the longest tool timeout voids the trial instead of
// scoring it: the driver never confirms a terminal state, and confirming one
// before the verifier runs is what keeps the agent from contaminating
// verification. Deliberate ceiling; raise it if tasks start using tool timeouts
// longer than this.
//
// The budget is spent inside the caller's wall clock, never on top of it: the
// caller subtracts it from the trial limit before passing --deadline-seconds,
// because only the caller knows that limit. A confirmation that ran past the
// wall used to cost the whole trial, evidence included, so --stop-confirm-
// seconds lets the two numbers be derived from one place. This constant is the
// fallback for a caller that does not set it.
const stopConfirmBudget = 3 * time.Minute

// finishTimedOut ends a trial that ran out of working time. Stop is confirmed
// before any evidence is read, because a turn still running while the verifier
// works would score the wrong environment. An unconfirmed stop stays fail-closed
// as an adapter fault, but the evidence is exported either way: a voided trial
// with no trajectory is unreadable, and the export is read-only, so it cannot
// make an already bad state worse.
func finishTimedOut(user apiClient, r *result, trajectory string, phase func(*int64), confirmBudget time.Duration) int {
	r.TimedOut = true
	terminalCtx, terminalCancel := context.WithTimeout(context.Background(), confirmBudget)
	waitErr := stopAndConfirm(terminalCtx, user, r.AgentID, r.SessionID)
	terminalCancel()
	phase(&r.Metrics.Timing.StopMs)
	if waitErr == nil {
		r.TurnTerminalState = "stopped"
	}
	_ = collectEvidence(context.Background(), user, r.AgentID, r.SessionID, trajectory, r)
	phase(&r.Metrics.Timing.ExportMs)
	if waitErr != nil {
		r.Errors = append(r.Errors, "confirm terminal after timeout: "+waitErr.Error())
		r.FailureClass = "adapter"
		return exitAdapter
	}
	return exitTimeout
}

func stopAndConfirm(ctx context.Context, c apiClient, agentID, sessionID string) error {
	if err := c.call(ctx, http.MethodPost, "/api/agents/"+agentID+"/sessions/"+sessionID+"/stop", nil, nil); err != nil {
		return err
	}
	_, err := waitForTerminal(ctx, c, agentID, sessionID)
	return err
}

// messageLimit caps one page of history. A trajectory that hits it is recorded
// as truncated rather than quietly shortened, because a partial trajectory that
// looks whole would mislabel a failure downstream.
const messageLimit = 500

func loadSessionMessages(ctx context.Context, c apiClient, agentID, sessionID string) (json.RawMessage, []sessionMessage, error) {
	var raw json.RawMessage
	if err := c.call(ctx, http.MethodGet, fmt.Sprintf("/api/agents/%s/sessions/%s/messages?limit=%d", agentID, sessionID, messageLimit), nil, &raw); err != nil {
		return nil, nil, err
	}
	var messages struct {
		Messages []sessionMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &messages); err != nil {
		return nil, nil, err
	}
	return raw, messages.Messages, nil
}

func collectEvidence(ctx context.Context, c apiClient, agentID, sessionID, trajectoryPath string, out *result) error {
	// Captured verbatim: the trajectory is the artifact a failure taxonomy and a
	// public log are built from, so re-marshalling our own structs would silently
	// drop every field this driver does not happen to model.
	raw, messages, err := loadSessionMessages(ctx, c, agentID, sessionID)
	if err != nil {
		return err
	}
	if trajectoryPath != "" && len(raw) != 0 {
		if err := os.WriteFile(trajectoryPath, raw, 0o600); err != nil {
			return err
		}
		out.TrajectoryPath = trajectoryPath
		out.TrajectoryTruncated = len(messages) >= messageLimit
	}
	m, calls := deriveMetrics(messages)
	// Best effort: a deployment that predates the usage API still produces a
	// valid trial, it just cannot report cost. Failing the trial over a missing
	// optional metric would be worse than reporting it as absent.
	if u, err := collectUsage(ctx, c, agentID, sessionID); err == nil && u != nil {
		m.Usage = u
	} else if err != nil {
		return fmt.Errorf("collect session usage: %w", err)
	}
	// Preserve the timing the driver measured itself; deriveMetrics only knows
	// what the message timeline shows.
	m.Timing.TotalMs, m.Timing.ProvisionMs = out.Metrics.Timing.TotalMs, out.Metrics.Timing.ProvisionMs
	m.Timing.SetupMs, m.Timing.TurnMs = out.Metrics.Timing.SetupMs, out.Metrics.Timing.TurnMs
	m.Timing.StopMs, m.Timing.ExportMs = out.Metrics.Timing.StopMs, out.Metrics.Timing.ExportMs
	out.Metrics = m
	out.StellaToolCalls = calls
	out.TokenCount = m.Tokens.Total
	for _, name := range toolNames(m.Tools) {
		out.ToolCalls[name] = m.Tools[name].Calls
	}
	return nil
}

func seedSpecializedFixtures(ctx context.Context, user apiClient, agentID string, fixture specializedFixture) error {
	// Values remain outside the task image and prompt. Task identity decides
	// which fixture exists, so a task cannot earn a verdict from another task's
	// preparatory state.
	switch fixture.task {
	case taskSkillBashGuard:
		return user.call(ctx, http.MethodPost, "/api/agents/"+agentID+"/skills", map[string]any{
			"name": "repair-report", "scope": "user_agent", "description": "Private repair guidance",
			"files": map[string]string{"SKILL.md": fixture.skill},
		}, nil)
	case taskMemoryLibraryShare:
		if err := user.call(ctx, http.MethodPost, "/api/users/me/memories/"+agentID+"/knowledge", map[string]any{"content": fixture.memory}, nil); err != nil {
			return fmt.Errorf("seed memory: %w", err)
		}
		if err := user.uploadLibraryFixture(ctx, agentID, fixture.library); err != nil {
			return fmt.Errorf("seed library: %w", err)
		}
		return nil
	case taskMCPRecally:
		// A pre-turn distractor proves that the verifier filters on the exact
		// fixture plan and turn boundary, not merely any Recally row.
		return user.call(ctx, http.MethodPost, "/api/recally/articles", map[string]any{
			"url": "https://fixture.invalid/distractor", "title": "Distractor", "content": "not the required article",
		}, nil)
	}
	return errors.New("unknown specialized fixture")
}

func verifySeedFixtures(ctx context.Context, user apiClient, agentID string, fixture specializedFixture) error {
	switch fixture.task {
	case taskSkillBashGuard:
		var skills struct {
			Skills []struct {
				Name string `json:"name"`
			} `json:"skills"`
		}
		if err := user.call(ctx, http.MethodGet, "/api/agents/"+agentID+"/skills?scope=user_agent&q=repair-report", nil, &skills); err != nil || len(skills.Skills) != 1 || skills.Skills[0].Name != "repair-report" {
			return errors.New("verify skill fixture")
		}
	case taskMemoryLibraryShare:
		var knowledge struct {
			Knowledge []struct {
				Content string `json:"content"`
			} `json:"knowledge"`
		}
		if err := user.call(ctx, http.MethodGet, "/api/users/me/memories/"+agentID+"/knowledge?state=active", nil, &knowledge); err != nil {
			return errors.New("verify memory fixture")
		}
		found := false
		for _, item := range knowledge.Knowledge {
			found = found || item.Content == fixture.memory
		}
		if !found {
			return errors.New("verify memory fixture")
		}
		var library struct {
			LibraryFiles []struct {
				Name string `json:"name"`
			} `json:"library_files"`
		}
		if err := user.call(ctx, http.MethodGet, "/api/library-files?scope=user_agent&agent_id="+url.QueryEscape(agentID)+"&q=evidence", nil, &library); err != nil || len(library.LibraryFiles) != 1 {
			return errors.New("verify library fixture")
		}
	case taskMCPRecally:
		return nil
	}
	return errors.New("unknown specialized fixture")
}

func businessFailure(nonce, reason string) hostVerdict {
	return hostVerdict{Version: 1, Valid: true, Reward: 0, Reasons: []string{reason}, Nonce: nonce}
}

func artifactBusinessFailure(err error) bool {
	return strings.HasSuffix(err.Error(), ": not_found") || strings.HasSuffix(err.Error(), ": is_dir")
}

func publicShareToken(messages []sessionMessage) (string, error) {
	var urls []string
	for _, message := range messages {
		if message.Role != "tool" || !strings.HasPrefix(message.ToolName, "share") || message.IsError {
			continue
		}
		var response struct {
			URL string `json:"url"`
		}
		if json.Unmarshal([]byte(message.Content), &response) == nil && response.URL != "" {
			urls = append(urls, response.URL)
		}
	}
	if len(urls) != 1 {
		return "", errors.New("share tool response is missing or duplicated")
	}
	u, err := url.Parse(urls[0])
	if err != nil || !strings.HasPrefix(u.Path, "/s/") || strings.Count(strings.TrimPrefix(u.Path, "/s/"), "/") != 0 {
		return "", errors.New("share tool response URL is invalid")
	}
	token := strings.TrimPrefix(u.Path, "/s/")
	if token == "" {
		return "", errors.New("share tool response token is empty")
	}
	return token, nil
}

func publicShareContent(ctx context.Context, c apiClient, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.baseURL, "/")+"/api/shares/public/"+url.PathEscape(token), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &apiError{resp.StatusCode, strings.TrimSpace(string(body))}
	}
	return body, nil
}

func sha256Digest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func verifySkillBashGuard(ctx context.Context, b binding, fixture specializedFixture) (hostVerdict, error) {
	artifact, err := readBridgeArtifact(ctx, b, skillArtifactPath)
	if err != nil {
		if artifactBusinessFailure(err) {
			return hostVerdict{Version: 1, Valid: true, Reward: 0, Reasons: []string{"required artifact is missing"}, Nonce: b.Nonce}, nil
		}
		return hostVerdict{}, err
	}
	if !bytes.Equal(artifact, fixture.artifact) {
		return hostVerdict{Version: 1, Valid: true, Reward: 0, Reasons: []string{"artifact content does not match private Skill"}, Nonce: b.Nonce}, nil
	}
	return hostVerdict{Version: 1, Valid: true, Reward: 1, Nonce: b.Nonce}, nil
}

func verifyMemoryLibraryShare(ctx context.Context, user apiClient, b binding, agentID, sessionID string, turnStarted time.Time, fixture specializedFixture) (hostVerdict, error) {
	artifact, err := readBridgeArtifact(ctx, b, shareArtifactPath)
	if err != nil {
		if artifactBusinessFailure(err) {
			return hostVerdict{Version: 1, Valid: true, Reward: 0, Reasons: []string{"required evidence artifact is missing"}, Nonce: b.Nonce}, nil
		}
		return hostVerdict{}, err
	}
	if !bytes.Equal(artifact, fixture.artifact) {
		return hostVerdict{Version: 1, Valid: true, Reward: 0, Reasons: []string{"evidence artifact does not contain the canonical facts"}, Nonce: b.Nonce}, nil
	}
	var listed struct {
		Shares []struct {
			ID        string    `json:"id"`
			Title     string    `json:"title"`
			MediaType string    `json:"media_type"`
			CreatedAt time.Time `json:"created_at"`
		} `json:"shares"`
	}
	if err := user.call(ctx, http.MethodGet, "/api/shares?page_size=100", nil, &listed); err != nil {
		return hostVerdict{}, fmt.Errorf("list shares: %w", err)
	}
	if len(listed.Shares) != 1 || listed.Shares[0].Title != "evidence.txt" || listed.Shares[0].MediaType != "text/plain; charset=utf-8" || listed.Shares[0].CreatedAt.Before(turnStarted) {
		return hostVerdict{Version: 1, Valid: true, Reward: 0, Reasons: []string{"expected one current-turn evidence share"}, Nonce: b.Nonce}, nil
	}
	_, messages, err := loadSessionMessages(ctx, user, agentID, sessionID)
	if err != nil {
		return hostVerdict{}, fmt.Errorf("read share evidence: %w", err)
	}
	token, tokenErr := publicShareToken(messages)
	if tokenErr != nil {
		//nolint:nilerr // A missing share tool result is task behavior, not a broken host API.
		return businessFailure(b.Nonce, "expected one share tool result"), nil
	}
	public, err := publicShareContent(ctx, user, token)
	if err != nil {
		return hostVerdict{}, fmt.Errorf("read public share: %w", err)
	}
	if sha256Digest(public) != sha256Digest(artifact) {
		return hostVerdict{Version: 1, Valid: true, Reward: 0, Reasons: []string{"public share bytes do not match evidence artifact"}, Nonce: b.Nonce}, nil
	}
	return hostVerdict{Version: 1, Valid: true, Reward: 1, Nonce: b.Nonce}, nil
}

func fixtureCatalogMatches(count int, digest string, plan fixtureConfig) bool {
	return count == specializedCatalogCount && digest == plan.CatalogDigest
}

func verifyMCPRecally(ctx context.Context, user apiClient, b binding, turnStarted time.Time, plan fixtureConfig, inspection fixtureInspection) (hostVerdict, error) {
	if inspection.Version != 1 || !inspection.Complete || inspection.CatalogCount != specializedCatalogCount {
		return hostVerdict{}, errors.New("inspect MCP fixture ledger")
	}
	if !inspection.ChainComplete || inspection.AckWriteCount != 1 || inspection.DuplicateWriteCount != 0 {
		return hostVerdict{Version: 1, Valid: true, Reward: 0, Reasons: []string{"MCP chain is incomplete or duplicated"}, Nonce: b.Nonce}, nil
	}
	var list struct {
		Articles []struct {
			ID           string    `json:"id"`
			CanonicalURL string    `json:"canonical_url"`
			Title        string    `json:"title"`
			CreatedAt    time.Time `json:"created_at"`
		} `json:"articles"`
	}
	if err := user.call(ctx, http.MethodGet, "/api/recally/articles?canonical_url="+url.QueryEscape(plan.ArticleCanonicalURL), nil, &list); err != nil {
		return hostVerdict{}, fmt.Errorf("list Recally articles: %w", err)
	}
	if len(list.Articles) != 1 || list.Articles[0].CanonicalURL != plan.ArticleCanonicalURL || list.Articles[0].Title != plan.ArticleTitle || list.Articles[0].CreatedAt.Before(turnStarted) {
		return hostVerdict{Version: 1, Valid: true, Reward: 0, Reasons: []string{"expected one current-turn canonical Recally article"}, Nonce: b.Nonce}, nil
	}
	var article struct {
		Content string `json:"content"`
	}
	if err := user.call(ctx, http.MethodGet, "/api/recally/articles/"+url.PathEscape(list.Articles[0].ID)+"?include=content", nil, &article); err != nil {
		return hostVerdict{}, fmt.Errorf("read Recally article: %w", err)
	}
	if sha256Digest([]byte(article.Content)) != plan.ArticleContentDigest {
		return hostVerdict{Version: 1, Valid: true, Reward: 0, Reasons: []string{"Recally article content digest mismatch"}, Nonce: b.Nonce}, nil
	}
	return hostVerdict{Version: 1, Valid: true, Reward: 1, Nonce: b.Nonce}, nil
}

func collectRuntimeSurface(ctx context.Context, c apiClient, agentID, sessionID string, out *result) error {
	var detail struct {
		ToolSurface *struct {
			Strategy string `json:"strategy"`
			Tools    []struct {
				Name        string         `json:"name"`
				Description string         `json:"description"`
				InputSchema map[string]any `json:"input_schema"`
			} `json:"tools"`
		} `json:"tool_surface"`
	}
	if err := c.call(ctx, http.MethodGet, "/api/agents/"+agentID+"/sessions/"+sessionID, nil, &detail); err != nil {
		return err
	}
	if detail.ToolSurface == nil || detail.ToolSurface.Strategy != "native" || len(detail.ToolSurface.Tools) == 0 {
		return errors.New("runtime tool surface is unavailable")
	}
	raw, err := json.Marshal(detail.ToolSurface.Tools)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	out.ToolStrategy = detail.ToolSurface.Strategy
	out.ProviderSurfaceCount = len(detail.ToolSurface.Tools)
	out.ProviderSurfaceJSONBytes = len(raw)
	out.ProviderSurfaceDigest = "sha256:" + hex.EncodeToString(sum[:])
	if out.MCPRegistrationID != "" {
		specialized := make([]any, 0)
		names := make([]string, 0)
		for _, tool := range detail.ToolSurface.Tools {
			if strings.HasPrefix(tool.Name, "mcp__") {
				specialized = append(specialized, tool)
				names = append(names, tool.Name)
			}
		}
		if len(specialized) == 0 {
			return errors.New("runtime specialized catalog is unavailable")
		}
		specializedRaw, err := json.Marshal(specialized)
		if err != nil {
			return err
		}
		specializedSum := sha256.Sum256(specializedRaw)
		out.SpecializedCatalogCount = len(specialized)
		out.SpecializedCatalogDigest = digestNames(names)
		out.RuntimeSpecializedCatalogDigest = "sha256:" + hex.EncodeToString(specializedSum[:])
	}
	return nil
}

func collectUsage(ctx context.Context, c apiClient, agentID, sessionID string) (*usage, error) {
	ctx, cancel := context.WithTimeout(ctx, usageSettleTimeout)
	defer cancel()
	path := "/api/agents/" + agentID + "/sessions/" + sessionID + "/usage"
	for {
		var u usage
		if err := c.call(ctx, http.MethodGet, path, nil, &u); err != nil {
			// Usage did not exist before this evaluation adapter. Keep old servers
			// usable, but never treat an explicit in-flight result as final.
			var apiErr *apiError
			if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
				return nil, nil
			}
			return nil, err
		}
		if u.PendingCallCount == nil || *u.PendingCallCount == 0 {
			return &u, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("usage persistence did not settle: %w", ctx.Err())
		case <-time.After(usageSettlePoll):
		}
	}
}

func gitRevParseHead() string {
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func run() int {
	var baseURL, instructionFile, bindingFile, bindingDir, model, output, externalID, taskID, bundleDigest, trajectory, excludedToolsCSV, fixtureConfigPath, cleanupStatePath string
	var deadlineSec int
	var stopConfirmSec int
	flag.StringVar(&baseURL, "stella-url", "", "Stella base URL")
	flag.StringVar(&instructionFile, "instruction-file", "", "task instruction file")
	flag.StringVar(&bindingFile, "binding-template", "", "Bridge binding template JSON")
	flag.StringVar(&bindingDir, "binding-dir", "", "directory read by stellad")
	flag.StringVar(&model, "model", "", "Stella provider/model")
	flag.StringVar(&output, "output", "", "result JSON path, stdout when empty")
	flag.StringVar(&externalID, "user-id", "", "unique Harbor trial identifier")
	flag.StringVar(&taskID, "task-id", "", "Harbor task identity for specialized fixture dispatch")
	flag.StringVar(&bundleDigest, "bundle-digest", "", "helper bundle SHA-256")
	flag.StringVar(&trajectory, "trajectory", "", "write the verbatim message history here")
	flag.StringVar(&excludedToolsCSV, "excluded-tools", "", "comma-separated tool names to hide for this run")
	flag.StringVar(&fixtureConfigPath, "mcp-fixture-config", "", "mode-0600 host-only testbed MCP fixture config")
	flag.StringVar(&cleanupStatePath, "cleanup-state", "", "mode-0600 host-only cleanup lease state")
	flag.IntVar(&deadlineSec, "deadline-seconds", 0, "working time in seconds, excluding the stop confirmation that follows it")
	flag.IntVar(&stopConfirmSec, "stop-confirm-seconds", 0, "seconds allowed to confirm the session stopped after the deadline; must fit inside the caller's trial limit")
	flag.Parse()
	r := result{
		ToolCalls:       map[string]int{},
		Model:           model,
		CandidateCommit: gitRevParseHead(),
		ExcludedTools:   parseExcludedTools(excludedToolsCSV),
	}
	start := time.Now()
	// Phase boundaries are measured here rather than inferred from the message
	// timeline: a reviewer needs to see whether a slow trial was the model, a
	// tool, or Stella's own setup.
	mark := start
	// Accumulates, so a phase entered twice (the turn stream, then the terminal
	// wait) reports its total instead of only the last leg.
	phase := func(into *int64) {
		now := time.Now()
		*into += now.Sub(mark).Milliseconds()
		mark = now
	}
	defer func() {
		r.ElapsedSec = time.Since(start).Seconds()
		r.Metrics.Timing.TotalMs = time.Since(start).Milliseconds()
		data, _ := json.MarshalIndent(r, "", "  ")
		if output == "" {
			fmt.Println(string(data))
		} else if err := os.WriteFile(output, append(data, '\n'), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "write result:", err)
		}
	}()
	if baseURL == "" || instructionFile == "" || bindingFile == "" || bindingDir == "" || model == "" || externalID == "" || deadlineSec <= 0 {
		r.Errors = append(r.Errors, "required flags missing")
		r.FailureClass = "adapter"
		return exitAdapter
	}
	adminToken := os.Getenv("STELLA_EVAL_ADMIN_TOKEN")
	if adminToken == "" {
		r.Errors = append(r.Errors, "admin token environment variable is empty")
		r.FailureClass = "adapter"
		return exitAdapter
	}
	instruction, err := os.ReadFile(instructionFile)
	if err != nil {
		r.Errors = append(r.Errors, err.Error())
		r.FailureClass = "adapter"
		return exitAdapter
	}
	var b binding
	raw, err := os.ReadFile(bindingFile)
	if err != nil || json.Unmarshal(raw, &b) != nil {
		r.Errors = append(r.Errors, "read binding template")
		r.FailureClass = "adapter"
		return exitAdapter
	}
	r.BridgeNonce = b.Nonce
	deadline := time.Now().UTC().Add(time.Duration(deadlineSec) * time.Second)
	confirmBudget := stopConfirmBudget
	if stopConfirmSec > 0 {
		confirmBudget = time.Duration(stopConfirmSec) * time.Second
	}
	admin := apiClient{baseURL, adminToken, &http.Client{Timeout: 30 * time.Second}}
	ctx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stopSignals()
	// Refuse before provisioning anything. On any other backend the agent's
	// commands run somewhere that is not the trial container, and the bridge
	// ledger can only prove that after they have already run. An older server
	// that does not report the field is refused too: unknown is not bridge.
	var status struct {
		SandboxBackend string `json:"sandbox_backend"`
	}
	if err := admin.call(ctx, http.MethodGet, "/api/status", nil, &status); err != nil {
		r.Errors = append(r.Errors, "read server status: "+err.Error())
		r.FailureClass = "adapter"
		return exitAdapter
	}
	r.SandboxBackend = status.SandboxBackend
	if status.SandboxBackend != config.SandboxBackendBridge {
		r.Errors = append(r.Errors, fmt.Sprintf("sandbox backend is %q, want %q: tools would not run in the trial container", status.SandboxBackend, config.SandboxBackendBridge))
		r.FailureClass = "adapter"
		return exitAdapter
	}
	var provision struct {
		ProvisionedUser struct {
			ID string `json:"id"`
		} `json:"provisioned_user"`
		Token string `json:"token"`
	}
	err = admin.call(ctx, http.MethodPost, "/api/provisioned-users", map[string]any{"external_id": externalID, "email": externalID + "@eval.invalid", "name": "Harbor evaluation", "token_name": "harbor-eval", "expires_at": deadline.Add(cleanupMargin).Format(time.RFC3339)}, &provision)
	if err != nil {
		r.Errors = append(r.Errors, "provision user: "+err.Error())
		r.FailureClass = "adapter"
		return exitAdapter
	}
	// ProvisionedUser.id is the provisioning record, not the account. The
	// bridge backend keys bindings by the account id that sessions carry, so
	// resolve it through the token itself.
	var identity struct {
		ID string `json:"id"`
	}
	if err = (apiClient{baseURL, provision.Token, &http.Client{Timeout: 15 * time.Second}}).call(ctx, http.MethodGet, "/api/auth/me", nil, &identity); err != nil || identity.ID == "" {
		r.Errors = append(r.Errors, "resolve provisioned account: "+fmt.Sprint(err))
		r.FailureClass = "adapter"
		_ = admin.call(context.Background(), http.MethodPost, "/api/provisioned-users/"+provision.ProvisionedUser.ID+"/deactivate", nil, nil)
		return exitAdapter
	}
	r.UserID = identity.ID
	phase(&r.Metrics.Timing.ProvisionMs)
	bindingPath, err := writeBinding(bindingDir, r.UserID, b)
	if err != nil {
		r.Errors = append(r.Errors, "write binding: "+err.Error())
		r.FailureClass = "adapter"
		return exitAdapter
	}
	defer func() { _ = os.Remove(bindingPath) }()
	user := apiClient{baseURL, provision.Token, &http.Client{Timeout: 45 * time.Second}}
	streamUser := apiClient{baseURL, provision.Token, &http.Client{}}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if r.MCPRegistrationID != "" && r.AgentID != "" {
			path := "/api/mcp/servers/" + r.MCPRegistrationID + "?scope=user_agent&agent_id=" + url.QueryEscape(r.AgentID)
			if cleanupErr := user.call(cleanupCtx, http.MethodDelete, path, nil, nil); cleanupErr != nil {
				r.Errors = append(r.Errors, "cleanup MCP registration: "+cleanupErr.Error())
			}
		}
		if r.AgentID != "" {
			if cleanupErr := user.call(cleanupCtx, http.MethodDelete, "/api/agents/"+r.AgentID, nil, nil); cleanupErr != nil {
				r.Errors = append(r.Errors, "cleanup agent: "+cleanupErr.Error())
			}
		}
		if cleanupErr := admin.call(cleanupCtx, http.MethodPost, "/api/provisioned-users/"+provision.ProvisionedUser.ID+"/deactivate", nil, nil); cleanupErr != nil {
			r.Errors = append(r.Errors, "cleanup provisioned user: "+cleanupErr.Error())
		}
	}()
	var task specializedTask
	var taskFixture specializedFixture
	var fixture *fixtureConfig
	if fixtureConfigPath != "" {
		var taskErr error
		task, taskErr = parseSpecializedTask(taskID)
		if taskErr != nil {
			r.Errors = append(r.Errors, taskErr.Error())
			r.FailureClass = "adapter"
			return exitAdapter
		}
		taskFixture, taskErr = newSpecializedFixture(task)
		if taskErr != nil {
			r.Errors = append(r.Errors, taskErr.Error())
			r.FailureClass = "adapter"
			return exitAdapter
		}
		r.TaskID = string(task)
		r.FixturePlanDigest = specializedFixtureDigest(task)
		if task == taskMCPRecally {
			loaded, fixtureErr := parseFixtureConfig(fixtureConfigPath)
			if fixtureErr != nil {
				r.Errors = append(r.Errors, fixtureErr.Error())
				r.FailureClass = "adapter"
				return exitAdapter
			}
			fixture = &loaded
			r.FixturePlanDigest = loaded.FixturePlanDigest
		}
	}
	var agent struct {
		ID string `json:"id"`
	}
	err = user.call(ctx, http.MethodPost, "/api/agents", map[string]any{"name": "harbor-eval-" + externalID, "model": model, "scope": "restricted", "enabled": true}, &agent)
	if err != nil {
		r.Errors = append(r.Errors, "create agent: "+err.Error())
		r.FailureClass = "adapter"
		return exitAdapter
	}
	r.AgentID = agent.ID
	if task != "" {
		if err = seedSpecializedFixtures(ctx, user, r.AgentID, taskFixture); err != nil {
			r.Errors = append(r.Errors, "seed specialized fixtures: "+err.Error())
			r.FailureClass = "adapter"
			return exitAdapter
		}
		if err = verifySeedFixtures(ctx, user, r.AgentID, taskFixture); err != nil {
			r.Errors = append(r.Errors, err.Error())
			r.FailureClass = "adapter"
			return exitAdapter
		}
	}
	var cleanupLease string
	if fixture != nil {
		route, routeErr := fixtureRouteForTrial(fixture.RouteKey, externalID)
		if routeErr != nil {
			r.Errors = append(r.Errors, "generate MCP fixture route")
			r.FailureClass = "adapter"
			return exitAdapter
		}
		var registration struct {
			ID string `json:"id"`
		}
		if err = user.call(ctx, http.MethodPost, "/api/mcp/servers", map[string]any{
			"scope": "user_agent", "agent_id": r.AgentID, "name": "harbor-specialized-fixture",
			"url": fixture.Authority + "/mcp/" + route, "transport": "streamable_http", "auth_type": "none",
		}, &registration); err != nil || registration.ID == "" {
			r.Errors = append(r.Errors, "register MCP fixture")
			r.FailureClass = "adapter"
			return exitAdapter
		}
		r.MCPRegistrationID = registration.ID
		lease, leaseErr := claimCleanupLease(ctx, fixture.CleanupSocket, map[string]string{
			"action": "claim", "token": provision.Token, "trial": externalID, "user_id": r.UserID,
			"agent_id": r.AgentID, "registration_id": r.MCPRegistrationID,
		})
		cleanupLease = lease
		if leaseErr != nil || writeCleanupState(cleanupStatePath, cleanupState{Lease: lease, ProvisionedUserID: provision.ProvisionedUser.ID}) != nil {
			r.Errors = append(r.Errors, "publish cleanup lease")
			r.FailureClass = "adapter"
			return exitAdapter
		}
	}
	var tools struct {
		Tools []struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
			Source  string `json:"source"`
		} `json:"tools"`
	}
	if err = user.call(ctx, http.MethodGet, "/api/agents/"+r.AgentID+"/tools", nil, &tools); err != nil {
		r.Errors = append(r.Errors, "list tools: "+err.Error())
		r.FailureClass = "adapter"
		return exitAdapter
	}
	// MCP tools bypass the sandbox Session and cannot be turned off: the tool
	// list reports them as always enabled with no override. An evaluation
	// instance must therefore have no MCP servers configured, and a run that
	// finds one is void rather than a score with an unknown capability set.
	for _, tool := range tools.Tools {
		if tool.Source == "mcp" {
			r.MCPTools = append(r.MCPTools, tool.Name)
		}
	}
	if task != taskMCPRecally && len(r.MCPTools) > 0 {
		r.Errors = append(r.Errors, "evaluation instance exposes MCP tools that cannot be disabled: "+strings.Join(r.MCPTools, ", "))
		r.FailureClass = "adapter"
		return exitAdapter
	}
	if task == taskMCPRecally && (len(r.MCPTools) != 1 || r.MCPTools[0] != specializedFixtureRegistrationName) {
		// The registration inventory exposes servers, not discovered definitions.
		// Catalog authority is checked only after the runner reports its actual
		// provider surface below.
		r.Errors = append(r.Errors, "MCP fixture registration inventory mismatch")
		r.FailureClass = "adapter"
		return exitAdapter
	}
	disabled := []string{}
	for _, tool := range tools.Tools {
		if tool.Source != "core" && tool.Enabled {
			if err = user.call(ctx, http.MethodPatch, "/api/agents/"+r.AgentID+"/tools/"+tool.Name, map[string]any{"enabled": false, "scope": "user_agent"}, nil); err != nil {
				r.Errors = append(r.Errors, "disable tool "+tool.Name+": "+err.Error())
				r.FailureClass = "adapter"
				return exitAdapter
			}
			disabled = append(disabled, tool.Name)
		}
	}
	r.DisabledToolsCount = len(disabled)
	r.CapabilityProfileDigest = digestProfile(disabled, bundleDigest)
	var session struct {
		ID string `json:"id"`
	}
	if err = user.call(ctx, http.MethodPost, "/api/agents/"+r.AgentID+"/sessions", map[string]any{"kind": "chat"}, &session); err != nil {
		r.Errors = append(r.Errors, "create session: "+err.Error())
		r.FailureClass = "adapter"
		return exitAdapter
	}
	r.SessionID = session.ID
	phase(&r.Metrics.Timing.SetupMs)
	turnStarted := time.Now().UTC()
	turnCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	r.StreamEvents, r.StreamErrors, err = streamUser.streamTurn(turnCtx, r.AgentID, r.SessionID, string(instruction), r.ExcludedTools)
	phase(&r.Metrics.Timing.TurnMs)
	if errors.Is(err, context.DeadlineExceeded) {
		return finishTimedOut(user, &r, trajectory, phase, confirmBudget)
	}
	if err != nil {
		r.Errors = append(r.Errors, "send instruction: "+err.Error())
		r.FailureClass = "product"
		_ = collectEvidence(context.Background(), user, r.AgentID, r.SessionID, trajectory, &r)
		phase(&r.Metrics.Timing.ExportMs)
		return exitProduct
	}
	state, err := waitForTerminal(turnCtx, user, r.AgentID, r.SessionID)
	phase(&r.Metrics.Timing.TurnMs)
	if errors.Is(err, context.DeadlineExceeded) {
		return finishTimedOut(user, &r, trajectory, phase, confirmBudget)
	}
	if err != nil {
		r.Errors = append(r.Errors, "wait terminal: "+err.Error())
		r.FailureClass = "adapter"
		return exitAdapter
	}
	r.TurnTerminalState = state
	if task != "" {
		if err = collectRuntimeSurface(context.Background(), user, r.AgentID, r.SessionID, &r); err != nil {
			r.Errors = append(r.Errors, "collect runtime tool surface: "+err.Error())
			r.FailureClass = "adapter"
			return exitAdapter
		}
		if task == taskMCPRecally && (!fixtureCatalogMatches(r.SpecializedCatalogCount, r.SpecializedCatalogDigest, *fixture) || r.RuntimeSpecializedCatalogDigest == "") {
			r.Errors = append(r.Errors, "runtime MCP fixture catalog attestation mismatch")
			r.FailureClass = "adapter"
			return exitAdapter
		}
		var verdict hostVerdict
		switch task {
		case taskSkillBashGuard:
			verdict, err = verifySkillBashGuard(context.Background(), b, taskFixture)
		case taskMemoryLibraryShare:
			verdict, err = verifyMemoryLibraryShare(context.Background(), user, b, r.AgentID, r.SessionID, turnStarted, taskFixture)
		case taskMCPRecally:
			inspection, inspectErr := inspectCleanupLease(context.Background(), fixture.CleanupSocket, cleanupLease)
			if inspectErr != nil {
				err = inspectErr
			} else {
				verdict, err = verifyMCPRecally(context.Background(), user, b, turnStarted, *fixture, inspection)
			}
		}
		if err != nil {
			r.Errors = append(r.Errors, "verify specialized task: "+err.Error())
			r.FailureClass = "adapter"
			return exitAdapter
		}
		verdict.TaskID = string(task)
		r.HostVerdict = &verdict
	}
	err = collectEvidence(context.Background(), user, r.AgentID, r.SessionID, trajectory, &r)
	phase(&r.Metrics.Timing.ExportMs)
	if err != nil {
		r.Errors = append(r.Errors, "collect evidence: "+err.Error())
		r.FailureClass = "adapter"
		return exitAdapter
	}
	if len(r.StreamErrors) > 0 {
		r.Errors = append(r.Errors, "turn emitted error events")
		r.FailureClass = "product"
		return exitProduct
	}
	if r.TokenCount == 0 && len(r.StellaToolCalls) == 0 {
		// A turn that produced neither tokens nor tool calls did no work; do
		// not let it masquerade as a completed attempt.
		r.Errors = append(r.Errors, "turn completed without model activity")
		r.FailureClass = "product"
		return exitProduct
	}
	return 0
}

func main() { os.Exit(run()) }
