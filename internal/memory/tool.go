package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/searchrank"
	"github.com/CherryHQ/stella/pkg/tools"
)

// Action name constants for the memory tool.
const (
	actionStatus          = "status"
	actionSearch          = "search"
	actionDescribe        = "describe"
	actionExpand          = "expand"
	actionGetMessage      = "get_message"
	actionSoulGet         = "soul_get"
	actionSoulUpdate      = "soul_update"
	actionProfileGet      = "profile_get"
	actionProfileUpdate   = "profile_update"
	actionProfileHistory  = "profile_history"
	actionProfileRollback = "profile_rollback"
	actionSearchKnowledge = "search_knowledge"

	actionConstraintList   = "constraint_list"
	actionConstraintAdd    = "constraint_add"
	actionConstraintRemove = "constraint_remove"
)

const runtimeUsageTouchTimeout = 500 * time.Millisecond

// ToolOption configures the generated memory tool.
type ToolOption func(*toolConfig)

type toolConfig struct {
	readOnlyProfile       bool
	readOnlySoul          bool
	sessionReadOnlyWrites bool
	actionsOnly           map[string]bool // nil means all available
}

// WithReadOnlyProfile disables the profile_update action.
// Used when building the tool for the self-improve reviewer agent,
// which should read but not arbitrarily overwrite profile notes.
func WithReadOnlyProfile() ToolOption {
	return func(c *toolConfig) {
		c.readOnlyProfile = true
	}
}

// WithReadOnlySoul disables the soul_update action.
func WithReadOnlySoul() ToolOption {
	return func(c *toolConfig) {
		c.readOnlySoul = true
	}
}

// WithSessionReadOnlyWrites removes actions that mutate durable memory from the
// model-facing session tool. Manual APIs and reflect use their own tool options.
func WithSessionReadOnlyWrites() ToolOption {
	return func(c *toolConfig) {
		c.sessionReadOnlyWrites = true
	}
}

// WithActionsOnly restricts the tool to the named actions only.
// Actions not in the list are omitted even if the provider supports them.
func WithActionsOnly(actions ...string) ToolOption {
	return func(c *toolConfig) {
		c.actionsOnly = make(map[string]bool, len(actions))
		for _, a := range actions {
			c.actionsOnly[a] = true
		}
	}
}

// BuildTool inspects provider capabilities and returns a memory tool
// whose available actions match exactly what the provider supports.
// If the provider supports no optional capabilities, the tool still exists
// but only offers a "status" action.
func BuildTool(provider Provider, opts ...ToolOption) tools.Tool {
	cfg := &toolConfig{}
	for _, o := range opts {
		o(cfg)
	}

	t := &memoryTool{
		provider: provider,
		cfg:      cfg,
	}

	// Discover capabilities via Unwrap so traced wrappers don't
	// advertise capabilities the inner provider does not have.
	// We check the inner provider but store typed references to the
	// outer (possibly traced) provider so tracing hooks fire on execution.
	inner := Unwrap(provider)
	if _, ok := inner.(Searcher); ok {
		t.searcher, _ = provider.(Searcher)
	}
	if _, ok := inner.(Explorer); ok {
		t.explorer, _ = provider.(Explorer)
	}
	if _, ok := inner.(MessageReader); ok {
		t.messageReader, _ = provider.(MessageReader)
	}
	if _, ok := inner.(ProfileStore); ok {
		t.profileStore, _ = provider.(ProfileStore)
	}
	if _, ok := inner.(ChangelogReader); ok {
		t.changelogReader, _ = provider.(ChangelogReader)
	}
	if _, ok := inner.(ChangelogWriter); ok {
		t.changelogWriter, _ = provider.(ChangelogWriter)
	}
	if _, ok := inner.(ConstraintStore); ok {
		t.constraintStore, _ = provider.(ConstraintStore)
	}
	if _, ok := inner.(SessionSnapshotStore); ok {
		t.snapshotStore, _ = provider.(SessionSnapshotStore)
	}
	if _, ok := inner.(FactStore); ok {
		t.factStore, _ = provider.(FactStore)
	}
	if _, ok := inner.(VersionedFactStore); ok {
		t.versionedFacts, _ = provider.(VersionedFactStore)
	}
	if _, ok := inner.(KnowledgeUsageTracker); ok {
		t.knowledgeUsageTracker, _ = provider.(KnowledgeUsageTracker)
	}

	// Build the list of available actions.
	t.actions = t.buildActions()

	return t
}

// actionMeta describes a single tool action for schema/description generation.
type actionMeta struct {
	name string
	desc string
}

// memoryTool implements tools.Tool with dynamic actions based on provider capabilities.
type memoryTool struct {
	provider              Provider
	cfg                   *toolConfig
	searcher              Searcher
	explorer              Explorer
	messageReader         MessageReader
	profileStore          ProfileStore
	changelogReader       ChangelogReader
	changelogWriter       ChangelogWriter
	constraintStore       ConstraintStore
	snapshotStore         SessionSnapshotStore
	factStore             FactStore
	versionedFacts        VersionedFactStore
	knowledgeUsageTracker KnowledgeUsageTracker
	actions               []actionMeta
}

func (t *memoryTool) buildActions() []actionMeta {
	var actions []actionMeta

	add := func(name, desc string) {
		if t.cfg.actionsOnly != nil && !t.cfg.actionsOnly[name] {
			return
		}
		actions = append(actions, actionMeta{name: name, desc: desc})
	}

	add(actionStatus, "Show session memory statistics: message count, token usage, summary count, time range.")

	if t.searcher != nil {
		add(actionSearch, "Search this user+agent's history by keyword across ALL past sessions, not just the current one. Each hit carries provenance: session_id and conversation_title for origin, and occurred_at (RFC3339) for when the content actually happened — use it to weight recency.")
	}

	if t.explorer != nil {
		add(actionDescribe, "Inspect a summary's content, metadata, and lineage (parents/children).")
		add(actionExpand, "Drill into a summary to retrieve original messages (leaf) or child summaries (condensed).")
	}

	if t.messageReader != nil {
		add(actionGetMessage, "Fetch one message in full by its ID (the source_id of a 'message' search hit). Use this to read a truncated search snippet in full, including hits from other past sessions of this user+agent.")
	}

	if t.profileStore != nil {
		add(actionSoulGet, "Read the current agent soul (identity, personality, behavior) for this user+agent pair.")
		if !t.cfg.sessionReadOnlyWrites && !t.cfg.readOnlySoul {
			add(actionSoulUpdate, "Update the agent soul. Replaces entire content — include the full updated text.")
		}
		add(actionProfileGet, "Read the current user profile (facts and context about this user).")
		if !t.cfg.sessionReadOnlyWrites && !t.cfg.readOnlyProfile {
			add(actionProfileUpdate, "Update the user profile. Replaces entire content — include the full updated text.")
		}
		if t.changelogReader != nil {
			add(actionProfileHistory, "Show recent profile or soul change history. Use scope='profile' or scope='soul'.")
		}
		if t.changelogReader != nil && t.changelogWriter != nil && !t.cfg.sessionReadOnlyWrites && !t.cfg.readOnlyProfile {
			add(actionProfileRollback, "Roll back profile or soul to a previous version. Requires scope and version from profile_history.")
		}
	}

	if t.constraintStore != nil {
		add(actionConstraintList, "List all active constraints (hard rules the agent must follow).")
		if !t.cfg.sessionReadOnlyWrites {
			add(actionConstraintAdd, "Add a new constraint. Only call after getting explicit user confirmation.")
			add(actionConstraintRemove, "Remove a constraint by its ID.")
		}
	}

	if t.factStore != nil {
		add(actionSearchKnowledge, "Search long-term knowledge facts (subject=world) visible to this DM session snapshot. Searches fact content only and never returns profile, soul, skills, or constraints.")
	}

	return actions
}

func (t *memoryTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "memory",
		Description: t.buildDescription(),
		InputSchema: t.buildInputSchema(),
	}
}

func (t *memoryTool) buildDescription() string {
	var b strings.Builder
	b.WriteString("Manage conversation memory.\n\nActions:\n")
	for _, a := range t.actions {
		fmt.Fprintf(&b, "- %s: %s\n", a.name, a.desc)
	}
	return b.String()
}

func (t *memoryTool) buildInputSchema() map[string]any {
	// Collect action names for the enum.
	names := make([]any, len(t.actions))
	for i, a := range t.actions {
		names[i] = a.name
	}

	// Build properties map. "action" is always present.
	properties := map[string]any{
		"action": map[string]any{
			"type":        "string",
			"enum":        names,
			"description": "The action to perform",
		},
	}

	// Add action-specific parameters.
	if t.hasAction(actionSearch) {
		properties["pattern"] = map[string]any{
			"type":        "string",
			"description": "Keywords to search for (required for search, full-text match ranked by relevance)",
		}
		properties["scope"] = map[string]any{
			"type":        "string",
			"enum":        []any{"messages", "summaries", "both"},
			"description": "Where to search: 'messages', 'summaries', or 'both' (default). Only for search",
		}
	}

	if t.hasAction(actionSearchKnowledge) {
		properties["query"] = map[string]any{
			"type":        "string",
			"description": "Fact-oriented query to search over snapshot-visible knowledge facts (required for search_knowledge)",
		}
	}

	if t.hasAction(actionSearch) || t.hasAction(actionSearchKnowledge) {
		properties["limit"] = map[string]any{
			"type":        "integer",
			"description": "Maximum number of results to return (default 20 for search, 10 for search_knowledge)",
		}
	}

	if t.hasAction(actionDescribe) || t.hasAction(actionExpand) {
		properties["summary_id"] = map[string]any{
			"type":        "string",
			"description": "The summary ID to inspect or expand (required for describe and expand)",
		}
	}

	if t.hasAction(actionExpand) {
		properties["token_cap"] = map[string]any{
			"type":        "integer",
			"description": "Maximum tokens of content to return (default 4000). Only for expand",
		}
	}

	if t.hasAction(actionGetMessage) {
		properties["message_id"] = map[string]any{
			"type":        "string",
			"description": "The message ID to fetch in full (required for get_message; use the source_id of a 'message' search hit)",
		}
	}

	if t.hasAction(actionProfileUpdate) || t.hasAction(actionSoulUpdate) {
		properties["content"] = map[string]any{
			"type":        "string",
			"description": "Full updated content. Replaces the entire existing value (required for profile_update and soul_update)",
		}
	}

	if t.hasAction(actionProfileHistory) || t.hasAction(actionProfileRollback) {
		properties["history_scope"] = map[string]any{
			"type":        "string",
			"enum":        []any{"profile", "soul"},
			"description": "Which memory type to inspect: 'profile' or 'soul' (required for profile_history and profile_rollback)",
		}
	}

	if t.hasAction(actionProfileHistory) {
		properties["history_limit"] = map[string]any{
			"type":        "integer",
			"description": "Maximum number of history entries to return (default 10). Only for profile_history",
		}
	}

	if t.hasAction(actionProfileRollback) {
		properties["rollback_version"] = map[string]any{
			"type":        "integer",
			"description": "Target version to roll back to (required for profile_rollback; get versions from profile_history)",
		}
	}

	if t.hasAction(actionConstraintAdd) {
		properties["constraint_text"] = map[string]any{
			"type":        "string",
			"description": "The text of the constraint to add (required for constraint_add)",
		}
	}

	if t.hasAction(actionConstraintRemove) {
		properties["constraint_id"] = map[string]any{
			"type":        "string",
			"description": "The ID of the constraint to remove (required for constraint_remove; get IDs from constraint_list)",
		}
	}

	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   []any{"action"},
	}
}

func (t *memoryTool) hasAction(name string) bool {
	for _, a := range t.actions {
		if a.name == name {
			return true
		}
	}
	return false
}

func (t *memoryTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action, _ := args["action"].(string)
	if !t.hasAction(action) {
		available := make([]string, len(t.actions))
		for i, a := range t.actions {
			available[i] = a.name
		}
		return "", fmt.Errorf("unknown action %q, available: %s", action, strings.Join(available, ", "))
	}

	switch action {
	case actionStatus:
		return t.execStatus(ctx)
	case actionSearch:
		return t.execSearch(ctx, args)
	case actionDescribe:
		return t.execDescribe(ctx, args)
	case actionExpand:
		return t.execExpand(ctx, args)
	case actionGetMessage:
		return t.execGetMessage(ctx, args)
	case actionSoulGet:
		return t.execSoulGet(ctx)
	case actionSoulUpdate:
		return t.execSoulUpdate(ctx, args)
	case actionProfileGet:
		return t.execProfileGet(ctx)
	case actionProfileUpdate:
		return t.execProfileUpdate(ctx, args)
	case actionProfileHistory:
		return t.execProfileHistory(ctx, args)
	case actionProfileRollback:
		return t.execProfileRollback(ctx, args)
	case actionConstraintList:
		return t.execConstraintList(ctx)
	case actionConstraintAdd:
		return t.execConstraintAdd(ctx, args)
	case actionConstraintRemove:
		return t.execConstraintRemove(ctx, args)
	case actionSearchKnowledge:
		return t.execSearchKnowledge(ctx, args)
	default:
		return "", fmt.Errorf("unhandled action %q", action)
	}
}

func (t *memoryTool) execStatus(ctx context.Context) (string, error) {
	session := sessionFromContext(ctx)
	stats, err := t.provider.Stats(ctx, session)
	if err != nil {
		return "", fmt.Errorf("memory status: %w", err)
	}
	return marshalJSON(stats)
}

func (t *memoryTool) execSearch(ctx context.Context, args map[string]any) (string, error) {
	if t.searcher == nil {
		return "", fmt.Errorf("memory search: not supported by provider")
	}

	pattern, ok := args["pattern"].(string)
	if !ok || pattern == "" {
		return "", fmt.Errorf("memory search: pattern is required")
	}

	query := SearchQuery{
		Text:  pattern,
		Scope: parseSearchScope(args),
		Limit: intArg(args, "limit", 20),
	}

	session := sessionFromContext(ctx)
	results, err := t.searcher.Search(ctx, session, query)
	if err != nil {
		return "", fmt.Errorf("memory search: %w", err)
	}

	if len(results) == 0 {
		return "No matches found.", nil
	}

	return marshalJSON(results)
}

type knowledgeSearchResult struct {
	FactID       string       `json:"fact_id"`
	Content      string       `json:"content"`
	Score        float64      `json:"score"`
	MatchedField string       `json:"matched_field"`
	Snippet      string       `json:"snippet"`
	Source       ChangeSource `json:"source"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

func (t *memoryTool) execSearchKnowledge(ctx context.Context, args map[string]any) (string, error) {
	if t.factStore == nil {
		return "", fmt.Errorf("memory search_knowledge: not supported by provider")
	}
	query, _ := args["query"].(string)
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("memory search_knowledge: query is required")
	}
	userID, agentID, err := t.requireKnowledgeCtx(ctx)
	if err != nil {
		return "", err
	}

	facts, err := t.searchKnowledgeFacts(ctx, userID, agentID)
	if err != nil {
		return "", err
	}
	results := rankKnowledgeFacts(query, facts, knowledgeSearchLimit(args))
	if len(results) == 0 {
		return "No knowledge facts found.", nil
	}
	t.touchReturnedKnowledgeUsage(ctx, userID, agentID, results)
	return marshalJSON(results)
}

func (t *memoryTool) touchReturnedKnowledgeUsage(ctx context.Context, userID string, agentID string, results []knowledgeSearchResult) {
	if t.knowledgeUsageTracker == nil {
		return
	}
	factIDs := make([]string, 0, len(results))
	for _, result := range results {
		if result.Source != SourceReflect {
			continue
		}
		factIDs = append(factIDs, result.FactID)
	}
	if len(factIDs) == 0 {
		return
	}
	touchCtx, cancel := context.WithTimeout(ctx, runtimeUsageTouchTimeout)
	defer cancel()
	if err := t.knowledgeUsageTracker.TouchKnowledgeUsage(touchCtx, userID, agentID, factIDs); err != nil {
		slog.WarnContext(ctx, "memory search_knowledge: failed to touch knowledge usage", "err", err)
	}
}

func (t *memoryTool) requireKnowledgeCtx(ctx context.Context) (string, string, error) {
	if t.factStore == nil {
		return "", "", fmt.Errorf("memory %s: not supported by provider", actionSearchKnowledge)
	}
	userID := authz.UserIDFromContext(ctx)
	if userID == "" {
		return "", "", fmt.Errorf("memory %s: no user context", actionSearchKnowledge)
	}
	agentID := authz.AgentIDFromContext(ctx)
	if agentID == "" {
		return "", "", fmt.Errorf("memory %s: no agent context", actionSearchKnowledge)
	}
	return userID, agentID, nil
}

func (t *memoryTool) searchKnowledgeFacts(ctx context.Context, userID string, agentID string) ([]Fact, error) {
	snapshotVersion := int64(0)
	hasSnapshot := false
	if t.snapshotStore != nil {
		if sessionID := SessionIDFromContext(ctx); sessionID != "" {
			snap, err := t.snapshotStore.GetOrCreateSessionSnapshot(ctx, sessionID, userID, agentID)
			if err != nil {
				return nil, fmt.Errorf("memory search_knowledge: get snapshot: %w", err)
			}
			snapshotVersion = snap.Version
			hasSnapshot = true
		}
	}
	if hasSnapshot {
		if t.versionedFacts == nil {
			return nil, fmt.Errorf("memory search_knowledge: snapshot facts are not supported by provider")
		}
		facts, err := t.versionedFacts.ListActiveFactsAt(ctx, userID, agentID, FactSubjectWorld, snapshotVersion)
		if err != nil {
			return nil, fmt.Errorf("memory search_knowledge: list snapshot facts: %w", err)
		}
		return filterWorldKnowledgeFacts(facts), nil
	}
	facts, err := t.factStore.ListActiveFacts(ctx, userID, agentID, FactSubjectWorld)
	if err != nil {
		return nil, fmt.Errorf("memory search_knowledge: list active facts: %w", err)
	}
	return filterWorldKnowledgeFacts(facts), nil
}

func filterWorldKnowledgeFacts(facts []Fact) []Fact {
	out := make([]Fact, 0, len(facts))
	for _, fact := range facts {
		if fact.Subject != FactSubjectWorld || fact.Scope != "user_agent" || fact.Status != FactStatusActive {
			continue
		}
		out = append(out, fact)
	}
	return out
}

func knowledgeSearchLimit(args map[string]any) int {
	const (
		defaultLimit = 10
		maxLimit     = 100
	)
	limit := intArg(args, "limit", defaultLimit)
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}

func rankKnowledgeFacts(query string, facts []Fact, limit int) []knowledgeSearchResult {
	docs := make([]searchrank.Document, 0, len(facts))
	byID := make(map[string]Fact, len(facts))
	for _, fact := range facts {
		byID[fact.ID] = fact
		docs = append(docs, searchrank.Document{
			ID: fact.ID,
			Fields: []searchrank.Field{
				{Name: "content", Text: fact.Content, Weight: 1},
			},
		})
	}
	ranked := searchrank.Rank(query, docs, limit)
	out := make([]knowledgeSearchResult, 0, len(ranked))
	for _, hit := range ranked {
		fact := byID[hit.ID]
		out = append(out, knowledgeSearchResult{
			FactID:       fact.ID,
			Content:      fact.Content,
			Score:        hit.Score,
			MatchedField: "content",
			Snippet:      hit.Snippet,
			Source:       fact.Source,
			UpdatedAt:    fact.UpdatedAt,
		})
	}
	return out
}

func (t *memoryTool) execDescribe(ctx context.Context, args map[string]any) (string, error) {
	if t.explorer == nil {
		return "", fmt.Errorf("memory describe: not supported by provider")
	}

	summaryID, ok := args["summary_id"].(string)
	if !ok || summaryID == "" {
		return "", fmt.Errorf("memory describe: summary_id is required")
	}

	result, err := t.explorer.Describe(ctx, summaryID)
	if err != nil {
		return "", fmt.Errorf("memory describe: %w", err)
	}

	return marshalJSON(result)
}

func (t *memoryTool) execExpand(ctx context.Context, args map[string]any) (string, error) {
	if t.explorer == nil {
		return "", fmt.Errorf("memory expand: not supported by provider")
	}

	summaryID, ok := args["summary_id"].(string)
	if !ok || summaryID == "" {
		return "", fmt.Errorf("memory expand: summary_id is required")
	}

	tokenCap := intArg(args, "token_cap", 4000)

	result, err := t.explorer.Expand(ctx, summaryID, tokenCap)
	if err != nil {
		return "", fmt.Errorf("memory expand: %w", err)
	}

	return marshalJSON(result)
}

func (t *memoryTool) execGetMessage(ctx context.Context, args map[string]any) (string, error) {
	if t.messageReader == nil {
		return "", fmt.Errorf("memory get_message: not supported by provider")
	}

	messageID, ok := args["message_id"].(string)
	if !ok || messageID == "" {
		return "", fmt.Errorf("memory get_message: message_id is required")
	}

	result, err := t.messageReader.GetMessage(ctx, messageID)
	if err != nil {
		return "", fmt.Errorf("memory get_message: %w", err)
	}

	return marshalJSON(result)
}

// requireProfileCtx validates that ProfileStore and user/agent context are available.
// It resolves strictly against the session user, so group turns (which have no
// session user, D9) fail closed. This backs soul, profile_history, and
// profile_rollback, which must never operate on a group member via fallback.
func (t *memoryTool) requireProfileCtx(ctx context.Context, action string) (string, string, error) {
	if t.profileStore == nil {
		return "", "", fmt.Errorf("memory %s: not supported by provider", action)
	}
	userID := authz.UserIDFromContext(ctx)
	if userID == "" {
		return "", "", fmt.Errorf("memory %s: no user context", action)
	}
	agentID := authz.AgentIDFromContext(ctx)
	if agentID == "" {
		return "", "", fmt.Errorf("memory %s: no agent context", action)
	}
	return userID, agentID, nil
}

// resolveProfileTarget resolves the (userID, agentID) a profile action should
// target. Normal sessions use the session user. Group turns have no session user
// (runtime identity is the group, D9), so profile_get / profile_update fall back
// to the current speaker's linked auth user. Only those two actions use this
// resolver; soul, constraints, and history/rollback stay on requireProfileCtx
// and therefore reject the speaker fallback by construction.
func (t *memoryTool) resolveProfileTarget(ctx context.Context, action string) (string, string, error) {
	if t.profileStore == nil {
		return "", "", fmt.Errorf("memory %s: not supported by provider", action)
	}
	agentID := authz.AgentIDFromContext(ctx)
	if agentID == "" {
		return "", "", fmt.Errorf("memory %s: no agent context", action)
	}
	if userID := authz.UserIDFromContext(ctx); userID != "" {
		return userID, agentID, nil
	}
	if speaker, ok := CurrentSpeakerFromContext(ctx); ok && speaker.UserID != "" {
		return speaker.UserID, agentID, nil
	}
	return "", "", fmt.Errorf("memory %s: no linked current speaker", action)
}

// ctxTargetResolver resolves the (userID, agentID) a store action targets.
type ctxTargetResolver func(ctx context.Context, action string) (string, string, error)

func (t *memoryTool) execStoreGet(
	ctx context.Context,
	action string,
	resolve ctxTargetResolver,
	getter func(context.Context, string, string) (string, error),
	emptyMsg string,
) (string, error) {
	userID, agentID, err := resolve(ctx, action)
	if err != nil {
		return "", err
	}
	content, err := getter(ctx, userID, agentID)
	if err != nil {
		return "", fmt.Errorf("memory %s: %w", action, err)
	}
	if content == "" {
		return emptyMsg, nil
	}
	return content, nil
}

// execStoreUpdate runs a profile/soul write and returns the resolved target
// userID so the caller can advance the right snapshot row (the session user for
// DM/soul writes, the current speaker for group profile writes).
func (t *memoryTool) execStoreUpdate(
	ctx context.Context,
	args map[string]any,
	action string,
	resolve ctxTargetResolver,
	setter func(context.Context, string, string, string) error,
	successMsg string,
) (string, string, error) {
	content, _ := args["content"].(string)
	if content == "" {
		return "", "", fmt.Errorf("memory %s: content is required", action)
	}
	userID, agentID, err := resolve(ctx, action)
	if err != nil {
		return "", "", err
	}
	if err := setter(ctx, userID, agentID, content); err != nil {
		return "", "", fmt.Errorf("memory %s: %w", action, err)
	}
	return successMsg, userID, nil
}

// profile_get / profile_update use resolveProfileTarget — the ONLY two actions
// allowed to fall back to the group current speaker. soul_* / constraint_* /
// profile_history / profile_rollback deliberately stay on requireProfileCtx so
// they fail closed in group turns (D9): a public room must not read or rewrite a
// member's soul, constraints, or history through the shared agent. Do not switch
// a soul/constraint/history action to resolveProfileTarget.
func (t *memoryTool) execProfileGet(ctx context.Context) (string, error) {
	return t.execStoreGet(ctx, actionProfileGet, t.resolveProfileTarget, t.profileStore.GetProfile, "No profile notes found.")
}

func (t *memoryTool) execSoulGet(ctx context.Context) (string, error) {
	return t.execStoreGet(ctx, actionSoulGet, t.requireProfileCtx, t.profileStore.GetAgentSoul, "No agent soul defined.")
}

func (t *memoryTool) execProfileUpdate(ctx context.Context, args map[string]any) (string, error) {
	// Speaker fallback intentional here (see execProfileGet); forbidden for soul.
	result, userID, err := t.execStoreUpdate(ctx, args, actionProfileUpdate, t.resolveProfileTarget, t.profileStore.SetProfile,
		"Profile updated. Changes will appear in the system prompt at the next session start.")
	if err == nil {
		t.advanceSnapshot(ctx, userID)
	}
	return result, err
}

func (t *memoryTool) execSoulUpdate(ctx context.Context, args map[string]any) (string, error) {
	result, userID, err := t.execStoreUpdate(ctx, args, actionSoulUpdate, t.requireProfileCtx, t.profileStore.SetAgentSoul,
		"Agent soul updated. Changes will appear in the system prompt at the next session start.")
	if err == nil {
		t.advanceSnapshot(ctx, userID)
	}
	return result, err
}

func (t *memoryTool) execProfileHistory(ctx context.Context, args map[string]any) (string, error) {
	if t.changelogReader == nil {
		return "", fmt.Errorf("memory profile_history: not supported by provider")
	}
	userID, agentID, err := t.requireProfileCtx(ctx, actionProfileHistory)
	if err != nil {
		return "", err
	}
	scope, _ := args["history_scope"].(string)
	if scope != "profile" && scope != "soul" {
		scope = "profile"
	}
	limit := intArg(args, "history_limit", 10)
	entries, err := t.changelogReader.ReadChangelog(ctx, userID, agentID, scope, limit)
	if err != nil {
		return "", fmt.Errorf("memory profile_history: %w", err)
	}
	if len(entries) == 0 {
		return "No history found.", nil
	}
	return marshalJSON(entries)
}

func (t *memoryTool) execProfileRollback(ctx context.Context, args map[string]any) (string, error) {
	if t.changelogReader == nil || t.changelogWriter == nil || t.profileStore == nil {
		return "", fmt.Errorf("memory profile_rollback: not supported by provider")
	}
	userID, agentID, err := t.requireProfileCtx(ctx, actionProfileRollback)
	if err != nil {
		return "", err
	}

	scope, _ := args["history_scope"].(string)
	if scope != "profile" && scope != "soul" {
		return "", fmt.Errorf("memory profile_rollback: history_scope must be 'profile' or 'soul'")
	}

	version := intArg(args, "rollback_version", 0)
	if version <= 0 {
		return "", fmt.Errorf("memory profile_rollback: rollback_version must be a positive integer")
	}

	// Look up the text at that version.
	entries, err := t.changelogReader.ReadChangelog(ctx, userID, agentID, scope, 100)
	if err != nil {
		return "", fmt.Errorf("memory profile_rollback: read history: %w", err)
	}

	var targetText string
	found := false
	for _, e := range entries {
		if e.MemoryVersionAfter != nil && *e.MemoryVersionAfter == int64(version) {
			targetText = e.AfterText
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("memory profile_rollback: version %d not found in history", version)
	}

	// Write the rollback as a new update.
	rollbackCtx := WithChangeSource(ctx, SourceUser)
	switch scope {
	case "profile":
		if err := t.profileStore.SetProfile(rollbackCtx, userID, agentID, targetText); err != nil {
			return "", fmt.Errorf("memory profile_rollback: %w", err)
		}
	case "soul":
		if err := t.profileStore.SetAgentSoul(rollbackCtx, userID, agentID, targetText); err != nil {
			return "", fmt.Errorf("memory profile_rollback: %w", err)
		}
	}
	t.advanceSnapshot(ctx, userID)

	return fmt.Sprintf("Rolled back %s to version %d.", scope, version), nil
}

func (t *memoryTool) requireConstraintCtx(ctx context.Context, action string) (string, string, error) {
	if t.constraintStore == nil {
		return "", "", fmt.Errorf("memory %s: not supported by provider", action)
	}
	userID := authz.UserIDFromContext(ctx)
	if userID == "" {
		return "", "", fmt.Errorf("memory %s: no user context", action)
	}
	agentID := authz.AgentIDFromContext(ctx)
	if agentID == "" {
		return "", "", fmt.Errorf("memory %s: no agent context", action)
	}
	return userID, agentID, nil
}

func (t *memoryTool) execConstraintList(ctx context.Context) (string, error) {
	userID, agentID, err := t.requireConstraintCtx(ctx, actionConstraintList)
	if err != nil {
		return "", err
	}
	entries, err := t.constraintStore.GetConstraints(ctx, userID, agentID)
	if err != nil {
		return "", fmt.Errorf("memory constraint_list: %w", err)
	}
	if len(entries) == 0 {
		return "No constraints set.", nil
	}
	return marshalJSON(entries)
}

func (t *memoryTool) execConstraintAdd(ctx context.Context, args map[string]any) (string, error) {
	text, _ := args["constraint_text"].(string)
	if text == "" {
		return "", fmt.Errorf("memory constraint_add: constraint_text is required")
	}
	userID, agentID, err := t.requireConstraintCtx(ctx, actionConstraintAdd)
	if err != nil {
		return "", err
	}
	entries, err := t.constraintStore.AddConstraint(ctx, userID, agentID, text)
	if err != nil {
		return "", fmt.Errorf("memory constraint_add: %w", err)
	}
	t.advanceSnapshot(ctx, userID)
	return marshalJSON(entries)
}

func (t *memoryTool) execConstraintRemove(ctx context.Context, args map[string]any) (string, error) {
	id, _ := args["constraint_id"].(string)
	if id == "" {
		return "", fmt.Errorf("memory constraint_remove: constraint_id is required")
	}
	userID, agentID, err := t.requireConstraintCtx(ctx, actionConstraintRemove)
	if err != nil {
		return "", err
	}
	entries, err := t.constraintStore.RemoveConstraint(ctx, userID, agentID, id)
	if err != nil {
		return "", fmt.Errorf("memory constraint_remove: %w", err)
	}
	t.advanceSnapshot(ctx, userID)
	return marshalJSON(entries)
}

// advanceSnapshot advances the session snapshot for the given profile subject
// after a front-end write. userID is the resolved write target — the session
// user for DM/soul/constraint writes, or the current speaker for group profile
// writes — so the speaker snapshot row is advanced, never the group_id row.
// Reflect writes don't carry session_id so they naturally skip this.
func (t *memoryTool) advanceSnapshot(ctx context.Context, userID string) {
	if t.snapshotStore == nil {
		return
	}
	sessionID := SessionIDFromContext(ctx)
	if sessionID == "" {
		return
	}
	agentID := authz.AgentIDFromContext(ctx)
	if userID == "" || agentID == "" {
		return
	}
	// Log but don't fail — snapshot advance failure should not block the write.
	_ = t.snapshotStore.AdvanceSessionSnapshot(ctx, sessionID, userID, agentID)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// sessionFromContext builds a Session from context values.
func sessionFromContext(ctx context.Context) Session {
	groupID := authz.GroupIDFromContext(ctx)
	userID := authz.UserIDFromContext(ctx)
	if userID == "" && groupID != "" {
		// Group LCM rows use the group UUID as their durable owner key. This is
		// storage scoping only: the runtime context remains a group identity and
		// never receives a synthetic authenticated user.
		userID = groupID
	}
	return Session{
		ID:      SessionIDFromContext(ctx),
		AgentID: authz.AgentIDFromContext(ctx),
		UserID:  userID,
		GroupID: groupID,
	}
}

// parseSearchScope converts the scope string arg to SearchScope.
func parseSearchScope(args map[string]any) SearchScope {
	scope, _ := args["scope"].(string)
	switch scope {
	case "messages":
		return SearchScopeMessages
	case "summaries":
		return SearchScopeSummaries
	default:
		return SearchScopeBoth
	}
}

// intArg extracts an integer argument with a default value.
func intArg(args map[string]any, key string, defaultVal int) int {
	v, ok := args[key]
	if !ok {
		return defaultVal
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return defaultVal
	}
}

// marshalJSON marshals v to indented JSON string.
func marshalJSON(v any) (string, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	return string(out), nil
}
