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
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/config"
)

const (
	exitAdapter        = 10
	exitProduct        = 11
	exitTimeout        = 12
	cleanupMargin      = 2 * time.Minute
	usageSettleTimeout = 30 * time.Second
	usageSettlePoll    = 100 * time.Millisecond
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
	SessionID               string          `json:"session_id,omitempty"`
	AgentID                 string          `json:"agent_id,omitempty"`
	Model                   string          `json:"model,omitempty"`
	CandidateCommit         string          `json:"candidate_commit,omitempty"`
	UserID                  string          `json:"user_id,omitempty"`
	TurnTerminalState       string          `json:"turn_terminal_state,omitempty"`
	ToolCalls               map[string]int  `json:"tool_calls"`
	StellaToolCalls         []toolCall      `json:"stella_tool_calls"`
	TokenCount              int64           `json:"token_count"`
	ElapsedSec              float64         `json:"elapsed_sec"`
	BridgeNonce             string          `json:"bridge_nonce"`
	DisabledToolsCount      int             `json:"disabled_tools_count"`
	ExcludedTools           []string        `json:"excluded_tools"`
	MCPTools                []string        `json:"mcp_tools,omitempty"`
	CapabilityProfileDigest string          `json:"capability_profile_digest"`
	SandboxBackend          string          `json:"sandbox_backend,omitempty"`
	ToolStrategy            string          `json:"tool_strategy,omitempty"`
	GatewayEndpoint         string          `json:"gateway_endpoint,omitempty"`
	ProviderType            string          `json:"provider_type,omitempty"`
	ModelPriceDigest        string          `json:"model_price_digest,omitempty"`
	ExecutionCapability     []string        `json:"execution_capability,omitempty"`
	ChildToolCalls          []childToolCall `json:"child_tool_calls,omitempty"`
	TimedOut                bool            `json:"timed_out"`
	StreamErrors            []string        `json:"stream_errors,omitempty"`
	StreamEvents            int             `json:"stream_events"`
	Metrics                 metrics         `json:"metrics"`
	TrajectoryPath          string          `json:"trajectory_path,omitempty"`
	TrajectoryTruncated     bool            `json:"trajectory_truncated,omitempty"`
	FailureClass            string          `json:"failure_class,omitempty"`
	Errors                  []string        `json:"errors,omitempty"`
}

// childToolCall is the API's narrow Code Mode audit record. Arguments and
// child output never enter this driver artifact, preserving the provider
// transcript boundary while retaining comparable invocation attempts.
type childToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsError   bool   `json:"is_error"`
	ErrorKind string `json:"error_kind,omitempty"`
}

type agentTool struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Source  string `json:"source"`
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
	out.ChildToolCalls = out.ChildToolCalls[:0]
	for _, message := range messages.Messages {
		out.ChildToolCalls = append(out.ChildToolCalls, message.ChildCalls...)
	}
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

// providerEvidence is the safe, admin-only DTO emitted before the host driver
// starts. The driver reads a private file, never an admin credential or the
// full provider configuration.
type providerEvidence struct {
	ProviderID       string `json:"provider_id"`
	ModelID          string `json:"model_id"`
	GatewayEndpoint  string `json:"gateway_endpoint"`
	ProviderType     string `json:"provider_type"`
	ModelPriceDigest string `json:"model_price_digest"`
}

func loadProviderEvidence(filename, model string) (gatewayEndpoint, providerType, priceDigest string, err error) {
	providerID, modelID, ok := strings.Cut(model, "/")
	if !ok || providerID == "" || modelID == "" {
		return "", "", "", fmt.Errorf("model must be provider/model")
	}
	raw, err := os.ReadFile(filename)
	if err != nil {
		return "", "", "", fmt.Errorf("read provider evidence: %w", err)
	}
	var evidence providerEvidence
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&evidence); err != nil {
		return "", "", "", errors.New("decode provider evidence")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", "", "", errors.New("decode provider evidence")
	}
	if evidence.ProviderID != providerID || evidence.ModelID != modelID || evidence.ProviderType == "" {
		return "", "", "", errors.New("provider evidence does not match requested model")
	}
	if len(evidence.ModelPriceDigest) != 64 || strings.ToLower(evidence.ModelPriceDigest) != evidence.ModelPriceDigest {
		return "", "", "", errors.New("provider evidence has invalid price digest")
	}
	if _, err := hex.DecodeString(evidence.ModelPriceDigest); err != nil {
		return "", "", "", errors.New("provider evidence has invalid price digest")
	}
	endpoint, err := normalizeProviderEvidenceEndpoint(evidence.GatewayEndpoint)
	if err != nil || endpoint != evidence.GatewayEndpoint {
		return "", "", "", errors.New("provider evidence has invalid gateway endpoint")
	}
	return endpoint, evidence.ProviderType, evidence.ModelPriceDigest, nil
}

func normalizeProviderEvidenceEndpoint(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid gateway endpoint")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("invalid gateway endpoint scheme")
	}
	host := strings.ToLower(parsed.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := parsed.Port(); port != "" {
		host += ":" + port
	}
	basePath := path.Clean(parsed.EscapedPath())
	if basePath == "." {
		basePath = "/"
	}
	return scheme + "://" + host + basePath, nil
}

func effectiveExecutionCapability(tools []agentTool, excluded []string) ([]string, error) {
	excludedSet := make(map[string]struct{}, len(excluded))
	for _, name := range excluded {
		excludedSet[name] = struct{}{}
	}
	capability := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool.Source != "core" || !tool.Enabled {
			continue
		}
		if _, excluded := excludedSet[tool.Name]; !excluded {
			capability = append(capability, tool.Name)
		}
	}
	sortStrings(capability)
	if len(capability) != 1 || capability[0] != "bash" {
		return capability, fmt.Errorf("effective core execution capability = %q, want [bash]", capability)
	}
	return capability, nil
}

func run() int {
	var baseURL, instructionFile, bindingFile, bindingDir, model, output, externalID, bundleDigest, trajectory, excludedToolsCSV, expectedToolMode string
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
	flag.StringVar(&excludedToolsCSV, "excluded-tools", "", "comma-separated tool names to hide for this run")
	flag.StringVar(&expectedToolMode, "tool-mode", "native", "expected active tool strategy (native or code)")
	flag.IntVar(&deadlineSec, "deadline-seconds", 0, "working time in seconds, excluding the stop confirmation that follows it")
	flag.IntVar(&stopConfirmSec, "stop-confirm-seconds", 0, "seconds allowed to confirm the session stopped after the deadline; must fit inside the caller's trial limit")
	flag.Parse()
	r := result{
		ToolCalls:       map[string]int{},
		Model:           model,
		CandidateCommit: gitRevParseHead(),
		ExcludedTools:   parseExcludedTools(excludedToolsCSV),
	}
	if expectedToolMode != "native" && expectedToolMode != "code" {
		r.Errors = append(r.Errors, "tool mode must be native or code")
		r.FailureClass = "adapter"
		return exitAdapter
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
	provisioningToken := os.Getenv("STELLA_EVAL_ADMIN_TOKEN")
	if provisioningToken == "" {
		r.Errors = append(r.Errors, "provisioning token environment variable is empty")
		r.FailureClass = "adapter"
		return exitAdapter
	}
	providerEvidenceFile := os.Getenv("STELLA_EVAL_PROVIDER_EVIDENCE_FILE")
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
	provisioner := apiClient{baseURL, provisioningToken, &http.Client{Timeout: 30 * time.Second}}
	ctx := context.Background()
	// Refuse before provisioning anything. On any other backend the agent's
	// commands run somewhere that is not the trial container, and the bridge
	// ledger can only prove that after they have already run. An older server
	// that does not report the field is refused too: unknown is not bridge.
	var status struct {
		SandboxBackend string `json:"sandbox_backend"`
		AgentToolMode  string `json:"agent_tool_mode"`
	}
	if err := provisioner.call(ctx, http.MethodGet, "/api/status", nil, &status); err != nil {
		r.Errors = append(r.Errors, "read server status: "+err.Error())
		r.FailureClass = "adapter"
		return exitAdapter
	}
	r.SandboxBackend = status.SandboxBackend
	r.ToolStrategy = status.AgentToolMode
	if status.SandboxBackend != config.SandboxBackendBridge || status.AgentToolMode != expectedToolMode {
		r.Errors = append(r.Errors, fmt.Sprintf("server status sandbox_backend=%q agent_tool_mode=%q, want sandbox_backend=%q agent_tool_mode=%q", status.SandboxBackend, status.AgentToolMode, config.SandboxBackendBridge, expectedToolMode))
		r.FailureClass = "adapter"
		return exitAdapter
	}
	if providerEvidenceFile == "" {
		r.Errors = append(r.Errors, "provider evidence file environment variable is empty")
		r.FailureClass = "adapter"
		return exitAdapter
	}
	if r.GatewayEndpoint, r.ProviderType, r.ModelPriceDigest, err = loadProviderEvidence(providerEvidenceFile, model); err != nil {
		r.Errors = append(r.Errors, "read configured gateway identity: "+err.Error())
		r.FailureClass = "adapter"
		return exitAdapter
	}
	var provision struct {
		ProvisionedUser struct {
			ID string `json:"id"`
		} `json:"provisioned_user"`
		Token string `json:"token"`
	}
	err = provisioner.call(ctx, http.MethodPost, "/api/provisioned-users", map[string]any{"external_id": externalID, "email": externalID + "@eval.invalid", "name": "Harbor evaluation", "token_name": "harbor-eval", "expires_at": deadline.Add(cleanupMargin).Format(time.RFC3339)}, &provision)
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
		_ = provisioner.call(context.Background(), http.MethodPost, "/api/provisioned-users/"+provision.ProvisionedUser.ID+"/deactivate", nil, nil)
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
		_ = provisioner.call(context.Background(), http.MethodPost, "/api/provisioned-users/"+provision.ProvisionedUser.ID+"/deactivate", nil, nil)
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
		Tools []agentTool `json:"tools"`
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
	r.ExecutionCapability, err = effectiveExecutionCapability(tools.Tools, r.ExcludedTools)
	if err != nil {
		r.Errors = append(r.Errors, err.Error())
		r.FailureClass = "adapter"
		return exitAdapter
	}
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
