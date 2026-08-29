package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/tools"
)

// The two model-facing tool names, spelled once. They appear in error prose the
// model reads back, so an error names something it can actually call again.
const (
	toolSearch = "memory_search"
	toolRead   = "memory_read"
)

const runtimeUsageTouchTimeout = 500 * time.Millisecond

// Recall is the memory surface both generated tools dispatch onto: the provider
// capabilities discovered once, the private recall lane, and the group lane.
// One instance is shared by memory_search and memory_read, because those
// capabilities belong to the provider rather than to a call.
type Recall struct {
	profileStore          ProfileStore
	changelogReader       ChangelogReader
	constraintStore       ConstraintStore
	snapshotStore         SessionSnapshotStore
	factStore             FactStore
	versionedFacts        VersionedFactStore
	versionedProfiles     VersionedProfileStore
	versionedConstraints  VersionedConstraintStore
	knowledgeUsageTracker KnowledgeUsageTracker
	private               RecallSource
	group                 GroupRecallSource
}

// NewRecall discovers the provider's capabilities once and binds both recall
// lanes. The group lane is selected from trusted turn context, never from tool
// input.
//
// Capabilities are probed on Unwrap(provider) so a traced wrapper cannot
// advertise something the inner provider does not implement, but the typed
// references are taken from the outer provider so tracing still fires.
func NewRecall(provider Provider, private RecallSource, group GroupRecallSource) *Recall {
	r := &Recall{private: private, group: group}
	inner := Unwrap(provider)
	if _, ok := inner.(ProfileStore); ok {
		r.profileStore, _ = provider.(ProfileStore)
	}
	if _, ok := inner.(ChangelogReader); ok {
		r.changelogReader, _ = provider.(ChangelogReader)
	}
	if _, ok := inner.(ConstraintStore); ok {
		r.constraintStore, _ = provider.(ConstraintStore)
	}
	if _, ok := inner.(SessionSnapshotStore); ok {
		r.snapshotStore, _ = provider.(SessionSnapshotStore)
	}
	if _, ok := inner.(FactStore); ok {
		r.factStore, _ = provider.(FactStore)
	}
	if _, ok := inner.(VersionedFactStore); ok {
		r.versionedFacts, _ = provider.(VersionedFactStore)
	}
	if _, ok := inner.(VersionedProfileStore); ok {
		r.versionedProfiles, _ = provider.(VersionedProfileStore)
	}
	if _, ok := inner.(VersionedConstraintStore); ok {
		r.versionedConstraints, _ = provider.(VersionedConstraintStore)
	}
	if _, ok := inner.(KnowledgeUsageTracker); ok {
		r.knowledgeUsageTracker, _ = provider.(KnowledgeUsageTracker)
	}
	return r
}

// Tool is one generated memory tool. The name carries the action, so the
// provider validates arguments against an exact schema before dispatch, and
// identity always comes from the runtime context.
type Tool struct {
	spec   ActionTool
	recall *Recall
}

// NewTool builds one memory action tool over a shared Recall.
func NewTool(recall *Recall, spec ActionTool) *Tool { return &Tool{spec: spec, recall: recall} }

func (t *Tool) Definition() tools.Definition { return t.spec.Definition("") }

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.recall == nil {
		return "", errors.New("memory is unavailable — try again later")
	}
	// The lane is chosen from the trusted turn context before any argument is
	// decoded: a group turn holds no private-memory authority, so it must not
	// reach the private dispatch even with a well-formed private ref.
	var handler Handler = privateHandler{recall: t.recall}
	if authz.GroupIDFromContext(ctx) != "" {
		handler = groupHandler{recall: t.recall}
	}
	out, err := Dispatch(ctx, handler, t.spec.Action, args)
	if err != nil {
		return "", err
	}
	if text, ok := out.(string); ok {
		return text, nil
	}
	return tools.MarshalResult(out)
}

// privateHandler answers the generated Dispatch on a one-to-one turn: this
// user's own session recall federated with snapshot-visible durable memory.
type privateHandler struct {
	recall *Recall
}

func (h privateHandler) Search(ctx context.Context, in MemorySearchInput) (any, error) {
	return h.recall.unifiedSearch(ctx, in)
}

func (h privateHandler) Read(ctx context.Context, in MemoryReadInput) (any, error) {
	return h.recall.unifiedRead(ctx, in)
}

// groupHandler answers it on a group turn: delivered public group text strictly
// before the trigger, and never a private ref.
type groupHandler struct {
	recall *Recall
}

func (h groupHandler) Search(ctx context.Context, in MemorySearchInput) (any, error) {
	return h.recall.groupSearch(ctx, in)
}

func (h groupHandler) Read(ctx context.Context, in MemoryReadInput) (any, error) {
	return h.recall.groupRead(ctx, in)
}

// returnedFact is the minimum a usage touch needs: which fact was handed to the
// model, and whether reflect wrote it.
type returnedFact struct {
	id     string
	source ChangeSource
}

// touchReturnedKnowledgeUsage records that reflect-authored facts were actually
// read, so reflect can retire the ones nothing ever recalls. Best effort and
// bounded: a slow tracker must not hold up the answer.
func (t *Recall) touchReturnedKnowledgeUsage(ctx context.Context, userID string, agentID string, facts []returnedFact) {
	if t.knowledgeUsageTracker == nil {
		return
	}
	factIDs := make([]string, 0, len(facts))
	for _, fact := range facts {
		if fact.source != SourceReflect {
			continue
		}
		factIDs = append(factIDs, fact.id)
	}
	if len(factIDs) == 0 {
		return
	}
	touchCtx, cancel := context.WithTimeout(ctx, runtimeUsageTouchTimeout)
	defer cancel()
	if err := t.knowledgeUsageTracker.TouchKnowledgeUsage(touchCtx, userID, agentID, factIDs); err != nil {
		slog.WarnContext(ctx, "memory_search: failed to touch knowledge usage", "err", err)
	}
}

func (t *Recall) requireKnowledgeCtx(ctx context.Context, tool string) (string, string, error) {
	if t.factStore == nil {
		return "", "", fmt.Errorf("%s: not supported by provider", tool)
	}
	userID := authz.UserIDFromContext(ctx)
	if userID == "" {
		return "", "", fmt.Errorf("%s: no user context", tool)
	}
	agentID := authz.AgentIDFromContext(ctx)
	if agentID == "" {
		return "", "", fmt.Errorf("%s: no agent context", tool)
	}
	return userID, agentID, nil
}

// searchKnowledgeFacts lists the world-knowledge facts this turn may see. A
// session with a snapshot reads the frozen version, so a fact written mid-turn
// cannot appear halfway through a conversation.
func (t *Recall) searchKnowledgeFacts(ctx context.Context, userID string, agentID string, tool string) ([]Fact, error) {
	snapshotVersion := int64(0)
	hasSnapshot := false
	if t.snapshotStore != nil {
		if sessionID := SessionIDFromContext(ctx); sessionID != "" {
			snap, err := t.snapshotStore.GetOrCreateSessionSnapshot(ctx, sessionID, userID, agentID)
			if err != nil {
				return nil, fmt.Errorf("%s: get snapshot: %w", tool, err)
			}
			snapshotVersion = snap.Version
			hasSnapshot = true
		}
	}
	if hasSnapshot {
		if t.versionedFacts == nil {
			return nil, fmt.Errorf("%s: snapshot facts are not supported by provider", tool)
		}
		facts, err := t.versionedFacts.ListActiveFactsAt(ctx, userID, agentID, FactSubjectWorld, snapshotVersion)
		if err != nil {
			return nil, fmt.Errorf("%s: list snapshot facts: %w", tool, err)
		}
		return filterWorldKnowledgeFacts(facts), nil
	}
	facts, err := t.factStore.ListActiveFacts(ctx, userID, agentID, FactSubjectWorld)
	if err != nil {
		return nil, fmt.Errorf("%s: list active facts: %w", tool, err)
	}
	return filterWorldKnowledgeFacts(facts), nil
}

func filterWorldKnowledgeFacts(facts []Fact) []Fact {
	out := make([]Fact, 0, len(facts))
	for _, fact := range facts {
		if fact.Subject != FactSubjectWorld || fact.Scope != "user_agent" || fact.Status != FactStatusActive {
			continue
		}
		out = append(out, fact)
	}
	return out
}

// requireProfileCtx resolves the (userID, agentID) a profile-backed read
// targets, strictly against the session user, so group turns — which have no
// session user (D9) — fail closed. This backs soul and the version histories,
// which must never resolve to a group member by fallback.
func (t *Recall) requireProfileCtx(ctx context.Context, tool string) (string, string, error) {
	if t.profileStore == nil {
		return "", "", fmt.Errorf("%s: not supported by provider", tool)
	}
	userID := authz.UserIDFromContext(ctx)
	if userID == "" {
		return "", "", fmt.Errorf("%s: no user context", tool)
	}
	agentID := authz.AgentIDFromContext(ctx)
	if agentID == "" {
		return "", "", fmt.Errorf("%s: no agent context", tool)
	}
	return userID, agentID, nil
}

// resolveProfileTarget is requireProfileCtx for the profile itself, the one
// store a group turn may resolve through the current speaker's linked auth
// user. Soul, constraints, and history stay on requireProfileCtx and therefore
// reject the speaker fallback by construction; do not switch one of them here.
func (t *Recall) resolveProfileTarget(ctx context.Context, tool string) (string, string, error) {
	if t.profileStore == nil {
		return "", "", fmt.Errorf("%s: not supported by provider", tool)
	}
	agentID := authz.AgentIDFromContext(ctx)
	if agentID == "" {
		return "", "", fmt.Errorf("%s: no agent context", tool)
	}
	if userID := authz.UserIDFromContext(ctx); userID != "" {
		return userID, agentID, nil
	}
	if speaker, ok := CurrentSpeakerFromContext(ctx); ok && speaker.UserID != "" {
		return speaker.UserID, agentID, nil
	}
	return "", "", fmt.Errorf("%s: no linked current speaker", tool)
}

func (t *Recall) requireConstraintCtx(ctx context.Context, tool string) (string, string, error) {
	if t.constraintStore == nil {
		return "", "", fmt.Errorf("%s: not supported by provider", tool)
	}
	userID := authz.UserIDFromContext(ctx)
	if userID == "" {
		return "", "", fmt.Errorf("%s: no user context", tool)
	}
	agentID := authz.AgentIDFromContext(ctx)
	if agentID == "" {
		return "", "", fmt.Errorf("%s: no agent context", tool)
	}
	return userID, agentID, nil
}

// marshalJSON marshals v to an indented JSON string.
func marshalJSON(v any) (string, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	return string(out), nil
}
