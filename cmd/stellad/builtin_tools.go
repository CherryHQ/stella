package main

import (
	"context"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	sessionaccess "github.com/CherryHQ/stella/internal/agent/session/access"
	"github.com/CherryHQ/stella/internal/agent/settingspolicy"
	"github.com/CherryHQ/stella/internal/agent/toolmeta"
	"github.com/CherryHQ/stella/internal/connections"
	"github.com/CherryHQ/stella/internal/email"
	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/internal/library"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/notify"
	"github.com/CherryHQ/stella/internal/recally"
	"github.com/CherryHQ/stella/internal/scheduler"
	sharepkg "github.com/CherryHQ/stella/internal/share"
	"github.com/CherryHQ/stella/internal/skills"
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
	Notifier        pkgplugins.Notifier
	Memory          memory.Provider
	Recall          memory.RecallSource
	GroupRecall     memory.GroupRecallSource
	Goal            *goal.Service
	Session         *sessionaccess.Service
	Library         *library.Service
	Scheduler       *scheduler.Service
	Workflow        *workflowpkg.Service
	Credentials     *connections.Service
	Email           *email.Service
	Share           *sharepkg.Service
	Recally         *recally.Service
	Vault           *vault.Service
	AgentManagement func() *agentaccess.Management
	ToolOverrides   *agent.ToolOverrideStore
	ToolMeta        func() *toolmeta.Registry
	SettingsAdmin   settingspolicy.AdminLookup
}

// toolAvailable is the fail-closed visibility predicate: a check that cannot be
// resolved reports the error rather than guessing a tool into or out of view.
type toolAvailable = func(context.Context, agent.RunnerParams) (bool, error)

// splitBuiltins turns a family's generated ActionTools into one registration
// entry per action. newTool builds the adapter for one action; every entry
// shares the same availability check, because visibility is a property of the
// family's service, not of the individual action.
//
// Adding an action to a split family therefore needs no edit here: the entry
// appears as soon as toolgen emits it.
func splitBuiltins(specs []toolmeta.ActionTool, newTool func(toolmeta.ActionTool) pkgtools.Tool, available toolAvailable) []agent.BuiltinTool {
	out := make([]agent.BuiltinTool, 0, len(specs))
	for _, spec := range specs {
		out = append(out, agent.BuiltinTool{Tool: newTool(spec), Available: available})
	}
	return out
}

// splitRuntimeBuiltins is splitBuiltins for a family whose tools need the
// sandbox session. The definition is still static, so the catalog can list the
// tool without building one.
func splitRuntimeBuiltins(specs []toolmeta.ActionTool, newTool func(pkgplugins.ToolBuildContext, toolmeta.ActionTool) pkgtools.Tool, spec func(toolmeta.ActionTool) pkgtools.Tool, available toolAvailable) []agent.BuiltinTool {
	out := make([]agent.BuiltinTool, 0, len(specs))
	for _, actionSpec := range specs {
		out = append(out, agent.BuiltinTool{
			Build: func(build pkgplugins.ToolBuildContext) (pkgtools.Tool, error) {
				return newTool(build, actionSpec), nil
			},
			Spec:      spec(actionSpec).Definition(),
			Available: available,
		})
	}
	return out
}

// newBuiltinTools returns the builtin tools in the order they are offered to the
// runner. A nil Vault omits the vault tools, the one dependency whose absence
// removes a tool rather than merely gating it.
func newBuiltinTools(d builtinToolDeps) []agent.BuiltinTool {
	// One Recall per deployment, one registered tool per action: the provider's
	// capabilities are probed once, so they belong to the deployment rather than
	// to a call. The lane a call takes is still chosen per turn.
	memoryRecall := memory.NewRecall(d.Memory, d.Recall, d.GroupRecall)
	builtins := splitBuiltins(memory.ActionTools(), func(spec toolmeta.ActionTool) pkgtools.Tool {
		return memory.NewTool(memoryRecall, spec)
	}, agent.BuiltinToolAvailable)
	if notifyTool := notify.NewTool(d.Notifier); notifyTool != nil {
		builtins = append(builtins, agent.BuiltinTool{Tool: notifyTool})
	}
	// A split family is one tool per action: the provider validates each call
	// against an exact schema instead of a union that accepts every action's
	// fields.
	builtins = append(builtins, splitBuiltins(goal.ActionTools(), func(spec toolmeta.ActionTool) pkgtools.Tool {
		return goal.NewTool(d.Goal, spec)
	}, agent.BuiltinToolAvailable)...)
	// The session tools reach another session's transcript, so they stay out of
	// group turns where the caller's identity is not one user's.
	builtins = append(builtins, splitBuiltins(sessionaccess.ActionTools(), func(spec toolmeta.ActionTool) pkgtools.Tool {
		return sessionaccess.NewTool(d.Session, spec)
	}, func(ctx context.Context, params agent.RunnerParams) (bool, error) {
		baseline, err := agent.BuiltinToolAvailable(ctx, params)
		if err != nil {
			return false, err
		}
		return params.GroupID == "" && baseline, nil
	})...)
	builtins = append(builtins, splitBuiltins(library.RuntimeActionTools(), func(spec toolmeta.ActionTool) pkgtools.Tool {
		return library.NewTool(d.Library, spec)
	}, libraryToolAvailable)...)
	builtins = append(builtins, splitBuiltins(scheduler.ActionTools(), func(spec toolmeta.ActionTool) pkgtools.Tool {
		return scheduler.NewTool(d.Scheduler, spec)
	}, agent.BuiltinToolAvailable)...)
	builtins = append(builtins, splitBuiltins(workflowpkg.ActionTools(), func(spec toolmeta.ActionTool) pkgtools.Tool {
		return workflowpkg.NewTool(d.Workflow, spec)
	}, agent.BuiltinToolAvailable)...)
	builtins = append(builtins, splitBuiltins(connections.ActionTools(), func(spec toolmeta.ActionTool) pkgtools.Tool {
		return connections.NewTool(d.Credentials, spec)
	}, oauthToolAvailable(d.Credentials))...)
	builtins = append(builtins, splitBuiltins(email.ActionTools(), func(spec toolmeta.ActionTool) pkgtools.Tool {
		return email.NewTool(d.Email, spec)
	}, emailToolAvailable(d.Vault))...)
	builtins = append(builtins, splitBuiltins(sharepkg.ActionTools(), func(spec toolmeta.ActionTool) pkgtools.Tool {
		return sharepkg.NewTool(d.Share, spec)
	}, agent.BuiltinToolAvailable)...)
	builtins = append(builtins, splitRuntimeBuiltins(recally.ActionTools(), func(build pkgplugins.ToolBuildContext, spec toolmeta.ActionTool) pkgtools.Tool {
		return recally.NewRuntimeTool(d.Recally, build.Runtime, spec)
	}, func(spec toolmeta.ActionTool) pkgtools.Tool {
		return recally.NewTool(d.Recally, spec)
	}, agent.BuiltinToolAvailable)...)
	if d.Vault != nil {
		builtins = append(builtins, splitBuiltins(vault.ActionTools(), func(spec toolmeta.ActionTool) pkgtools.Tool {
			return vault.NewTool(d.Vault, d.Credentials, spec)
		}, agent.BuiltinToolAvailable)...)
	}
	// Settings tools stay cold behind Code Mode, but are always part of the
	// production inventory. Missing domain wiring fails on Execute rather than
	// making a deployment silently advertise a partial family.
	builtins = append(builtins, splitBuiltins(agent.AgentActionTools(), func(spec toolmeta.ActionTool) pkgtools.Tool {
		return agent.NewManagementTool(spec, d.AgentManagement)
	}, settingspolicy.Available(false, d.SettingsAdmin))...)
	builtins = append(builtins, splitBuiltins(agent.AgentToolActionTools(), func(spec toolmeta.ActionTool) pkgtools.Tool {
		return agent.NewToolOverrideManagementTool(spec, d.AgentManagement, d.ToolOverrides, d.ToolMeta)
	}, settingspolicy.Available(false, d.SettingsAdmin))...)
	return builtins
}

// generatedFamilies is every family toolgen emits in this build. It is one list
// so that the selector registry and any test that asserts on real tool names
// read from the same source.
func generatedFamilies() [][]toolmeta.ActionTool {
	return [][]toolmeta.ActionTool{
		goal.ActionTools(), scheduler.ActionTools(), workflowpkg.ActionTools(),
		connections.ActionTools(), email.ActionTools(), sharepkg.ActionTools(),
		vault.ActionTools(), recally.ActionTools(),
		sessionaccess.ActionTools(), skills.RuntimeActionTools(),
		memory.ActionTools(), library.RuntimeActionTools(),
		agent.AgentActionTools(), agent.AgentToolActionTools(),
	}
}

// newToolMetaRegistry declares this build's generated tools for the name-only
// selector sites — the runner's excluded_tools filter, the delegate preset
// whitelist, and the trace hook's action attribute. Every family goes in,
// including ones whose service is unavailable at runtime: the registry answers
// "what does this name mean", not "is this tool usable right now".
func newToolMetaRegistry(families ...[]toolmeta.ActionTool) *toolmeta.Registry {
	var all []toolmeta.ActionTool
	for _, family := range families {
		all = append(all, family...)
	}
	return toolmeta.NewRegistry(all...)
}

// splitFamilyNames lists every generated tool name in the given families. The
// goal worker's exclusion list is derived from it rather than written by hand:
// a new scheduler action must not become a tool a goal worker can call just
// because nobody remembered to add its name here.
func splitFamilyNames(families ...[]toolmeta.ActionTool) []string {
	var out []string
	for _, family := range families {
		for _, spec := range family {
			out = append(out, spec.Name)
		}
	}
	return out
}
