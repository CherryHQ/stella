package channel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Two classifiers in this package run one JSON completion on an agent's fast
// model: chat intent and group nudge. They share the machinery below and
// nothing else -- their degradation policies differ on purpose. Intent sits on
// the user's synchronous path and goes quiet; nudge drops to a deterministic
// heuristic. So this returns raw text plus the stage it reached, and every
// policy decision stays with the caller.

// errNoFastModel reports that this agent has no usable fast model configured.
// It is a static configuration fact, so callers should degrade rather than
// retry: retrying a missing setting only burns attempts.
var errNoFastModel = errors.New("no fast model configured")

// The stage a call reached before failing. Callers record it in diagnostics;
// fallback policy stays local to the call site, so the strings are data, not
// user-visible degraded reasons.
const (
	fastModelStageSnapshot   = "snapshot"
	fastModelStageStream     = "stream"
	fastModelStageCompletion = "completion"
)

type fastModelCaller struct {
	load     SnapshotLoader
	build    StreamFuncBuilder
	complete CompleteFunc
}

// Complete runs one single-turn completion and returns the model's flattened
// text. Parsing stays with the caller: only the caller knows whether a
// malformed answer deserves a retry, a fallback, or a shrug.
func (f fastModelCaller) Complete(ctx context.Context, agentID, system, payload string, timeout time.Duration) (string, string, error) {
	snap, err := f.load(ctx, agentID)
	if err != nil || snap == nil {
		return "", fastModelStageSnapshot, fmt.Errorf("load snapshot: %w", err)
	}
	if strings.TrimSpace(snap.ModelFast) == "" {
		return "", fastModelStageSnapshot, errNoFastModel
	}
	model := snap.ResolveModelTier(config.ModelTierFast)
	// A stored ref that resolves to no provider or no model id is the same
	// static fact as an unset one. Agent writes now reject that shape, but rows
	// stored before they did still exist, so this stays as the backstop.
	if model.API == "" || model.ID == "" {
		return "", fastModelStageSnapshot, errNoFastModel
	}
	creds := snap.ResolveProviderCreds(model.Provider)
	stream, err := f.build(ctx, classifierProviderType(model.Provider, creds), creds)
	if err != nil || stream == nil {
		return "", fastModelStageStream, fmt.Errorf("build stream: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	msg, err := f.complete(cctx, model, ai.Context{
		System:   system,
		Messages: []ai.Message{ai.UserMessage{Content: payload}},
	}, ai.CompleteOptions{StreamOptions: ai.StreamOptions{Timeout: timeout}}, stream)
	if err != nil {
		return "", fastModelStageCompletion, err
	}
	return ai.FlattenText(msg.Content), "", nil
}

// firstSystemScopeMember returns one system-scope group member, or "" when
// none is available. A platform-group nudge classifier is billed to that agent,
// so platform participants cannot spend a private member's provider credits.
//
// It costs one GetAgent per member; a group-scoped join belongs here once
// member counts justify it (CR-012).
func firstSystemScopeMember(ctx context.Context, q *sqlc.Queries, members []sqlc.ChannelGroupMember) string {
	for _, member := range members {
		agent, err := q.GetAgent(ctx, member.AgentID)
		if err == nil && agent.Scope == config.AgentScopeSystem {
			return agent.ID
		}
	}
	return ""
}
