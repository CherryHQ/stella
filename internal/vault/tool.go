package vault

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/tools"
)

type Tool struct{ svc *Service }

func NewTool(svc *Service) *Tool { return &Tool{svc: svc} }
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
	action, err := tools.ActionArg(args, "vault")
	if err != nil {
		return "", err
	}
	out, err := Dispatch(ctx, vaultHandler{svc: t.svc, ident: ident}, action, args)
	if err != nil {
		return "", authz.MapError("vault", err)
	}
	return tools.MarshalResult(out)
}

type vaultHandler struct {
	svc   *Service
	ident authz.Identity
}

func (h vaultHandler) List(ctx context.Context, in ListInput) (any, error) {
	entries, err := h.svc.As(h.ident).List(ctx, in.Scope)
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
	meta, err := h.svc.As(h.ident).Set(ctx, in.Scope, in.Name, in.Value)
	if err != nil {
		return nil, err
	}
	return map[string]any{"name": meta.Name, "scope": meta.Scope, "status": "set"}, nil
}

func (h vaultHandler) Delete(ctx context.Context, in DeleteInput) (any, error) {
	if err := h.svc.As(h.ident).Delete(ctx, in.Scope, in.Name); err != nil {
		return nil, err
	}
	return map[string]any{"name": in.Name, "scope": defaultVaultScope(in.Scope), "status": "deleted"}, nil
}

func defaultVaultScope(scope string) string {
	if scope == "" {
		return ScopeUser
	}
	return scope
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
