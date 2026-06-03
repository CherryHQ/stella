// Package agentvault exposes the user's secret vault as native agent tools.
// Entries are scoped to the acting user (read from context, never an argument).
//
// Security: there is deliberately NO vault_get and NO vault_keygen tool. Reading
// a secret's plaintext back would leak it into the model context, traces, and
// logs; the agent already receives the secrets it needs via sandbox env
// injection (VaultEnvLoader). Key generation is a deployment bootstrap, not an
// agent operation. Only list/set/delete are exposed — set takes a value the user
// already handed the agent, so it adds no new disclosure surface.
package agentvault

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/CherryHQ/stella/internal/tools/toolctx"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/tools"
)

// NewTools builds the vault native tools bound to the given service. Returns nil
// when the service is unavailable (no vault key configured) so callers can
// append unconditionally.
func NewTools(svc *vault.Service) []tools.Tool {
	if svc == nil {
		return nil
	}
	t := &impl{svc: svc}
	return []tools.Tool{
		fnTool{listDef(), t.list},
		fnTool{setDef(), t.set},
		fnTool{deleteDef(), t.del},
	}
}

type impl struct{ svc *vault.Service }

func (t *impl) list(ctx context.Context, _ map[string]any) (string, error) {
	userID, err := toolctx.UserID(ctx)
	if err != nil {
		return "", err
	}
	metas, err := t.svc.List(ctx, userID)
	if err != nil {
		return "", err
	}
	return marshal(metas)
}

func (t *impl) set(ctx context.Context, args map[string]any) (string, error) {
	userID, err := toolctx.UserID(ctx)
	if err != nil {
		return "", err
	}
	name, _ := args["name"].(string)
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	value, _ := args["value"].(string)
	if value == "" {
		return "", fmt.Errorf("value is required")
	}
	if err := t.svc.Set(ctx, userID, name, value); err != nil {
		return "", err
	}
	return fmt.Sprintf("Secret %q stored.", name), nil
}

func (t *impl) del(ctx context.Context, args map[string]any) (string, error) {
	userID, err := toolctx.UserID(ctx)
	if err != nil {
		return "", err
	}
	name, _ := args["name"].(string)
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	if err := t.svc.Delete(ctx, userID, name); err != nil {
		return "", err
	}
	return fmt.Sprintf("Secret %q deleted.", name), nil
}

type fnTool struct {
	def tools.Definition
	fn  func(context.Context, map[string]any) (string, error)
}

func (t fnTool) Definition() tools.Definition { return t.def }
func (t fnTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	return t.fn(ctx, args)
}

func marshal(v any) (string, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}
