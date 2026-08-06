//go:build personamemeval

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/tools"
)

const (
	memoryBenchmarkToolName               = "memory"
	memoryBenchmarkBudgetExhaustedMessage = "Memory lookup budget exhausted. Answer now using evidence already retrieved."
)

type memoryBenchmarkKnowledgeUsageRow struct {
	FactID     string    `json:"fact_id"`
	LastUsedAt time.Time `json:"last_used_at"`
	CreatedAt  time.Time `json:"created_at"`
}

type memoryBenchmarkPendingQuestion struct {
	QAIndex   int                                `json:"qa_index"`
	SessionID string                             `json:"session_id"`
	Usage     []memoryBenchmarkKnowledgeUsageRow `json:"usage"`
}

type memoryBenchmarkRemoteModelMetadata struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// frozenMemoryBenchmarkTool prevents a benchmark question from retrieving
// messages written by its own temporary QA session while preserving production
// memory behavior for the frozen conversation history.
type frozenMemoryBenchmarkTool struct {
	inner            tools.Tool
	blockedSessionID string
	maxCalls         int32
	calls            atomic.Int32
}

func newFrozenMemoryBenchmarkTool(provider memory.Provider, blockedSessionID string, maxCalls int) tools.Tool {
	return &frozenMemoryBenchmarkTool{
		inner:            memory.BuildTool(provider, memory.WithSessionReadOnlyWrites()),
		blockedSessionID: blockedSessionID,
		maxCalls:         int32(maxCalls),
	}
}

func (tool *frozenMemoryBenchmarkTool) Definition() tools.Definition {
	return tool.inner.Definition()
}

func (tool *frozenMemoryBenchmarkTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if tool.calls.Add(1) > tool.maxCalls {
		return memoryBenchmarkBudgetExhaustedMessage, nil
	}
	if action, _ := args["action"].(string); action != "search" {
		return tool.inner.Execute(ctx, args)
	}

	requestedLimit := memoryBenchmarkIntArg(args, "limit", 20)
	searchArgs := make(map[string]any, len(args))
	for key, value := range args {
		searchArgs[key] = value
	}
	// Overfetch before removing current-session rows so historical hits can
	// still fill the model-requested result count.
	searchArgs["limit"] = min(requestedLimit+64, 100)
	result, err := tool.inner.Execute(ctx, searchArgs)
	if err != nil || result == "No matches found." {
		return result, err
	}

	var hits []memory.SearchResult
	if err := json.Unmarshal([]byte(result), &hits); err != nil {
		return "", fmt.Errorf("decode benchmark memory search: %w", err)
	}
	filtered := make([]memory.SearchResult, 0, min(len(hits), requestedLimit))
	for _, hit := range hits {
		if hit.SessionID == tool.blockedSessionID {
			continue
		}
		filtered = append(filtered, hit)
		if len(filtered) == requestedLimit {
			break
		}
	}
	if len(filtered) == 0 {
		return "No matches found.", nil
	}
	payload, err := json.MarshalIndent(filtered, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode benchmark memory search: %w", err)
	}
	return string(payload), nil
}

func memoryBenchmarkIntArg(args map[string]any, key string, defaultValue int) int {
	value, ok := args[key]
	if !ok {
		return defaultValue
	}
	var result int
	switch number := value.(type) {
	case int:
		result = number
	case float64:
		result = int(number)
	case json.Number:
		parsed, err := strconv.Atoi(number.String())
		if err != nil {
			return defaultValue
		}
		result = parsed
	default:
		return defaultValue
	}
	if result <= 0 {
		return defaultValue
	}
	return result
}

func configureMemoryBenchmarkAgent(
	ctx context.Context,
	setupState *setupResult,
	agentID string,
	agentName string,
	providerID string,
	modelID string,
) (config.Agent, error) {
	agentCfg, err := setupState.store.GetAgent(ctx, agentID)
	if err != nil {
		return config.Agent{}, fmt.Errorf("load benchmark agent %q: %w", agentID, err)
	}
	modelRef := providerID + "/" + modelID
	agentCfg.Name = agentName
	agentCfg.Enabled = true
	agentCfg.SystemPrompt = ""
	agentCfg.Soul = ""
	agentCfg.Workspace = ""
	agentCfg.Sandbox = config.SandboxConfig{}
	agentCfg.Scope = config.AgentScopeSystem
	agentCfg.Model = modelRef
	agentCfg.ModelStrong = modelRef
	agentCfg.ModelFast = modelRef
	agentCfg.ModelThinking = ""
	agentCfg.ModelStrongThinking = ""
	agentCfg.ModelFastThinking = ""
	if err := setupState.store.UpdateAgent(ctx, agentCfg); err != nil {
		return config.Agent{}, fmt.Errorf("configure benchmark agent: %w", err)
	}
	if err := setupState.poolManager.SyncAgent(ctx, agentCfg.ID); err != nil {
		return config.Agent{}, fmt.Errorf("reload benchmark agent: %w", err)
	}
	return agentCfg, nil
}

func collectMemoryBenchmarkAnswer(stream <-chan runtime.Event) (string, []string, error) {
	var current strings.Builder
	var fallback strings.Builder
	final := ""
	toolSet := make(map[string]struct{})
	for event := range stream {
		if event.Err != nil {
			return "", nil, event.Err
		}
		if event.Step != nil && event.Step.Kind == "start" {
			current.Reset()
		}
		if event.Text != "" {
			current.WriteString(event.Text)
			fallback.WriteString(event.Text)
		}
		if event.ToolUse != nil && event.ToolUse.Tool != "" {
			toolSet[event.ToolUse.Tool] = struct{}{}
		}
		if event.Step != nil && event.Step.Kind == "finish" {
			if text := strings.TrimSpace(current.String()); text != "" {
				final = text
			}
		}
	}
	if final == "" {
		final = strings.TrimSpace(fallback.String())
	}
	toolsUsed := make([]string, 0, len(toolSet))
	for name := range toolSet {
		toolsUsed = append(toolsUsed, name)
	}
	sort.Strings(toolsUsed)
	return final, toolsUsed, nil
}

func snapshotMemoryBenchmarkKnowledgeUsage(
	ctx context.Context,
	setupState *setupResult,
	userID string,
	agentID string,
) ([]memoryBenchmarkKnowledgeUsageRow, error) {
	rows, err := setupState.db.Query(ctx, `
		SELECT fact_id::text, last_used_at, created_at
		FROM knowledge_usage
		WHERE user_id = $1 AND agent_id = $2
		ORDER BY fact_id`, userID, agentID)
	if err != nil {
		return nil, fmt.Errorf("snapshot knowledge usage: %w", err)
	}
	defer rows.Close()
	var result []memoryBenchmarkKnowledgeUsageRow
	for rows.Next() {
		var item memoryBenchmarkKnowledgeUsageRow
		if err := rows.Scan(&item.FactID, &item.LastUsedAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func restoreMemoryBenchmarkKnowledgeUsage(
	ctx context.Context,
	setupState *setupResult,
	userID string,
	agentID string,
	usage []memoryBenchmarkKnowledgeUsageRow,
) error {
	tx, err := setupState.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM knowledge_usage WHERE user_id = $1 AND agent_id = $2`, userID, agentID); err != nil {
		return fmt.Errorf("clear knowledge usage: %w", err)
	}
	for _, item := range usage {
		if _, err := tx.Exec(ctx, `
			INSERT INTO knowledge_usage (fact_id, user_id, agent_id, last_used_at, created_at)
			VALUES ($1, $2, $3, $4, $5)`, item.FactID, userID, agentID, item.LastUsedAt, item.CreatedAt); err != nil {
			return fmt.Errorf("restore knowledge usage %s: %w", item.FactID, err)
		}
	}
	return tx.Commit(ctx)
}

func recoverMemoryBenchmarkPendingQuestion(
	ctx context.Context,
	setupState *setupResult,
	userID string,
	agentID string,
	pending *memoryBenchmarkPendingQuestion,
) error {
	if pending == nil {
		return nil
	}
	if service := setupState.poolManager.GetService(agentID); service != nil {
		_ = service.Runtime.CloseSession(ctx, pending.SessionID)
	}
	tx, err := setupState.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM ctx_agent_memory_snapshot WHERE session_id = $1`, pending.SessionID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM plugin_state WHERE scope_kind = 'session' AND scope_id = $1`, pending.SessionID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM ctx_conversation WHERE session_id = $1 AND user_id = $2 AND agent_id = $3`, pending.SessionID, userID, agentID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return restoreMemoryBenchmarkKnowledgeUsage(ctx, setupState, userID, agentID, pending.Usage)
}

func memoryBenchmarkPairDigest(
	ctx context.Context,
	setupState *setupResult,
	userID string,
	agentID string,
) (string, error) {
	queries := []string{
		`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY t.id)::text, '[]') FROM (SELECT * FROM facts WHERE user_id=$1 AND agent_id=$2) t`,
		`SELECT COALESCE(jsonb_agg(to_jsonb(t))::text, '[]') FROM (SELECT * FROM ctx_agent_memory WHERE user_id=$1 AND agent_id=$2) t`,
		`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY t.id)::text, '[]') FROM (SELECT * FROM ctx_agent_memory_changelog WHERE user_id=$1 AND agent_id=$2) t`,
		`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY t.fact_id)::text, '[]') FROM (SELECT * FROM knowledge_usage WHERE user_id=$1 AND agent_id=$2) t`,
		`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY t.session_id)::text, '[]') FROM (SELECT * FROM ctx_agent_memory_snapshot WHERE user_id=$1 AND agent_id=$2) t`,
		`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY t.id)::text, '[]') FROM (SELECT * FROM skill WHERE user_id=$1 AND agent_id=$2) t`,
		`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY t.skill_id)::text, '[]') FROM (SELECT * FROM skill_usage WHERE user_id=$1 AND agent_id=$2) t`,
	}
	hash := sha256.New()
	for _, query := range queries {
		var payload string
		if err := setupState.db.QueryRow(ctx, query, userID, agentID).Scan(&payload); err != nil {
			return "", fmt.Errorf("digest pair state: %w", err)
		}
		_, _ = hash.Write([]byte(payload))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func resetMemoryBenchmarkPair(
	ctx context.Context,
	setupState *setupResult,
	userID string,
	agentID string,
	name string,
) error {
	if service := setupState.poolManager.GetService(agentID); service != nil {
		_ = service.Runtime.ResetRunnersForUser(userID)
	}
	tx, err := setupState.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		DELETE FROM plugin_state
		WHERE scope_kind = 'session'
		  AND scope_id IN (SELECT session_id FROM ctx_conversation WHERE user_id=$1 AND agent_id=$2)`, userID, agentID); err != nil {
		return fmt.Errorf("delete pair plugin state: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM ctx_agent_memory_snapshot WHERE user_id=$1 AND agent_id=$2`, userID, agentID); err != nil {
		return fmt.Errorf("delete pair snapshots: %w", err)
	}
	// Conversation rows intentionally have no auth_user cascade.
	if _, err := tx.Exec(ctx, `DELETE FROM ctx_conversation WHERE user_id=$1 AND agent_id=$2`, userID, agentID); err != nil {
		return fmt.Errorf("delete pair conversations: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM auth_user WHERE id=$1`, userID); err != nil {
		return fmt.Errorf("delete benchmark user: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return createMemoryBenchmarkUser(ctx, setupState, userID, agentID, name)
}

func ensureMemoryBenchmarkUser(
	ctx context.Context,
	setupState *setupResult,
	userID string,
	agentID string,
	name string,
) error {
	var exists bool
	if err := setupState.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_user WHERE id=$1)`, userID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return createMemoryBenchmarkUser(ctx, setupState, userID, agentID, name)
	}
	return setupState.authStore.AssignAgent(ctx, userID, agentID)
}

func createMemoryBenchmarkUser(
	ctx context.Context,
	setupState *setupResult,
	userID string,
	agentID string,
	name string,
) error {
	_, err := setupState.authStore.CreateUser(ctx, auth.User{
		ID: userID, Email: fmt.Sprintf("benchmark-%s@benchmark.invalid", userID), Name: name, Role: auth.RoleUser,
	})
	if err != nil {
		return fmt.Errorf("create benchmark user: %w", err)
	}
	if err := setupState.authStore.AssignAgent(ctx, userID, agentID); err != nil {
		return fmt.Errorf("assign benchmark agent: %w", err)
	}
	return nil
}

func inspectMemoryBenchmarkRemoteModelMetadata(
	ctx context.Context,
	providerCfg config.Provider,
	modelID string,
) ([]memoryBenchmarkRemoteModelMetadata, error) {
	endpoint := strings.TrimRight(providerCfg.BaseURL, "/") + "/models"
	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+providerCfg.APIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("model metadata request returned %s", resp.Status)
	}
	var payload struct {
		Data []memoryBenchmarkRemoteModelMetadata `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	metadata := make([]memoryBenchmarkRemoteModelMetadata, 0, 1)
	for _, model := range payload.Data {
		if model.ID == modelID {
			metadata = append(metadata, model)
		}
	}
	return metadata, nil
}

func writeMemoryBenchmarkJSONAtomic(path string, value any) error {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, payload, 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func isMemoryBenchmarkTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return !errors.Is(err, context.Canceled)
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"unexpected eof", "connection reset", "temporarily unavailable", "timeout", "timed out",
		"context deadline exceeded",
		"status code 429", "status code 500", "status code 502", "status code 503", "status code 504",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return errors.Is(err, pgx.ErrTxClosed)
}
