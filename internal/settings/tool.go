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
	toolName = "stella_settings"
)

var ErrUnavailable = errors.New("stella settings is unavailable")

// agentReader is the existing Agent PEP. Keeping the tool on this narrow port
// prevents it from reading config.Store directly and bypassing ownership rules.
type agentReader interface {
	ListReadable(context.Context, authz.Authority, bool) ([]config.Agent, error)
	Read(context.Context, authz.Authority, string) (config.Agent, error)
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
	return params.UserID != "" && params.AgentID == AgentID && params.GroupID == "" && params.GuestID == ""
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
    "id":{"type":"string"}
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
		ag, err := t.agents.Read(ctx, authority, id)
		if err != nil {
			return "", err
		}
		return tools.MarshalResult(agentView(ag, authority))
	}
	agents, err := t.agents.ListReadable(ctx, authority, false)
	if err != nil {
		return "", err
	}
	out := make([]map[string]any, 0, len(agents))
	for _, ag := range agents {
		out = append(out, agentView(ag, authority))
	}
	return tools.MarshalResult(map[string]any{"agents": out})
}

func agentView(ag config.Agent, authority authz.Authority) map[string]any {
	// Deliberately omit workspace, sandbox, and any future opaque config fields.
	// This is a model-facing read boundary, not a serialization of config.Agent.
	view := map[string]any{
		"id":            ag.ID,
		"name":          ag.Name,
		"model":         ag.Model,
		"system_prompt": ag.SystemPrompt,
		"soul":          ag.Soul,
		"scope":         ag.Scope,
		"enabled":       ag.Enabled,
	}
	if authority.IsAdmin() {
		view["creator_id"] = ag.CreatorID
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
