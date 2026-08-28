package vault

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/tools"
)

// ListTool is the vault action that lists what this agent can reach. Error
// prose points at it, so a rename shows up here rather than in a string.
const ListTool = "vault_secret_list"

// actionDescriptions is the model-facing description per generated tool. A
// split tool's schema is exact, so each description only says what the call
// does and what it costs.
var actionDescriptions = map[string]string{
	"list":   "List this user's stored secret names and scopes. Values are never returned, by design: a secret reaches a process as an environment variable, not as model context.",
	"set":    "Store or replace one secret for this user, or for this user and agent when scope is user_agent. It is injected into sandbox processes at the next session start, so a running session does not see it.",
	"delete": "Delete one stored secret by name and scope. Processes started before the delete keep the value they were given.",
}

// Tool is one generated vault action. The tool name carries the action, so the
// provider validates arguments against an exact schema before dispatch.
type Tool struct {
	spec        ActionTool
	svc         *Service
	invalidator RunnerInvalidator
}

// NewTool builds one vault action tool.
func NewTool(svc *Service, invalidator RunnerInvalidator, spec ActionTool) *Tool {
	return &Tool{spec: spec, svc: svc, invalidator: invalidator}
}

func (t *Tool) Definition() tools.Definition {
	return t.spec.Definition(actionDescriptions[t.spec.Action])
}

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.svc == nil {
		return "", fmt.Errorf("vault service is unavailable — ask an operator to configure STELLA_VAULT_KEY")
	}
	ident, err := authz.ToolIdentity(ctx, t.spec.Name)
	if err != nil {
		return "", err
	}
	// The runtime context identity is the trusted adapter: a delegated agent turn
	// becomes a confined AgentActor. Model-supplied arguments never form identity.
	authority, err := ident.ToAuthority()
	if err != nil {
		return "", authz.MapToolError(t.spec.Name, ListTool, err)
	}
	out, err := Dispatch(ctx, vaultHandler{svc: t.svc, invalidator: t.invalidator, authority: authority, agentID: ident.AgentID}, t.spec.Action, args)
	if err != nil {
		return "", authz.MapToolError(t.spec.Name, ListTool, err)
	}
	return tools.MarshalResult(out)
}

type vaultHandler struct {
	svc         *Service
	invalidator RunnerInvalidator
	authority   authz.Authority
	agentID     string
}

func (h vaultHandler) begin(ctx context.Context) (*Access, error) {
	return h.svc.Begin(ctx, h.authority)
}

func (h vaultHandler) List(ctx context.Context, in ListInput) (any, error) {
	acc, err := h.begin(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := acc.ListScoped(ctx, in.Scope, "")
	if err != nil {
		return nil, err
	}
	items := make([]vaultResponse, 0, len(entries))
	for _, entry := range entries {
		items = append(items, vaultSummary(entry))
	}
	return listResponse[vaultResponse]{Items: items, HasMore: false}, nil
}

func (h vaultHandler) Set(ctx context.Context, in SetInput) (any, error) {
	if err := h.svc.ValidateUserFacingName(in.Name); err != nil {
		return nil, err
	}
	acc, err := h.begin(ctx)
	if err != nil {
		return nil, err
	}
	scope := in.Scope
	if scope == "" {
		scope = ScopeUser
	}
	// The tool acts as an agent turn, so a user_agent write targets the bound agent.
	if err := acc.SetScoped(ctx, scope, h.agentID, in.Name, in.Value, SetOptions{}); err != nil {
		return nil, err
	}
	h.invalidate(scope, acc.userID, in.Name, "set")
	meta, err := acc.GetScopedMeta(ctx, scope, h.agentID, in.Name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"name": meta.Name, "scope": meta.Scope, "status": "set"}, nil
}

func (h vaultHandler) Delete(ctx context.Context, in DeleteInput) (any, error) {
	acc, err := h.begin(ctx)
	if err != nil {
		return nil, err
	}
	scope := in.Scope
	if scope == "" {
		scope = ScopeUser
	}
	if err := acc.DeleteScoped(ctx, scope, h.agentID, in.Name); err != nil {
		return nil, err
	}
	h.invalidate(scope, acc.userID, in.Name, "delete")
	return map[string]any{"name": in.Name, "scope": scope, "status": "deleted"}, nil
}

func (h vaultHandler) invalidate(scope, userID, name, op string) {
	// A user_agent write binds to the acting agent; a plain user scope has none.
	agentID := h.agentID
	if scope == ScopeUser {
		agentID = ""
	}
	if err := InvalidateForScope(h.invalidator, scope, userID, agentID); err != nil {
		slog.Warn("invalidate runners after vault tool "+op, "scope", scope, "user_id", userID, "agent_id", agentID, "name", name, "error", err)
	}
}

type vaultResponse struct {
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	UpdatedAt string `json:"updated_at"`
}
type listResponse[T any] struct {
	Items         []T    `json:"items"`
	HasMore       bool   `json:"has_more"`
	NextPageToken string `json:"next_page_token,omitempty"`
}

func vaultSummary(meta EntryMeta) vaultResponse {
	return vaultResponse{Name: meta.Name, Scope: meta.Scope, UpdatedAt: meta.UpdatedAt}
}
