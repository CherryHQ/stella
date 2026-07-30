package live

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/pgruntime"
	"github.com/CherryHQ/stella/internal/vault"
	releasecontract "github.com/CherryHQ/stella/test/release"
)

const (
	candidateReadyTimeout    = 120 * time.Second
	candidateShutdownTimeout = 45 * time.Second
)

// stellaCandidate owns one isolated candidate daemon and its embedded
// PostgreSQL process for the X12 real Session journey.
type stellaCandidate struct {
	runID   string
	home    string
	baseURL string
	client  *http.Client
	command *exec.Cmd
	done    chan struct{}
	waitErr error
}

func startStellaCandidate(
	ctx context.Context,
	binary string,
	run releasecontract.Run,
) (providerCandidate, error) {
	runtimeRoot, err := installedPostgresRuntime()
	if err != nil {
		return nil, err
	}
	home, err := os.MkdirTemp("", "stella-live-provider-*")
	if err != nil {
		return nil, fmt.Errorf("create candidate home: %w", err)
	}
	cleanupHome := true
	defer func() {
		if cleanupHome {
			_ = os.RemoveAll(home)
		}
	}()

	vaultKey, err := vault.GenerateMasterIdentity()
	if err != nil {
		return nil, fmt.Errorf("generate candidate vault identity: %w", err)
	}
	port, err := freeTCPPort()
	if err != nil {
		return nil, err
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := os.Mkdir(filepath.Join(home, "tmp"), 0o700); err != nil {
		return nil, fmt.Errorf("create candidate temp directory: %w", err)
	}
	logFile, err := os.Create(filepath.Join(home, "candidate.log"))
	if err != nil {
		return nil, fmt.Errorf("create candidate log: %w", err)
	}
	absoluteBinary, err := filepath.Abs(binary)
	if err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("resolve candidate binary: %w", err)
	}
	command := exec.Command(absoluteBinary, "serve")
	command.Stdout = logFile
	command.Stderr = logFile
	command.Env = append(candidateBaseEnv(home),
		"STELLA_HOME="+home,
		"STELLA_POSTGRES_RUNTIME="+runtimeRoot,
		"STELLA_VAULT_KEY="+vaultKey,
		"HOST=127.0.0.1",
		fmt.Sprintf("PORT=%d", port),
		"LOG_LEVEL=warn",
	)
	setLiveProcessGroup(command)
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("start candidate: %w", err)
	}
	_ = logFile.Close()

	candidate := &stellaCandidate{
		runID: run.ID, home: home, baseURL: baseURL,
		command: command, done: make(chan struct{}),
	}
	go func() {
		candidate.waitErr = command.Wait()
		close(candidate.done)
	}()
	if err := candidate.waitReady(ctx); err != nil {
		_ = candidate.Close(context.Background())
		return nil, err
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		_ = candidate.Close(context.Background())
		return nil, fmt.Errorf("create candidate cookie jar: %w", err)
	}
	candidate.client = &http.Client{Jar: jar}
	if err := candidate.registerBootstrapUser(ctx); err != nil {
		_ = candidate.Close(context.Background())
		return nil, err
	}
	cleanupHome = false
	return candidate, nil
}

func (c *stellaCandidate) RunProviderJourney(
	ctx context.Context,
	target providerTargetConfig,
	providerBaseURL string,
	apiKey string,
	marker string,
) (providerJourneyResult, error) {
	providerID := candidateID(c.runID, target.ID+"-provider")
	if err := c.doJSON(ctx, http.MethodPost, "/api/providers", map[string]any{
		"id":       providerID,
		"type":     target.Type,
		"name":     "Release Live " + target.ID,
		"enabled":  true,
		"api_key":  apiKey,
		"base_url": providerBaseURL,
		"models":   map[string]any{},
	}, http.StatusCreated, nil); err != nil {
		return providerJourneyResult{}, err
	}

	var agent struct {
		ID string `json:"id"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/agents", map[string]any{
		"name":          candidateID(c.runID, target.ID+"-agent"),
		"model":         providerID + "/" + target.Model,
		"enabled":       true,
		"system_prompt": "Follow the release-test instruction exactly. Use only the requested tool and do not expose credentials.",
	}, http.StatusCreated, &agent); err != nil {
		return providerJourneyResult{}, err
	}
	if agent.ID == "" {
		return providerJourneyResult{}, fmt.Errorf("candidate created an agent without an id")
	}
	if err := c.assertBashAvailable(ctx, agent.ID); err != nil {
		return providerJourneyResult{}, err
	}

	var session struct {
		ID string `json:"id"`
	}
	path := fmt.Sprintf("/api/agents/%s/sessions", agent.ID)
	if err := c.doJSON(ctx, http.MethodPost, path, map[string]any{"kind": "chat"}, http.StatusCreated, &session); err != nil {
		return providerJourneyResult{}, err
	}
	if session.ID == "" {
		return providerJourneyResult{}, fmt.Errorf("candidate created a Session without an id")
	}

	prompt := fmt.Sprintf(
		"Call the bash tool exactly once with command `printf '%%s' '%s'`. "+
			"After reading the tool output, reply with the exact marker %s.",
		marker,
		marker,
	)
	return c.streamProviderTurn(ctx, agent.ID, session.ID, target.ID, marker, prompt)
}

func (c *stellaCandidate) streamProviderTurn(
	ctx context.Context,
	agentID string,
	sessionID string,
	targetID string,
	marker string,
	prompt string,
) (providerJourneyResult, error) {
	payload, err := json.Marshal(map[string]any{
		"parts": []map[string]any{{"type": "text", "text": prompt}},
	})
	if err != nil {
		return providerJourneyResult{}, fmt.Errorf("encode Session prompt: %w", err)
	}
	path := fmt.Sprintf("/api/agents/%s/sessions/%s/messages", agentID, sessionID)
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+path,
		bytes.NewReader(payload),
	)
	if err != nil {
		return providerJourneyResult{}, fmt.Errorf("build Session request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	response, err := c.client.Do(request)
	if err != nil {
		// This request targets the local candidate, not CherryIN. A connection
		// or read failure here is therefore candidate behavior, even when the
		// underlying model request may have triggered it.
		return providerJourneyResult{}, fmt.Errorf("call candidate Session stream: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusBadGateway ||
			response.StatusCode == http.StatusServiceUnavailable ||
			response.StatusCode == http.StatusGatewayTimeout {
			return providerJourneyResult{}, &providerExternalError{
				reason: "upstream gateway was unavailable", retryable: true,
			}
		}
		return providerJourneyResult{}, fmt.Errorf("candidate Session endpoint returned HTTP %d", response.StatusCode)
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		return providerJourneyResult{}, fmt.Errorf("candidate Session endpoint did not return SSE")
	}

	result := providerJourneyResult{TargetID: targetID}
	var (
		text              strings.Builder
		toolOutput        strings.Builder
		bashToolCallID    string
		completedSentinel bool
	)
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			completedSentinel = true
			break
		}
		var event struct {
			Type       string `json:"type"`
			Delta      string `json:"delta"`
			ErrorText  string `json:"errorText"`
			ToolName   string `json:"toolName"`
			ToolCallID string `json:"toolCallId"`
			Output     any    `json:"output"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return providerJourneyResult{}, fmt.Errorf("candidate emitted invalid SSE JSON")
		}
		switch event.Type {
		case "error":
			return providerJourneyResult{}, classifyProviderStreamError(event.ErrorText)
		case "text-delta":
			if event.Delta != "" {
				result.StreamedText = true
				text.WriteString(event.Delta)
			}
		case "tool-input-available":
			if event.ToolName == "bash" {
				result.CalledBash = true
				bashToolCallID = event.ToolCallID
			}
		case "tool-output-available":
			if bashToolCallID != "" && event.ToolCallID == bashToolCallID {
				result.ObservedOutput = true
				fmt.Fprint(&toolOutput, event.Output)
			}
		case "tool-output-error":
			if bashToolCallID != "" && event.ToolCallID == bashToolCallID {
				return providerJourneyResult{}, fmt.Errorf("controlled bash tool returned an error")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return providerJourneyResult{}, fmt.Errorf("read candidate Session SSE: %w", err)
	}
	result.CompletedStream = completedSentinel
	result.ObservedMarker = strings.Contains(text.String(), marker) &&
		strings.Contains(toolOutput.String(), marker)
	return result, nil
}

func (c *stellaCandidate) assertBashAvailable(ctx context.Context, agentID string) error {
	var response struct {
		Tools []struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		} `json:"tools"`
	}
	path := fmt.Sprintf("/api/agents/%s/tools", agentID)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, http.StatusOK, &response); err != nil {
		return err
	}
	for _, tool := range response.Tools {
		if tool.Name == "bash" && tool.Enabled {
			return nil
		}
	}
	return fmt.Errorf("candidate did not expose an enabled bash tool")
}

func (c *stellaCandidate) registerBootstrapUser(ctx context.Context) error {
	suffix := candidateID(c.runID, "bootstrap")
	password := "release-live-" + suffix
	return c.doJSON(ctx, http.MethodPost, "/api/auth/local/register", map[string]string{
		"name":             "Release Live " + suffix,
		"email":            suffix + "@release.test",
		"password":         password,
		"confirm_password": password,
	}, http.StatusOK, nil)
}

func (c *stellaCandidate) doJSON(
	ctx context.Context,
	method string,
	path string,
	body any,
	expectedStatus int,
	output any,
) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode %s request: %w", path, err)
		}
		reader = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build %s request: %w", path, err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("call candidate %s: %w", path, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != expectedStatus {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return fmt.Errorf(
			"candidate %s %s returned HTTP %d, want %d",
			method,
			path,
			response.StatusCode,
			expectedStatus,
		)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		return fmt.Errorf("decode candidate %s response: %w", path, err)
	}
	return nil
}

func (c *stellaCandidate) waitReady(ctx context.Context) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(candidateReadyTimeout)
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("candidate readiness canceled: %w", ctx.Err())
		case <-c.done:
			return fmt.Errorf("candidate exited before readiness")
		default:
		}
		response, err := client.Get(c.baseURL + "/readyz")
		if err == nil {
			ok := response.StatusCode == http.StatusOK
			_ = response.Body.Close()
			if ok {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("candidate was not ready within %s", candidateReadyTimeout)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func (c *stellaCandidate) Close(ctx context.Context) error {
	var cleanupErr error
	select {
	case <-c.done:
	default:
		if err := terminateLiveProcess(c.command.Process); err != nil {
			killLiveProcessGroup(c.command)
		}
		timer := time.NewTimer(candidateShutdownTimeout)
		select {
		case <-c.done:
			if !successfulCandidateExit(c.waitErr) {
				cleanupErr = fmt.Errorf("candidate exited non-zero during shutdown")
			}
		case <-ctx.Done():
			killLiveProcessGroup(c.command)
			cleanupErr = fmt.Errorf("candidate cleanup canceled")
		case <-timer.C:
			killLiveProcessGroup(c.command)
			cleanupErr = fmt.Errorf("candidate did not stop within %s", candidateShutdownTimeout)
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	if liveProcessGroupAlive(c.command) {
		killLiveProcessGroup(c.command)
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("candidate left descendant processes running"))
	}
	if err := os.RemoveAll(c.home); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove candidate home: %w", err))
	}
	return cleanupErr
}

func installedPostgresRuntime() (string, error) {
	source, ok := pgruntime.DefaultRuntimeSource()
	if !ok {
		return "", fmt.Errorf("no Stella PostgreSQL runtime exists for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	root := pgruntime.RuntimeRoot(filepath.Join(userHome, ".stella"), source)
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("stella PostgreSQL runtime is not installed")
	}
	return root, nil
}

func freeTCPPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve candidate port: %w", err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func candidateBaseEnv(home string) []string {
	// Keep the candidate independent from the developer or runner environment.
	// All paths used by Stella and the controlled bash command are available in
	// the standard Linux search path; the live job itself is Linux-only.
	return []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=" + home,
		"TMPDIR=" + filepath.Join(home, "tmp"),
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
	}
}

func candidateID(runID, suffix string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '-'
		}
	}, runID+"-"+suffix)
	clean = strings.Trim(clean, "-")
	for strings.Contains(clean, "--") {
		clean = strings.ReplaceAll(clean, "--", "-")
	}
	if len(clean) > 56 {
		clean = clean[:56]
	}
	return clean
}

func classifyProviderStreamError(raw string) error {
	text := strings.ToLower(raw)
	switch {
	case strings.Contains(text, "401"),
		strings.Contains(text, "unauthorized"),
		strings.Contains(text, "authentication"):
		return &providerExternalError{reason: "authentication was rejected"}
	case strings.Contains(text, "402"),
		strings.Contains(text, "quota"),
		strings.Contains(text, "insufficient"):
		return &providerExternalError{reason: "request quota was unavailable"}
	case strings.Contains(text, "404"),
		strings.Contains(text, "model not found"),
		strings.Contains(text, "does not exist"):
		return &providerExternalError{reason: "configured model was unavailable"}
	case strings.Contains(text, "429"),
		strings.Contains(text, "rate limit"):
		return &providerExternalError{reason: "rate limit blocked the request", retryable: true}
	case strings.Contains(text, "timeout"),
		strings.Contains(text, "deadline"),
		strings.Contains(text, "500"),
		strings.Contains(text, "502"),
		strings.Contains(text, "503"),
		strings.Contains(text, "504"):
		return &providerExternalError{reason: "upstream service was temporarily unavailable", retryable: true}
	default:
		return &providerExternalError{reason: "upstream stream returned an error"}
	}
}

func successfulCandidateExit(err error) bool {
	if err == nil {
		return true
	}
	var exitError *exec.ExitError
	return errors.As(err, &exitError) && exitError.Success()
}
