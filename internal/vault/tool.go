package vault

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/tools"
)

type Tool struct {
	svc         *Service
	invalidator RunnerInvalidator
}

func NewTool(svc *Service, invalidator RunnerInvalidator) *Tool {
	return &Tool{svc: svc, invalidator: invalidator}
}

func (t *Tool) Definition() tools.Definition {
	return tools.Definition{Name: ToolName, Description: "Store, list, and delete secrets for this user or this user+agent. Secrets are injected into sandbox processes as environment variables at session start; there is deliberately no read-back action, and list returns metadata only. Actions: list, set, delete.", InputSchema: InputSchema()}
}

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.svc == nil {
		return "", fmt.Errorf("vault service is unavailable — ask an operator to configure STELLA_VAULT_KEY")
	}
	ident, err := authz.ToolIdentity(ctx, "vault")
	if err != nil {
		return "", err
	}
	// The runtime context identity is the trusted adapter: a delegated agent turn
	// becomes a confined AgentActor. Model-supplied arguments never form identity.
	authority, err := ident.ToAuthority()
	if err != nil {
		return "", authz.MapError("vault", err)
	}
	action, err := tools.ActionArg(args, "vault")
	if err != nil {
		return "", err
	}
	out, err := Dispatch(ctx, vaultHandler{svc: t.svc, invalidator: t.invalidator, authority: authority, agentID: ident.AgentID}, action, args)
	if err != nil {
		return "", authz.MapError("vault", err)
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
