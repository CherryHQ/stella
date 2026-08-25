// stella-eval-agent drives one Harbor evaluation trial through Stella's public
// HTTP API. It intentionally has no direct database or in-process server access:
// a passing benchmark must exercise the shipped service boundary.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
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
	normalCleanupBudget                = 30 * time.Second
	usageSettleTimeout                 = 30 * time.Second
	usageSettlePoll                    = 100 * time.Millisecond
	cleanupSocketDialCeiling           = 5 * time.Second
	cleanupSocketIOCeiling             = 15 * time.Second
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
	ProviderSurfaceTools            []string       `json:"provider_surface_tools,omitempty"`
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
	Cleanup                         []cleanupPhase `json:"cleanup,omitempty"`
	// libraryFixture is host-only cleanup state. It never leaves the adapter
	// result because it is solely the ownership proof for Library teardown.
	libraryFixture bool
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

type agentTool struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Source  string `json:"source"`
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
	FixturePlanSeed      string `json:"fixture_plan_seed"`
}

type specializedTask string

const (
	taskSkillBashGuard        specializedTask = "skill-bash-guard"
	taskMemoryLibraryEvidence specializedTask = "memory-library-evidence"
	taskMCPRecally            specializedTask = "mcp-recally"
	skillArtifactPath                         = "/workspace/report.txt"
	shareArtifactPath                         = "/workspace/evidence.txt"
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

type cleanupPhase struct {
	Phase   string `json:"phase"`
	Outcome string `json:"outcome"`
}

func cleanupTrialResources(ctx context.Context, r *result, user, admin apiClient, provisionedUserID string) error {
	var firstErr error
	record := func(phase string, cleanupErr error) bool {
		outcome := "completed"
		if cleanupErr != nil {
			outcome = "error"
			r.Errors = append(r.Errors, "cleanup "+phase+": "+cleanupErr.Error())
			if firstErr == nil {
				firstErr = cleanupErr
			}
		}
		r.Cleanup = append(r.Cleanup, cleanupPhase{Phase: phase, Outcome: outcome})
		return cleanupErr == nil
	}

	registrationComplete := true
	if r.MCPRegistrationID != "" && r.AgentID != "" {
		path := "/api/mcp/servers/" + r.MCPRegistrationID + "?scope=user_agent&agent_id=" + url.QueryEscape(r.AgentID)
		registrationComplete = record("mcp_registration", user.call(ctx, http.MethodDelete, path, nil, nil))
	} else {
		r.Cleanup = append(r.Cleanup, cleanupPhase{Phase: "mcp_registration", Outcome: "skipped"})
	}
	libraryComplete := true
	if r.libraryFixture && r.AgentID != "" {
		libraryComplete = record("library_files", deleteTrialLibraryFiles(ctx, user, r.AgentID))
	}
	agentComplete := true
	if r.AgentID != "" {
		agentComplete = record("agent", deleteTrialAgent(ctx, user, r.AgentID, r.libraryFixture))
	} else {
		r.Cleanup = append(r.Cleanup, cleanupPhase{Phase: "agent", Outcome: "skipped"})
	}
	if registrationComplete && libraryComplete && agentComplete {
		record("provisioned_user", admin.call(ctx, http.MethodPost, "/api/provisioned-users/"+provisionedUserID+"/deactivate", nil, nil))
		return firstErr
	}
	// A retained cleanup lease needs this PAT. The Python coordinator retries
	// the user-scoped phases before making the irreversible admin deactivation.
	r.Cleanup = append(r.Cleanup, cleanupPhase{Phase: "provisioned_user", Outcome: "pending"})
	return firstErr
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
// deleteTrialLibraryFiles uses Library's deletion boundary before deleting the
// owning Agent. library_file deliberately RESTRICTs Agent deletion so the raw
// snapshot cleanup cannot be bypassed by a database cascade.
func deleteTrialLibraryFiles(ctx context.Context, user apiClient, agentID string) error {
	var list struct {
		LibraryFiles []struct {
			ID string `json:"id"`
		} `json:"library_files"`
	}
	path := "/api/library-files?scope=user_agent&agent_id=" + url.QueryEscape(agentID)
	if err := user.call(ctx, http.MethodGet, path, nil, &list); err != nil {
		return fmt.Errorf("list library files: %w", err)
	}
	for _, file := range list.LibraryFiles {
		if file.ID == "" {
			return errors.New("list library files: response omitted file id")
		}
		if err := user.call(ctx, http.MethodDelete, "/api/library-files/"+url.PathEscape(file.ID), nil, nil); err != nil {
			return fmt.Errorf("delete library file: %w", err)
		}
	}
	return nil
}

// deleteTrialAgent waits only after this fresh trial tombstoned Library files.
// Library deletion commits visibility before its asynchronous raw-object cleanup,
// while the Agent FK remains RESTRICT until that worker hard-deletes metadata.
// The retry is scoped to the known fixture and reports any final failure.
func deleteTrialAgent(ctx context.Context, user apiClient, agentID string, libraryFixture bool) error {
	for {
		err := user.call(ctx, http.MethodDelete, "/api/agents/"+agentID, nil, nil)
		if err == nil || !libraryFixture {
			return err
		}
		var apiErr *apiError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError {
			return err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
	}
}

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
	// Request cancellation should already interrupt the body read, but retain a
	// client deadline as a second fence. A transport that ignores a canceled
	// context otherwise leaves Harbor's child process alive past the trial wall.
	httpClient := *c.http
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, nil, context.DeadlineExceeded
		}
		if httpClient.Timeout == 0 || httpClient.Timeout > remaining {
			httpClient.Timeout = remaining
		}
	}
	resp, err := httpClient.Do(req)
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
	if !response.OK {
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

// laneCatalogTools is the frozen builtin union for every specialized trial.
// Task verifiers prove their own causal behavior separately; changing this
// set changes the lane's provider contract and must apply to all three tasks.
func laneCatalogTools() map[string]struct{} {
	return map[string]struct{}{
		"bash":           {},
		"skills":         {},
		"memory":         {},
		"library_search": {},
		"recally":        {},
	}
}

func parseSpecializedTask(value string) (specializedTask, error) {
	switch specializedTask(value) {
	case taskSkillBashGuard, taskMemoryLibraryEvidence, taskMCPRecally:
		return specializedTask(value), nil
	default:
		return "", errors.New("unknown specialized task identity")
	}
}

func newSpecializedFixture(task specializedTask, planSeed string) (specializedFixture, string, error) {
	if planSeed == "" {
		return specializedFixture{}, "", errors.New("specialized fixture plan seed is required")
	}
	// The seed remains in the mode-0600 host fixture config. The task-visible
	// token is deterministic for a task and plan, so paired trials receive the
	// same private Skill without serializing either secret into the result.
	mac := hmac.New(sha256.New, []byte(planSeed))
	_, _ = mac.Write([]byte("stella-eval-fixture-plan-v1:" + string(task)))
	material := mac.Sum(nil)
	token := base64.RawURLEncoding.EncodeToString(material[:18])
	tokenDigest := sha256.Sum256(material)
	planDigest := sha256.Sum256([]byte("specialized-tools-v2:" + string(task) + ":" + hex.EncodeToString(tokenDigest[:])))

	switch task {
	case taskSkillBashGuard:
		artifact := []byte("status=restored\ntoken=" + token + "\n")
		return specializedFixture{task: task, skill: "# Repair report\n\nWrite exactly this file, with no extra bytes:\n\n" + string(artifact), artifact: artifact}, "sha256:" + hex.EncodeToString(planDigest[:]), nil
	case taskMemoryLibraryEvidence:
		return specializedFixture{task: task, memory: "Memory fact: cobalt lantern.", library: "Library fact: amber meadow.", artifact: []byte("memory=cobalt lantern\nlibrary=amber meadow\n")}, "sha256:" + hex.EncodeToString(planDigest[:]), nil
	case taskMCPRecally:
		return specializedFixture{task: task}, "sha256:" + hex.EncodeToString(planDigest[:]), nil
	default:
		return specializedFixture{}, "", errors.New("unknown specialized task fixture")
	}
}

func parseFixtureConfig(path string) (fixtureConfig, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return fixtureConfig{}, errors.New("MCP fixture config must be a regular mode 0600 file")
	}
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
	if cfg.CatalogDigest == "" || cfg.ArticleCanonicalURL == "" || cfg.ArticleTitle == "" || cfg.FixturePlanSeed == "" || !strings.HasPrefix(cfg.ArticleContentDigest, "sha256:") || !strings.HasPrefix(cfg.FixturePlanDigest, "sha256:") {
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

func claimCleanupLease(ctx context.Context, socket string, claim map[string]any) (string, error) {
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
	Version                      int  `json:"version"`
	Complete                     bool `json:"complete"`
	CatalogCount                 int  `json:"catalog_count"`
	InitializeCount              int  `json:"initialize_count"`
	InitializedNotificationCount int  `json:"initialized_notification_count"`
	ToolsListCount               int  `json:"tools_list_count"`
	AckWriteCount                int  `json:"ack_write_count"`
	DuplicateWriteCount          int  `json:"duplicate_write_count"`
	ChainComplete                bool `json:"chain_complete"`
}

func inspectCleanupLease(ctx context.Context, socket, lease string) (fixtureInspection, error) {
	var out struct {
		Inspect *fixtureInspection `json:"inspect"`
		Error   string             `json:"error"`
	}
	dialCtx, cancel := context.WithTimeout(ctx, cleanupSocketDialCeiling)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "unix", socket)
	if err != nil {
		return fixtureInspection{}, err
	}
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(cleanupSocketIOCeiling)
	if finalizationDeadline, ok := ctx.Deadline(); ok && finalizationDeadline.Before(deadline) {
		deadline = finalizationDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return fixtureInspection{}, err
	}
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

// stopConfirmBudget is the entire finalization wall after a trial's working
// deadline. It covers stop confirmation, specialized admission, evidence, and
// cleanup. Deliberate ceiling; raise it only when measurements show those
// public-API operations cannot complete within it.
const stopConfirmBudget = 3 * time.Minute

type timeoutCleanup func(context.Context) error

// finishTimedOut ends a trial that ran out of working time. Every operation
// after that deadline shares finalizationCtx, so no diagnostic or deferred
// cleanup can turn one bounded trial into an unbounded Harbor child.
func finishTimedOut(user apiClient, r *result, trajectory string, phase func(*int64), finalizationBudget time.Duration, task specializedTask, fixture *fixtureConfig, cleanupLease string, cleanup timeoutCleanup) int {
	r.TimedOut = true
	finalizationCtx, cancel := context.WithTimeout(context.Background(), finalizationBudget)
	defer cancel()

	var firstErr error
	fail := func(operation string, err error) {
		if firstErr == nil {
			firstErr = err
			r.HostVerdict = nil
			r.FailureClass = "adapter"
		}
		// Keep every bounded diagnostic, but the first entry remains the causal
		// failure that classified this timeout as adapter-invalid.
		r.Errors = append(r.Errors, operation+": "+err.Error())
	}
	collect := func() {
		if err := collectEvidence(finalizationCtx, user, r.AgentID, r.SessionID, trajectory, r); err != nil {
			fail("collect evidence after timeout", err)
		}
		phase(&r.Metrics.Timing.ExportMs)
	}

	if err := stopAndConfirm(finalizationCtx, user, r.AgentID, r.SessionID); err != nil {
		fail("confirm terminal after timeout", err)
		// Keep the diagnostic export best effort, but it cannot replace the stop
		// failure or attach a verdict.
		collect()
	} else {
		phase(&r.Metrics.Timing.StopMs)
		r.TurnTerminalState = "stopped"
		if task != "" {
			if err := collectRuntimeSurface(finalizationCtx, user, r.AgentID, r.SessionID, r); err != nil {
				fail("collect runtime tool surface after timeout", err)
			} else if _, err := assertSpecializedAdmission(finalizationCtx, *r, fixture, cleanupLease); err != nil {
				fail("specialized catalog admission after timeout", err)
			}
		}
		collect()
		if firstErr == nil && task != "" {
			// A timeout is scoreable only after the complete terminal evidence is
			// present. Never publish a valid zero before its evidence exists.
			r.HostVerdict = &hostVerdict{Version: 1, TaskID: string(task), Valid: true, Reward: 0, Reasons: []string{"agent deadline"}, Nonce: r.BridgeNonce}
		}
	}
	if cleanup != nil {
		if err := cleanup(finalizationCtx); err != nil {
			fail("cleanup after timeout", err)
		}
	}
	if firstErr != nil {
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
	case taskMemoryLibraryEvidence:
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
		return nil
	case taskMemoryLibraryEvidence:
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
		return nil
	case taskMCPRecally:
		return nil
	}
	return errors.New("unknown specialized fixture")
}

func businessFailure(nonce, reason string) hostVerdict {
	return hostVerdict{Version: 1, Valid: true, Reward: 0, Reasons: []string{reason}, Nonce: nonce}
}

func artifactBusinessFailure(err error) bool {
	// Fixed artifacts are tiny regular files by contract. Missing, directory,
	// non-regular, empty, and oversized artifacts are wrong task output, never
	// a reason to erase an attempted trial from the denominator.
	return strings.HasSuffix(err.Error(), ": not_found") || strings.HasSuffix(err.Error(), ": is_dir") || strings.HasSuffix(err.Error(), ": non_regular") || strings.HasSuffix(err.Error(), ": too_large")
}

func sha256Digest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func verifyExactArtifact(ctx context.Context, b binding, path string, expected []byte, missingReason, mismatchReason string) (hostVerdict, error) {
	artifact, err := readBridgeArtifact(ctx, b, path)
	if err != nil {
		if artifactBusinessFailure(err) {
			return businessFailure(b.Nonce, missingReason), nil
		}
		return hostVerdict{}, err
	}
	if !bytes.Equal(artifact, expected) {
		return businessFailure(b.Nonce, mismatchReason), nil
	}
	return hostVerdict{Version: 1, Valid: true, Reward: 1, Nonce: b.Nonce}, nil
}

func verifySkillBashGuard(ctx context.Context, b binding, fixture specializedFixture) (hostVerdict, error) {
	return verifyExactArtifact(ctx, b, skillArtifactPath, fixture.artifact, "required artifact is missing", "artifact content does not match private Skill")
}

func verifyMemoryLibraryEvidence(ctx context.Context, b binding, fixture specializedFixture) (hostVerdict, error) {
	return verifyExactArtifact(ctx, b, shareArtifactPath, fixture.artifact, "required evidence artifact is missing", "evidence artifact does not contain the canonical facts")
}

func fixtureCatalogMatches(count int, digest string, plan fixtureConfig) bool {
	return count == specializedCatalogCount && digest == plan.CatalogDigest
}

func verifyMCPRecally(ctx context.Context, user apiClient, b binding, turnStarted time.Time, plan fixtureConfig, inspection fixtureInspection) (hostVerdict, error) {
	// Catalog admission is lane-wide and already complete before this verifier
	// runs. This task checks only its causal three-call chain and Recally write.
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

// failAfterRuntimeSurface keeps the runtime-surface error as the invalid
// trial's first causal evidence. The history export remains best effort: it
// makes a broken admission diagnosable, but cannot replace that admission
// failure with an unrelated export error.
func failAfterRuntimeSurface(ctx context.Context, c apiClient, out *result, trajectory string, phase func(*int64), operation string, surfaceErr error) int {
	out.Errors = append(out.Errors, operation+": "+surfaceErr.Error())
	if evidenceErr := collectEvidence(ctx, c, out.AgentID, out.SessionID, trajectory, out); evidenceErr != nil {
		out.Errors = append(out.Errors, "collect evidence after runtime tool surface failure: "+evidenceErr.Error())
	}
	phase(&out.Metrics.Timing.ExportMs)
	out.FailureClass = "adapter"
	return exitAdapter
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
	if detail.ToolSurface == nil {
		return errors.New("runtime tool surface is missing")
	}
	if detail.ToolSurface.Strategy != "native" {
		return fmt.Errorf("runtime tool surface strategy is %q, want native", detail.ToolSurface.Strategy)
	}
	if len(detail.ToolSurface.Tools) == 0 {
		return errors.New("runtime tool surface has zero tools")
	}
	raw, err := json.Marshal(detail.ToolSurface.Tools)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	out.ToolStrategy = detail.ToolSurface.Strategy
	out.ProviderSurfaceCount = len(detail.ToolSurface.Tools)
	out.ProviderSurfaceTools = make([]string, 0, len(detail.ToolSurface.Tools))
	for _, tool := range detail.ToolSurface.Tools {
		out.ProviderSurfaceTools = append(out.ProviderSurfaceTools, tool.Name)
	}
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

func assertRuntimeLaneCatalog(r result) error {
	if r.ToolStrategy != "native" || r.ProviderSurfaceCount == 0 {
		return errors.New("native provider surface is absent")
	}
	seen := make(map[string]struct{}, len(r.ProviderSurfaceTools))
	for _, name := range r.ProviderSurfaceTools {
		seen[name] = struct{}{}
	}
	for name := range laneCatalogTools() {
		if _, ok := seen[name]; !ok {
			return fmt.Errorf("lane catalog tool %q is absent", name)
		}
	}
	for _, name := range []string{"view_image", "vllm"} {
		if _, ok := seen[name]; ok {
			return fmt.Errorf("excluded tool %q is present in the runtime surface", name)
		}
	}
	return nil
}

func assertMCPFixtureAdmission(r result, plan fixtureConfig, inspection fixtureInspection) error {
	if r.MCPRegistrationID == "" {
		return errors.New("MCP fixture registration is absent")
	}
	if len(r.MCPTools) != 1 || r.MCPTools[0] != specializedFixtureRegistrationName {
		return errors.New("MCP fixture registration inventory mismatch")
	}
	if !fixtureCatalogMatches(r.SpecializedCatalogCount, r.SpecializedCatalogDigest, plan) || r.RuntimeSpecializedCatalogDigest == "" {
		return errors.New("runtime MCP fixture catalog attestation mismatch")
	}
	if inspection.Version != 1 || !inspection.Complete || inspection.CatalogCount != specializedCatalogCount || inspection.InitializeCount < 1 || inspection.InitializedNotificationCount != 1 || inspection.ToolsListCount < 1 {
		return errors.New("MCP fixture initialize/initialized/tools-list admission is incomplete")
	}
	return nil
}

func assertSpecializedAdmission(ctx context.Context, r result, plan *fixtureConfig, cleanupLease string) (fixtureInspection, error) {
	if plan == nil || cleanupLease == "" {
		return fixtureInspection{}, errors.New("specialized MCP fixture admission is unavailable")
	}
	if err := assertRuntimeLaneCatalog(r); err != nil {
		return fixtureInspection{}, err
	}
	inspection, err := inspectCleanupLease(ctx, plan.CleanupSocket, cleanupLease)
	if err != nil {
		return fixtureInspection{}, err
	}
	if err := assertMCPFixtureAdmission(r, *plan, inspection); err != nil {
		return fixtureInspection{}, err
	}
	return inspection, nil
}

func mcpToolInventory(tools []agentTool) []string {
	inventory := make([]string, 0)
	for _, tool := range tools {
		if tool.Source == "mcp" {
			inventory = append(inventory, tool.Name)
		}
	}
	sortStrings(inventory)
	return inventory
}

func laneToolInventory(tools []agentTool, requireEnabled bool) error {
	seen := make(map[string]agentTool, len(tools))
	for _, tool := range tools {
		if _, duplicate := seen[tool.Name]; duplicate {
			return fmt.Errorf("duplicate tool %q in agent inventory", tool.Name)
		}
		seen[tool.Name] = tool
	}
	for name := range laneCatalogTools() {
		tool, ok := seen[name]
		if !ok {
			return fmt.Errorf("lane catalog tool %q is absent", name)
		}
		if name == "bash" && tool.Source != "core" {
			return fmt.Errorf("lane catalog tool %q is not core", name)
		}
		if name != "bash" && tool.Source != "builtin" {
			return fmt.Errorf("lane catalog tool %q is not a builtin", name)
		}
		if requireEnabled && !tool.Enabled {
			return fmt.Errorf("lane catalog tool %q is disabled", name)
		}
	}
	inventory := mcpToolInventory(tools)
	if len(inventory) != 1 || inventory[0] != specializedFixtureRegistrationName {
		return errors.New("MCP fixture registration inventory mismatch")
	}
	return nil
}

func assertLaneAdmissionInventory(tools []agentTool) error {
	return laneToolInventory(tools, true)
}

func assertLaneToolPolicy(tools []agentTool) error {
	if err := assertLaneAdmissionInventory(tools); err != nil {
		return err
	}
	for _, tool := range tools {
		if tool.Source != "core" && tool.Source != "mcp" {
			if _, keep := laneCatalogTools()[tool.Name]; !keep && tool.Enabled {
				return fmt.Errorf("non-core tool %q remains enabled", tool.Name)
			}
		}
	}
	return nil
}

// configureSpecializedToolPolicy makes the lane independent of deployment
// defaults. Core tools are immutable; every non-core tool gets an explicit
// user-agent policy before the post-write inventory attests the frozen union.
func configureSpecializedToolPolicy(ctx context.Context, user apiClient, agentID string, tools []agentTool) ([]agentTool, []string, error) {
	if err := laneToolInventory(tools, false); err != nil {
		return nil, nil, err
	}

	disabled := make([]string, 0)
	for _, tool := range tools {
		if tool.Source == "core" || tool.Source == "mcp" {
			continue
		}
		_, keep := laneCatalogTools()[tool.Name]
		enabled := keep
		if keep && tool.Enabled {
			continue
		}
		if err := user.call(ctx, http.MethodPatch, "/api/agents/"+agentID+"/tools/"+url.PathEscape(tool.Name), map[string]any{"enabled": enabled, "scope": "user_agent"}, nil); err != nil {
			return nil, nil, fmt.Errorf("set tool %q enabled=%t: %w", tool.Name, enabled, err)
		}
		if !keep {
			disabled = append(disabled, tool.Name)
		}
	}

	var refreshed struct {
		Tools []agentTool `json:"tools"`
	}
	if err := user.call(ctx, http.MethodGet, "/api/agents/"+agentID+"/tools", nil, &refreshed); err != nil {
		return nil, nil, fmt.Errorf("read lane tool policy: %w", err)
	}
	if err := assertLaneToolPolicy(refreshed.Tools); err != nil {
		return nil, nil, err
	}
	return refreshed.Tools, disabled, nil
}

func assertSpecializedExclusions(excluded []string) error {
	seen := make(map[string]struct{}, len(excluded))
	for _, name := range excluded {
		seen[name] = struct{}{}
	}
	for _, name := range []string{"view_image", "vllm"} {
		if _, ok := seen[name]; !ok {
			return fmt.Errorf("specialized lane must exclude %q", name)
		}
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
	flag.IntVar(&stopConfirmSec, "stop-confirm-seconds", 0, "total seconds allowed after the working deadline for timeout finalization; must fit inside the caller's trial limit")
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
	finalizationBudget := stopConfirmBudget
	if stopConfirmSec > 0 {
		finalizationBudget = time.Duration(stopConfirmSec) * time.Second
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
	cleanupComplete := false
	cleanup := func(cleanupCtx context.Context) error {
		if cleanupComplete {
			return nil
		}
		cleanupComplete = true
		return cleanupTrialResources(cleanupCtx, &r, user, admin, provision.ProvisionedUser.ID)
	}
	defer func() {
		if cleanupComplete {
			return
		}
		// Ordinary exits retain a bounded cleanup budget. Timeout finalization
		// passes its one shared context directly instead.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), normalCleanupBudget)
		defer cancel()
		_ = cleanup(cleanupCtx)
	}()
	var task specializedTask
	var taskFixture specializedFixture
	var fixture *fixtureConfig
	if fixtureConfigPath == "" {
		if _, specializedErr := parseSpecializedTask(taskID); specializedErr == nil {
			r.Errors = append(r.Errors, "specialized task fixture config is required")
			r.FailureClass = "adapter"
			return exitAdapter
		}
	}
	if fixtureConfigPath != "" {
		var taskErr error
		task, taskErr = parseSpecializedTask(taskID)
		if taskErr != nil {
			r.Errors = append(r.Errors, taskErr.Error())
			r.FailureClass = "adapter"
			return exitAdapter
		}
		loaded, fixtureErr := parseFixtureConfig(fixtureConfigPath)
		if fixtureErr != nil {
			r.Errors = append(r.Errors, fixtureErr.Error())
			r.FailureClass = "adapter"
			return exitAdapter
		}
		taskFixture, r.FixturePlanDigest, taskErr = newSpecializedFixture(task, loaded.FixturePlanSeed)
		if taskErr != nil {
			r.Errors = append(r.Errors, taskErr.Error())
			r.FailureClass = "adapter"
			return exitAdapter
		}
		r.TaskID = string(task)
		// Every specialized task shares this host-owned config and receives its
		// own same-named MCP registration, opaque route, and cleanup lease.
		fixture = &loaded
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
		// Own teardown before seeding: upload can succeed while the following
		// verification fails, and no recovery lease exists until later.
		r.libraryFixture = taskFixture.task == taskMemoryLibraryEvidence
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
		lease, leaseErr := claimCleanupLease(ctx, fixture.CleanupSocket, map[string]any{
			"action": "claim", "token": provision.Token, "trial": externalID, "user_id": r.UserID,
			"agent_id": r.AgentID, "registration_id": r.MCPRegistrationID, "library_files": r.libraryFixture,
		})
		cleanupLease = lease
		if leaseErr != nil || writeCleanupState(cleanupStatePath, cleanupState{Lease: lease, ProvisionedUserID: provision.ProvisionedUser.ID}) != nil {
			r.Errors = append(r.Errors, "publish cleanup lease")
			r.FailureClass = "adapter"
			return exitAdapter
		}
	}
	var tools struct {
		Tools []agentTool `json:"tools"`
	}
	if err = user.call(ctx, http.MethodGet, "/api/agents/"+r.AgentID+"/tools", nil, &tools); err != nil {
		r.Errors = append(r.Errors, "list tools: "+err.Error())
		r.FailureClass = "adapter"
		return exitAdapter
	}
	// MCP tools bypass the sandbox Session and cannot be disabled. The frozen
	// specialized lane therefore admits exactly one fixture registration for
	// every task, then verifies its actual 53-tool provider surface after turn
	// admission. Ordinary evaluation runs still reject any MCP inventory.
	r.MCPTools = mcpToolInventory(tools.Tools)
	if task == "" && len(r.MCPTools) > 0 {
		r.Errors = append(r.Errors, "evaluation instance exposes MCP tools that cannot be disabled: "+strings.Join(r.MCPTools, ", "))
		r.FailureClass = "adapter"
		return exitAdapter
	}
	disabled := []string{}
	if task != "" {
		if err := assertSpecializedExclusions(r.ExcludedTools); err != nil {
			r.Errors = append(r.Errors, err.Error())
			r.FailureClass = "adapter"
			return exitAdapter
		}
		tools.Tools, disabled, err = configureSpecializedToolPolicy(ctx, user, r.AgentID, tools.Tools)
		if err != nil {
			r.Errors = append(r.Errors, "configure specialized lane tool policy: "+err.Error())
			r.FailureClass = "adapter"
			return exitAdapter
		}
		r.MCPTools = mcpToolInventory(tools.Tools)
	} else {
		for _, tool := range tools.Tools {
			if tool.Source == "core" || tool.Source == "mcp" || !tool.Enabled {
				continue
			}
			if err = user.call(ctx, http.MethodPatch, "/api/agents/"+r.AgentID+"/tools/"+url.PathEscape(tool.Name), map[string]any{"enabled": false, "scope": "user_agent"}, nil); err != nil {
				r.Errors = append(r.Errors, "disable tool "+tool.Name+": "+err.Error())
				r.FailureClass = "adapter"
				return exitAdapter
			}
			disabled = append(disabled, tool.Name)
		}
	}
	// Runtime exclusions are part of the admitted tool surface even when a
	// deployment does not list that optional core tool in /tools. Count them
	// beside persisted non-core disables for the evidence predicate.
	r.DisabledToolsCount = len(disabled) + len(r.ExcludedTools)
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
		return finishTimedOut(user, &r, trajectory, phase, finalizationBudget, task, fixture, cleanupLease, cleanup)
	}
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		r.Errors = append(r.Errors, "trial driver canceled")
		r.FailureClass = "adapter"
		return exitAdapter
	}
	if err != nil {
		r.Errors = append(r.Errors, "send instruction: "+err.Error())
		r.FailureClass = "product"
		// A specialized product error is scoreable only when the fixed lane
		// catalog was actually admitted. Otherwise it is a harness-invalid
		// failure, not evidence about the task behavior.
		if task != "" {
			if surfaceErr := collectRuntimeSurface(context.Background(), user, r.AgentID, r.SessionID, &r); surfaceErr != nil {
				return failAfterRuntimeSurface(context.Background(), user, &r, trajectory, phase, "collect runtime tool surface after product error", surfaceErr)
			}
			if _, admissionErr := assertSpecializedAdmission(context.Background(), r, fixture, cleanupLease); admissionErr != nil {
				r.Errors = append(r.Errors, "specialized catalog admission after product error: "+admissionErr.Error())
				r.FailureClass = "adapter"
				return exitAdapter
			}
		}
		_ = collectEvidence(context.Background(), user, r.AgentID, r.SessionID, trajectory, &r)
		phase(&r.Metrics.Timing.ExportMs)
		return exitProduct
	}
	state, err := waitForTerminal(turnCtx, user, r.AgentID, r.SessionID)
	phase(&r.Metrics.Timing.TurnMs)
	if errors.Is(err, context.DeadlineExceeded) {
		return finishTimedOut(user, &r, trajectory, phase, finalizationBudget, task, fixture, cleanupLease, cleanup)
	}
	if err != nil {
		r.Errors = append(r.Errors, "wait terminal: "+err.Error())
		r.FailureClass = "adapter"
		return exitAdapter
	}
	r.TurnTerminalState = state
	if task != "" {
		if err = collectRuntimeSurface(context.Background(), user, r.AgentID, r.SessionID, &r); err != nil {
			return failAfterRuntimeSurface(context.Background(), user, &r, trajectory, phase, "collect runtime tool surface", err)
		}
		inspection, admissionErr := assertSpecializedAdmission(context.Background(), r, fixture, cleanupLease)
		if admissionErr != nil {
			r.Errors = append(r.Errors, "specialized catalog admission: "+admissionErr.Error())
			r.FailureClass = "adapter"
			return exitAdapter
		}
		var verdict hostVerdict
		switch task {
		case taskSkillBashGuard:
			verdict, err = verifySkillBashGuard(context.Background(), b, taskFixture)
		case taskMemoryLibraryEvidence:
			verdict, err = verifyMemoryLibraryEvidence(context.Background(), b, taskFixture)
		case taskMCPRecally:
			verdict, err = verifyMCPRecally(context.Background(), user, b, turnStarted, *fixture, inspection)
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
