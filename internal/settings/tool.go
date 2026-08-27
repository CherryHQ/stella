// Package settings exposes the small, read-only settings surface owned by Stella.
// Mutation protocols and deployment-wide settings stay out of this first slice.
package settings

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/tools"
)

const (
	// AgentID is the stable runtime identity of Stella's built-in assistant.
	AgentID  = "stella"
	toolName = agent.StellaSettingsToolName

	maxAgentListSummaryBytes   = 256
	maxAgentDetailTextBytes    = 4 * 1024
	defaultAgentListPageSize   = 20
	maxAgentListPageSize       = 100
	maxAgentSerializedResult   = 96 * 1024
	maxAgentProjectedTextBytes = 256
)

var ErrUnavailable = errors.New("stella settings is unavailable")

// agentReader is the existing Agent PEP. Keeping the tool on this narrow port
// prevents it from reading config.Store directly and bypassing ownership rules.
type agentReader interface {
	ListReadable(context.Context, authz.Authority, bool) ([]config.Agent, error)
	Read(context.Context, authz.Authority, string) (config.Agent, error)
}

// boundedAgentReader is implemented by the Agent PEP's model-facing port. The
// fallback keeps small test doubles and legacy integrations source-compatible,
// while production wiring uses the bounded storage projection.
type boundedAgentReader interface {
	ListReadableProjection(context.Context, authz.Authority, bool) ([]config.Agent, error)
	ReadProjection(context.Context, authz.Authority, string) (config.Agent, error)
}

type Tool struct {
	agents agentReader
}

func NewTool(agents agentReader) *Tool {
	return &Tool{agents: agents}
}

// Available is the registry gate. Execute repeats the same checks because
// registry visibility is not a security boundary and a tool can be invoked by
// a miswired caller or an override.
func Available(_ context.Context, params agent.RunnerParams) bool {
	return params.ForegroundHuman && params.UserID != "" && params.AgentID == AgentID &&
		params.GroupID == "" && params.GuestID == ""
}

func (t *Tool) Definition() tools.Definition {
	return tools.Definition{
		Name:        toolName,
		Description: "Read-only settings catalog for Stella's direct one-to-one session. Use catalog first, then list or get supported resources. Configuration mutations are not available in this first slice.",
		InputSchema: tools.MustInputSchema(`{
  "type":"object",
  "additionalProperties":false,
  "properties":{
    "action":{"type":"string","enum":["catalog","describe","list","get"]},
    "resource":{"type":"string","enum":["agents"]},
    "id":{"type":"string"},
    "page_size":{"type":"integer","minimum":1,"maximum":100,"description":"Number of agents returned by list."},
    "page_token":{"type":"string","description":"Opaque list cursor; pass it back unchanged."}
  },
  "required":["action"]
}`),
	}
}

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if err := t.authorizeTurn(ctx); err != nil {
		return "", err
	}
	action, err := tools.ActionArg(args, toolName)
	if err != nil {
		return "", err
	}
	switch action {
	case "catalog":
		if err := rejectUnexpected(args, "action"); err != nil {
			return "", err
		}
		return tools.MarshalResult(map[string]any{
			"resources": []map[string]any{{"name": "agents", "operations": []string{"list", "get"}}},
			"mutations": "unsupported",
		})
	case "describe":
		if err := rejectUnexpected(args, "action", "resource"); err != nil {
			return "", err
		}
		resource, err := stringArg(args, "resource", true)
		if err != nil {
			return "", err
		}
		if resource != "agents" {
			return "", fmt.Errorf("unsupported settings resource %q", resource)
		}
		return tools.MarshalResult(map[string]any{
			"resource":    "agents",
			"operations":  []string{"list", "get"},
			"read_fields": []string{"id", "name", "model", "system_prompt", "soul", "scope", "enabled"},
			"write":       false,
		})
	case "list", "get":
		return t.readAgents(ctx, args, action)
	default:
		return "", fmt.Errorf("unknown settings action %q", action)
	}
}

func (t *Tool) authorizeTurn(ctx context.Context) error {
	if t == nil || t.agents == nil {
		return ErrUnavailable
	}
	if authz.AgentIDFromContext(ctx) != AgentID || authz.UserIDFromContext(ctx) == "" ||
		authz.GroupIDFromContext(ctx) != "" || authz.GuestIDFromContext(ctx) != "" ||
		memory.SessionIDFromContext(ctx) == "" {
		return errors.New("stella_settings is available only in a signed-in direct one-to-one session")
	}
	authority, ok := authz.AuthorityFromContext(ctx)
	if !ok || authority.Kind() != authz.ActorUser || !authority.Valid() ||
		string(authority.UserID()) != authz.UserIDFromContext(ctx) {
		return errors.New("stella_settings requires the current direct human Authority")
	}
	return nil
}

func (t *Tool) readAgents(ctx context.Context, args map[string]any, action string) (string, error) {
	allowed := []string{"action", "resource"}
	if action == "get" {
		allowed = append(allowed, "id")
	} else {
		allowed = append(allowed, "page_size", "page_token")
	}
	if err := rejectUnexpected(args, allowed...); err != nil {
		return "", err
	}
	resource, err := stringArg(args, "resource", true)
	if err != nil {
		return "", err
	}
	if resource != "agents" {
		return "", fmt.Errorf("unsupported settings resource %q", resource)
	}
	authority, _ := authz.AuthorityFromContext(ctx)
	if action == "get" {
		id, err := stringArg(args, "id", true)
		if err != nil {
			return "", err
		}
		var ag config.Agent
		if bounded, ok := t.agents.(boundedAgentReader); ok {
			ag, err = bounded.ReadProjection(ctx, authority, id)
		} else {
			ag, err = t.agents.Read(ctx, authority, id)
		}
		if err != nil {
			return "", err
		}
		return marshalAgentResult(action, agentView(ag, authority, false))
	}

	pageSize, pageOffset, err := agentListPage(args)
	if err != nil {
		return "", err
	}
	var agents []config.Agent
	if bounded, ok := t.agents.(boundedAgentReader); ok {
		agents, err = bounded.ListReadableProjection(ctx, authority, false)
	} else {
		agents, err = t.agents.ListReadable(ctx, authority, false)
	}
	if err != nil {
		return "", err
	}
	total := len(agents)
	if pageOffset > total {
		return "", fmt.Errorf("invalid pagination — pass page_token returned by list unchanged")
	}
	page := agents[pageOffset:]
	if len(page) > pageSize {
		page = page[:pageSize]
	}
	out := make([]map[string]any, 0, len(page))
	for _, ag := range page {
		out = append(out, agentView(ag, authority, true))
	}

	// The page size is only a row ceiling. User-controlled Agent fields still
	// need a serialized ceiling, so shrink this page while preserving a cursor
	// for every omitted row rather than silently making the directory partial.
	for {
		hasMore := pageOffset+len(out) < total
		response := map[string]any{
			"agents": out,
			"total":  total,
		}
		if hasMore {
			response["next_page_token"] = tools.OffsetToken(pageOffset + len(out))
		}
		serialized, err := tools.MarshalResult(response)
		if err != nil {
			return "", err
		}
		if len(serialized) <= maxAgentSerializedResult {
			return serialized, nil
		}
		if len(out) == 0 {
			return "", fmt.Errorf("stella_settings.%s exceeded its serialized result limit", action)
		}
		out = out[:len(out)-1]
	}
}

func agentListPage(args map[string]any) (int, int, error) {
	var input struct {
		PageSize  int    `json:"page_size,omitempty"`
		PageToken string `json:"page_token,omitempty"`
	}
	if err := tools.DecodeInput(args, &input, nil); err != nil {
		return 0, 0, err
	}
	pageSize, pageOffset, err := tools.ParsePage(input.PageSize, input.PageToken, defaultAgentListPageSize, maxAgentListPageSize)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid pagination — use page_size between 1 and %d and pass next_page_token unchanged", maxAgentListPageSize)
	}
	return pageSize, pageOffset, nil
}

func marshalAgentResult(action string, value any) (string, error) {
	serialized, err := tools.MarshalResult(value)
	if err != nil {
		return "", err
	}
	if len(serialized) > maxAgentSerializedResult {
		return "", fmt.Errorf("stella_settings.%s exceeded its serialized result limit", action)
	}
	return serialized, nil
}

func agentView(ag config.Agent, authority authz.Authority, summary bool) map[string]any {
	// Deliberately omit workspace, sandbox, and any future opaque config fields.
	// This is a model-facing read boundary, not a serialization of config.Agent.
	textLimit := maxAgentDetailTextBytes
	if summary {
		textLimit = maxAgentListSummaryBytes
	}
	id, idTruncated := tools.TruncateText(ag.ID, maxAgentProjectedTextBytes)
	name, nameTruncated := tools.TruncateText(ag.Name, maxAgentProjectedTextBytes)
	model, modelTruncated := tools.TruncateText(ag.Model, maxAgentProjectedTextBytes)
	scope, scopeTruncated := tools.TruncateText(ag.Scope, maxAgentProjectedTextBytes)
	systemPrompt, systemPromptTruncated := tools.TruncateText(ag.SystemPrompt, textLimit)
	soul, soulTruncated := tools.TruncateText(ag.Soul, textLimit)
	view := map[string]any{
		"id":            id,
		"name":          name,
		"model":         model,
		"system_prompt": systemPrompt,
		"soul":          soul,
		"scope":         scope,
		"enabled":       ag.Enabled,
	}
	if idTruncated {
		view["id_truncated"] = true
	}
	if nameTruncated {
		view["name_truncated"] = true
	}
	if modelTruncated {
		view["model_truncated"] = true
	}
	if scopeTruncated {
		view["scope_truncated"] = true
	}
	if systemPromptTruncated {
		view["system_prompt_truncated"] = true
	}
	if soulTruncated {
		view["soul_truncated"] = true
	}
	if authority.IsAdmin() {
		creatorID, creatorIDTruncated := tools.TruncateText(ag.CreatorID, maxAgentProjectedTextBytes)
		view["creator_id"] = creatorID
		if creatorIDTruncated {
			view["creator_id_truncated"] = true
		}
	}
	return view
}

func rejectUnexpected(args map[string]any, allowed ...string) error {
	known := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		known[key] = true
	}
	for key := range args {
		if !known[key] {
			return fmt.Errorf("unsupported stella_settings field %q", key)
		}
	}
	return nil
}

func stringArg(args map[string]any, key string, required bool) (string, error) {
	value, ok := args[key]
	if !ok {
		if required {
			return "", fmt.Errorf("missing required field %q", key)
		}
		return "", nil
	}
	result, ok := value.(string)
	if !ok || (required && strings.TrimSpace(result) == "") {
		return "", fmt.Errorf("%s must be a non-empty string", key)
	}
	return result, nil
}
