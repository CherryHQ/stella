// stella-eval-agent drives one Harbor evaluation trial through Stella's public
// HTTP API. It intentionally has no direct database or in-process server access:
// a passing benchmark must exercise the shipped service boundary.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/config"
)

const (
	exitAdapter   = 10
	exitProduct   = 11
	exitTimeout   = 12
	cleanupMargin = 2 * time.Minute
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
	SessionID               string         `json:"session_id,omitempty"`
	AgentID                 string         `json:"agent_id,omitempty"`
	UserID                  string         `json:"user_id,omitempty"`
	TurnTerminalState       string         `json:"turn_terminal_state,omitempty"`
	ToolCalls               map[string]int `json:"tool_calls"`
	StellaToolCalls         []toolCall     `json:"stella_tool_calls"`
	TokenCount              int64          `json:"token_count"`
	ElapsedSec              float64        `json:"elapsed_sec"`
	BridgeNonce             string         `json:"bridge_nonce"`
	DisabledToolsCount      int            `json:"disabled_tools_count"`
	MCPTools                []string       `json:"mcp_tools,omitempty"`
	CapabilityProfileDigest string         `json:"capability_profile_digest"`
	SandboxBackend          string         `json:"sandbox_backend,omitempty"`
	TimedOut                bool           `json:"timed_out"`
	StreamErrors            []string       `json:"stream_errors,omitempty"`
	StreamEvents            int            `json:"stream_events"`
	Metrics                 metrics        `json:"metrics"`
	TrajectoryPath          string         `json:"trajectory_path,omitempty"`
	TrajectoryTruncated     bool           `json:"trajectory_truncated,omitempty"`
	FailureClass            string         `json:"failure_class,omitempty"`
	Errors                  []string       `json:"errors,omitempty"`
}

type toolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	// IsError marks a call that failed. A call that never reached the sandbox
	// leaves no bridge ledger entry by definition, so the evidence predicate
	// must not demand one for it.
	IsError bool `json:"is_error,omitempty"`
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
func (c apiClient) streamTurn(ctx context.Context, agentID, sessionID, instruction string) (events int, streamErrors []string, err error) {
	body, err := json.Marshal(map[string]any{"parts": []map[string]string{{"type": "text", "text": instruction}}})
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

func collectEvidence(ctx context.Context, c apiClient, agentID, sessionID, trajectoryPath string, out *result) error {
	// Captured verbatim: the trajectory is the artifact a failure taxonomy and a
	// public log are built from, so re-marshalling our own structs would silently
	// drop every field this driver does not happen to model.
	var raw json.RawMessage
	if err := c.call(ctx, http.MethodGet, fmt.Sprintf("/api/agents/%s/sessions/%s/messages?limit=%d", agentID, sessionID, messageLimit), nil, &raw); err != nil {
		return err
	}
	var messages struct {
		Messages []sessionMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &messages); err != nil {
		return err
	}
	if trajectoryPath != "" && len(raw) != 0 {
		if err := os.WriteFile(trajectoryPath, raw, 0o600); err != nil {
			return err
		}
		out.TrajectoryPath = trajectoryPath
		out.TrajectoryTruncated = len(messages.Messages) >= messageLimit
	}
	m, calls := deriveMetrics(messages.Messages)
	// Best effort: a deployment that predates the usage API still produces a
	// valid trial, it just cannot report cost. Failing the trial over a missing
	// optional metric would be worse than reporting it as absent.
	var u usage
	if err := c.call(ctx, http.MethodGet, "/api/agents/"+agentID+"/sessions/"+sessionID+"/usage", nil, &u); err == nil {
		m.Usage = &u
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

func run() int {
	var baseURL, instructionFile, bindingFile, bindingDir, model, output, externalID, bundleDigest, trajectory string
	var deadlineSec int
	var stopConfirmSec int
	flag.StringVar(&baseURL, "stella-url", "", "Stella base URL")
	flag.StringVar(&instructionFile, "instruction-file", "", "task instruction file")
	flag.StringVar(&bindingFile, "binding-template", "", "Bridge binding template JSON")
	flag.StringVar(&bindingDir, "binding-dir", "", "directory read by stellad")
	flag.StringVar(&model, "model", "", "Stella provider/model")
	flag.StringVar(&output, "output", "", "result JSON path, stdout when empty")
	flag.StringVar(&externalID, "user-id", "", "unique Harbor trial identifier")
	flag.StringVar(&bundleDigest, "bundle-digest", "", "helper bundle SHA-256")
	flag.StringVar(&trajectory, "trajectory", "", "write the verbatim message history here")
	flag.IntVar(&deadlineSec, "deadline-seconds", 0, "working time in seconds, excluding the stop confirmation that follows it")
	flag.IntVar(&stopConfirmSec, "stop-confirm-seconds", 0, "seconds allowed to confirm the session stopped after the deadline; must fit inside the caller's trial limit")
	flag.Parse()
	r := result{ToolCalls: map[string]int{}}
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
	ctx := context.Background()
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
	defer func() {
		if r.AgentID != "" {
			_ = apiClient{baseURL, provision.Token, &http.Client{Timeout: 15 * time.Second}}.call(context.Background(), http.MethodDelete, "/api/agents/"+r.AgentID, nil, nil)
		}
		_ = admin.call(context.Background(), http.MethodPost, "/api/provisioned-users/"+provision.ProvisionedUser.ID+"/deactivate", nil, nil)
	}()
	user := apiClient{baseURL, provision.Token, &http.Client{Timeout: 45 * time.Second}}
	streamUser := apiClient{baseURL, provision.Token, &http.Client{}}
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
	if len(r.MCPTools) > 0 {
		r.Errors = append(r.Errors, "evaluation instance exposes MCP tools that cannot be disabled: "+strings.Join(r.MCPTools, ", "))
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
	turnCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	r.StreamEvents, r.StreamErrors, err = streamUser.streamTurn(turnCtx, r.AgentID, r.SessionID, string(instruction))
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
