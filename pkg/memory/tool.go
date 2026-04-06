package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vaayne/anna/pkg/tools"
)

// ToolOption configures the generated memory tool.
type ToolOption func(*toolConfig)

type toolConfig struct {
	readOnlyProfile bool
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

	// Discover capabilities.
	t.searcher, _ = provider.(Searcher)
	t.explorer, _ = provider.(Explorer)
	t.profileStore, _ = provider.(ProfileStore)

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
	provider     Provider
	cfg          *toolConfig
	searcher     Searcher
	explorer     Explorer
	profileStore ProfileStore
	actions      []actionMeta
}

func (t *memoryTool) buildActions() []actionMeta {
	var actions []actionMeta

	add := func(name, desc string) {
		if t.cfg.actionsOnly != nil && !t.cfg.actionsOnly[name] {
			return
		}
		actions = append(actions, actionMeta{name: name, desc: desc})
	}

	add("status", "Show session memory statistics: message count, token usage, summary count, time range.")

	if t.searcher != nil {
		add("search", "Search conversation history by keyword. Returns matching messages and summaries.")
	}

	if t.explorer != nil {
		add("describe", "Inspect a summary's content, metadata, and lineage (parents/children).")
		add("expand", "Drill into a summary to retrieve original messages (leaf) or child summaries (condensed).")
	}

	if t.profileStore != nil {
		add("profile_get", "Read the current persistent profile notes for this user+agent pair.")
		if !t.cfg.readOnlyProfile {
			add("profile_update", "Update persistent profile notes. Replaces entire content — include the full updated text.")
		}
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
	if t.hasAction("search") {
		properties["pattern"] = map[string]any{
			"type":        "string",
			"description": "Text pattern to search for (required for search, case-insensitive substring match)",
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

	if t.hasAction("describe") || t.hasAction("expand") {
		properties["summary_id"] = map[string]any{
			"type":        "string",
			"description": "The summary ID to inspect or expand (required for describe and expand)",
		}
	}

	if t.hasAction("expand") {
		properties["token_cap"] = map[string]any{
			"type":        "integer",
			"description": "Maximum tokens of content to return (default 4000). Only for expand",
		}
	}

	if t.hasAction("profile_update") {
		properties["content"] = map[string]any{
			"type":        "string",
			"description": "Full updated profile content. Replaces the entire existing profile (required for profile_update)",
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
	case "status":
		return t.execStatus(ctx)
	case "search":
		return t.execSearch(ctx, args)
	case "describe":
		return t.execDescribe(ctx, args)
	case "expand":
		return t.execExpand(ctx, args)
	case "profile_get":
		return t.execProfileGet(ctx)
	case "profile_update":
		return t.execProfileUpdate(ctx, args)
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

func (t *memoryTool) execProfileGet(ctx context.Context) (string, error) {
	if t.profileStore == nil {
		return "", fmt.Errorf("memory profile_get: not supported by provider")
	}

	userID := UserIDFromContext(ctx)
	agentID := AgentIDFromContext(ctx)
	if userID == 0 {
		return "", fmt.Errorf("memory profile_get: no user context")
	}
	if agentID == "" {
		return "", fmt.Errorf("memory profile_get: no agent context")
	}

	content, err := t.profileStore.GetProfile(ctx, userID, agentID)
	if err != nil {
		return "", fmt.Errorf("memory profile_get: %w", err)
	}

	if content == "" {
		return "No profile notes found.", nil
	}

	return content, nil
}

func (t *memoryTool) execProfileUpdate(ctx context.Context, args map[string]any) (string, error) {
	if t.profileStore == nil {
		return "", fmt.Errorf("memory profile_update: not supported by provider")
	}

	content, _ := args["content"].(string)
	if content == "" {
		return "", fmt.Errorf("memory profile_update: content is required")
	}

	userID := UserIDFromContext(ctx)
	agentID := AgentIDFromContext(ctx)
	if userID == 0 {
		return "", fmt.Errorf("memory profile_update: no user context")
	}
	if agentID == "" {
		return "", fmt.Errorf("memory profile_update: no agent context")
	}

	if err := t.profileStore.SetProfile(ctx, userID, agentID, content); err != nil {
		return "", fmt.Errorf("memory profile_update: %w", err)
	}

	return "Profile updated. Changes will appear in the system prompt at the next session start.", nil
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
