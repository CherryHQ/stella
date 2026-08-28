package main

import (
	"context"

	"github.com/CherryHQ/stella/internal/agent"
	sessionaccess "github.com/CherryHQ/stella/internal/agent/session/access"
	"github.com/CherryHQ/stella/internal/connections"
	"github.com/CherryHQ/stella/internal/email"
	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/internal/library"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/notify"
	"github.com/CherryHQ/stella/internal/recally"
	"github.com/CherryHQ/stella/internal/scheduler"
	sharepkg "github.com/CherryHQ/stella/internal/share"
	"github.com/CherryHQ/stella/internal/vault"
	workflowpkg "github.com/CherryHQ/stella/internal/workflow"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

// builtinToolDeps names every service the default builtin tool set is built
// from. Assembling the set in one function keeps the list of tools a deployment
// puts in front of the model enumerable by a test, rather than spread across
// three appends in the middle of service construction.
type builtinToolDeps struct {
	Notifier    pkgplugins.Notifier
	Memory      memory.Provider
	Recall      memory.RecallSource
	GroupRecall memory.GroupRecallSource
	Goal        *goal.Service
	Session     *sessionaccess.Service
	Library     *library.Service
	Scheduler   *scheduler.Service
	Workflow    *workflowpkg.Service
	Credentials *connections.Service
	Email       *email.Service
	Share       *sharepkg.Service
	Recally     *recally.Service
	Vault       *vault.Service
}

// newBuiltinTools returns the builtin tools in the order they are offered to the
// runner. A nil Vault omits the vault tool, the one dependency whose absence
// removes a tool rather than merely gating it.
func newBuiltinTools(d builtinToolDeps) []agent.BuiltinTool {
	builtins := []agent.BuiltinTool{{
		Tool: memory.BuildTool(d.Memory, memory.WithRecallSource(d.Recall), memory.WithGroupRecallSource(d.GroupRecall)),
	}}
	if notifyTool := notify.NewTool(d.Notifier); notifyTool != nil {
		builtins = append(builtins, agent.BuiltinTool{Tool: notifyTool})
	}
	builtins = append(builtins,
		agent.BuiltinTool{Tool: goal.NewTool(d.Goal), Available: agent.BuiltinToolAvailable},
		agent.BuiltinTool{Tool: sessionaccess.NewTool(d.Session), Available: func(ctx context.Context, params agent.RunnerParams) (bool, error) {
			baseline, err := agent.BuiltinToolAvailable(ctx, params)
			if err != nil {
				return false, err
			}
			return params.GroupID == "" && baseline, nil
		}},
		agent.BuiltinTool{Tool: library.NewTool(d.Library), Available: libraryToolAvailable},
		agent.BuiltinTool{Tool: scheduler.NewTool(d.Scheduler), Available: agent.BuiltinToolAvailable},
		agent.BuiltinTool{Tool: workflowpkg.NewTool(d.Workflow), Available: agent.BuiltinToolAvailable},
		agent.BuiltinTool{Tool: connections.NewTool(d.Credentials), Available: oauthToolAvailable(d.Credentials)},
		agent.BuiltinTool{Tool: email.NewTool(d.Email), Available: emailToolAvailable(d.Vault)},
		agent.BuiltinTool{Tool: sharepkg.NewTool(d.Share), Available: agent.BuiltinToolAvailable},
	)
	// Recally is one tool per action: the provider validates each call against an
	// exact schema instead of a union that accepts every action's fields.
	for _, spec := range recally.ActionTools() {
		builtins = append(builtins, agent.BuiltinTool{
			Build: func(build pkgplugins.ToolBuildContext) (pkgtools.Tool, error) {
				return recally.NewRuntimeTool(d.Recally, build.Runtime, spec), nil
			},
			Spec:      recally.NewTool(d.Recally, spec).Definition(),
			Available: agent.BuiltinToolAvailable,
		})
	}
	if d.Vault != nil {
		builtins = append(builtins, agent.BuiltinTool{Tool: vault.NewTool(d.Vault, d.Credentials), Available: agent.BuiltinToolAvailable})
	}
	return builtins
}
