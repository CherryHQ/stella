package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/pkg/tools"
)

// Action name constants for the memory tool.
const (
	actionStatus          = "status"
	actionSearch          = "search"
	actionDescribe        = "describe"
	actionExpand          = "expand"
	actionSoulGet         = "soul_get"
	actionSoulUpdate      = "soul_update"
	actionProfileGet      = "profile_get"
	actionProfileUpdate   = "profile_update"
	actionProfileHistory  = "profile_history"
	actionProfileRollback = "profile_rollback"

	actionConstraintList   = "constraint_list"
	actionConstraintAdd    = "constraint_add"
	actionConstraintRemove = "constraint_remove"
)

// ToolOption configures the generated memory tool.
type ToolOption func(*toolConfig)

type toolConfig struct {
	readOnlyProfile bool
	readOnlySoul    bool
	actionsOnly     map[string]bool // nil means all available
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
	provider        Provider
	cfg             *toolConfig
	searcher        Searcher
	explorer        Explorer
	profileStore    ProfileStore
	changelogReader ChangelogReader
	changelogWriter ChangelogWriter
	constraintStore ConstraintStore
	snapshotStore   SessionSnapshotStore
	actions         []actionMeta
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
		add(actionSearch, "Search conversation history by keyword. Returns matching messages and summaries.")
	}

	if t.explorer != nil {
		add(actionDescribe, "Inspect a summary's content, metadata, and lineage (parents/children).")
		add(actionExpand, "Drill into a summary to retrieve original messages (leaf) or child summaries (condensed).")
	}

	if t.profileStore != nil {
		add(actionSoulGet, "Read the current agent soul (identity, personality, behavior) for this user+agent pair.")
		if !t.cfg.readOnlySoul {
			add(actionSoulUpdate, "Update the agent soul. Replaces entire content — include the full updated text.")
		}
		add(actionProfileGet, "Read the current user profile (facts and context about this user).")
		if !t.cfg.readOnlyProfile {
			add(actionProfileUpdate, "Update the user profile. Replaces entire content — include the full updated text.")
		}
		if t.changelogReader != nil {
			add(actionProfileHistory, "Show recent profile or soul change history. Use scope='profile' or scope='soul'.")
		}
		if t.changelogReader != nil && t.changelogWriter != nil && !t.cfg.readOnlyProfile {
			add(actionProfileRollback, "Roll back profile or soul to a previous version. Requires scope and version from profile_history.")
		}
	}

	if t.constraintStore != nil {
		add(actionConstraintList, "List all active constraints (hard rules the agent must follow).")
		add(actionConstraintAdd, "Add a new constraint. Only call after getting explicit user confirmation.")
		add(actionConstraintRemove, "Remove a constraint by its ID.")
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
		properties["limit"] = map[string]any{
			"type":        "integer",
			"description": "Maximum number of results to return (default 20). Only for search",
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

// requireProfileCtx validates that ProfileStore and user/agent context are available.
// It resolves strictly against the session user, so group turns (which have no
// session user, D9) fail closed. This backs soul, profile_history, and
// profile_rollback, which must never operate on a group member via fallback.
func (t *memoryTool) requireProfileCtx(ctx context.Context, action string) (string, string, error) {
	if t.profileStore == nil {
		return "", "", fmt.Errorf("memory %s: not supported by provider", action)
	}
	userID := UserIDFromContext(ctx)
	if userID == "" {
		return "", "", fmt.Errorf("memory %s: no user context", action)
	}
	agentID := AgentIDFromContext(ctx)
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
	agentID := AgentIDFromContext(ctx)
	if agentID == "" {
		return "", "", fmt.Errorf("memory %s: no agent context", action)
	}
	if userID := UserIDFromContext(ctx); userID != "" {
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
	userID := UserIDFromContext(ctx)
	if userID == "" {
		return "", "", fmt.Errorf("memory %s: no user context", action)
	}
	agentID := AgentIDFromContext(ctx)
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
	agentID := AgentIDFromContext(ctx)
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
	return Session{
		ID:      SessionIDFromContext(ctx),
		AgentID: AgentIDFromContext(ctx),
		UserID:  UserIDFromContext(ctx),
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
