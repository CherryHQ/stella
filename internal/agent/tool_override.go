package agent

import (
	"context"

	"github.com/CherryHQ/stella/internal/agent/sandbox"
)

const (
	ToolOverrideScopeSystem      = "system"
	ToolOverrideScopeSystemAgent = "system_agent"
	ToolOverrideScopeUser        = "user"
	ToolOverrideScopeUserAgent   = "user_agent"

	ToolOverrideOriginDefault = "default"
)

// ToolOverride is the runner-facing view of a persisted tool visibility row.
type ToolOverride struct {
	ToolName string
	Scope    string
	Enabled  bool
}

type ToolOverrideDecision struct {
	Enabled bool
	Origin  string
}

type ToolOverrideFetcher func(ctx context.Context, userID, agentID string) ([]ToolOverride, error)

var coreToolNames = func() map[string]struct{} {
	m := make(map[string]struct{}, 4)
	for _, d := range sandbox.ToolDefinitions() {
		m[d.Name] = struct{}{}
	}
	return m
}()

func IsCoreToolName(name string) bool {
	_, ok := coreToolNames[name]
	return ok
}

func ResolveToolOverride(defaultEnabled bool, toolName string, rows []ToolOverride) ToolOverrideDecision {
	if IsCoreToolName(toolName) {
		return ToolOverrideDecision{Enabled: true, Origin: ToolOverrideOriginDefault}
	}

	admin, hasAdmin := mostSpecificOverride(toolName, rows, ToolOverrideScopeSystemAgent, ToolOverrideScopeSystem)
	if hasAdmin && !admin.Enabled {
		return ToolOverrideDecision{Enabled: false, Origin: admin.Scope}
	}

	user, hasUser := mostSpecificOverride(toolName, rows, ToolOverrideScopeUserAgent, ToolOverrideScopeUser)
	if hasUser {
		return ToolOverrideDecision{Enabled: user.Enabled, Origin: user.Scope}
	}
	if hasAdmin {
		return ToolOverrideDecision{Enabled: true, Origin: admin.Scope}
	}
	return ToolOverrideDecision{Enabled: defaultEnabled, Origin: ToolOverrideOriginDefault}
}

func mostSpecificOverride(toolName string, rows []ToolOverride, scopes ...string) (ToolOverride, bool) {
	for _, scope := range scopes {
		for _, row := range rows {
			if row.ToolName == toolName && row.Scope == scope {
				return row, true
			}
		}
	}
	return ToolOverride{}, false
}

func FilterToolEnabled(defaultEnabled bool, toolName string, rows []ToolOverride) bool {
	return ResolveToolOverride(defaultEnabled, toolName, rows).Enabled
}
