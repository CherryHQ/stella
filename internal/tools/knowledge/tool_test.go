package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type memoryKnowledgeStore struct {
	knowledge  []pkgplugins.KnowledgeEntry
	owners     map[string]knowledgeOwner
	lastCreate pkgplugins.KnowledgeCreateParams
}

type knowledgeOwner struct {
	scope   string
	userID  string
	agentID string
}

func (s *memoryKnowledgeStore) ListKnowledge(context.Context, pkgplugins.SkillViewContext, ...pkgplugins.KnowledgeType) ([]pkgplugins.KnowledgeEntry, error) {
	return s.knowledge, nil
}

func (s *memoryKnowledgeStore) ExpireKnowledgeDraftsByType(context.Context, pkgplugins.KnowledgeType, time.Time) error {
	return nil
}

func (s *memoryKnowledgeStore) CreateKnowledge(_ context.Context, params pkgplugins.KnowledgeCreateParams) (pkgplugins.KnowledgeEntry, error) {
	s.lastCreate = params
	if s.owners == nil {
		s.owners = make(map[string]knowledgeOwner)
	}
	entry := pkgplugins.KnowledgeEntry{
		ID:            fmt.Sprintf("k%d", len(s.knowledge)+1),
		Name:          params.Name,
		Description:   params.Description,
		Content:       params.Content,
		Status:        params.Status,
		KnowledgeType: params.KnowledgeType,
		Evidence:      params.Evidence,
		Confidence:    params.Confidence,
		ExpiresAt:     params.ExpiresAt,
		Supersedes:    params.Supersedes,
		Metadata:      params.Metadata,
	}
	s.knowledge = append(s.knowledge, entry)
	s.owners[entry.ID] = knowledgeOwner{scope: params.Scope, userID: params.UserID, agentID: params.AgentID}
	return entry, nil
}

func (s *memoryKnowledgeStore) ListKnowledgeByNameAndScope(_ context.Context, name string, scope string, userID string, agentID string) ([]pkgplugins.KnowledgeEntry, error) {
	var out []pkgplugins.KnowledgeEntry
	for _, entry := range s.knowledge {
		owner := s.owners[entry.ID]
		if entry.Name == name && owner.scope == scope && owner.userID == userID && owner.agentID == agentID {
			out = append(out, entry)
		}
	}
	return out, nil
}

func (s *memoryKnowledgeStore) UpdateKnowledge(_ context.Context, params pkgplugins.KnowledgeUpdateParams) (pkgplugins.KnowledgeEntry, error) {
	for i, entry := range s.knowledge {
		if entry.ID != params.ID {
			continue
		}
		entry.Name = params.Name
		entry.Description = params.Description
		entry.Content = params.Content
		entry.Status = params.Status
		entry.Evidence = params.Evidence
		entry.Confidence = params.Confidence
		entry.ExpiresAt = params.ExpiresAt
		entry.Supersedes = params.Supersedes
		entry.Metadata = params.Metadata
		s.knowledge[i] = entry
		return entry, nil
	}
	return pkgplugins.KnowledgeEntry{}, fmt.Errorf("missing knowledge")
}

func (s *memoryKnowledgeStore) DeprecateKnowledge(_ context.Context, id string) error {
	for i, entry := range s.knowledge {
		if entry.ID == id {
			s.knowledge[i].Status = statusDeprecated
			return nil
		}
	}
	return fmt.Errorf("missing knowledge")
}

func TestCreateKnowledgeWritesFirstClassEntry(t *testing.T) {
	store := &memoryKnowledgeStore{}
	tool := NewTool(store)
	ctx := ctxWithUserAndAgent("u1", "a1")
	confidence := 0.8

	result, err := tool.create(ctx, map[string]any{
		"name":        "project-fact",
		"description": "records a project fact",
		"content":     "The project uses goose migrations.",
		"kind":        "fact",
		"scope":       "agent",
		"evidence":    "plan.md confirmed goose migrations",
		"confidence":  confidence,
	})
	if err != nil {
		t.Fatalf("create knowledge: %v", err)
	}
	if !strings.Contains(result, `Knowledge fact "project-fact" created as draft`) {
		t.Fatalf("unexpected create result: %q", result)
	}
	if len(store.knowledge) != 1 {
		t.Fatalf("expected one knowledge row, got %d", len(store.knowledge))
	}
	got := store.knowledge[0]
	if got.KnowledgeType != pkgplugins.KnowledgeTypeFact || got.Status != statusDraft {
		t.Fatalf("unexpected knowledge entry: %+v", got)
	}
	if store.lastCreate.Scope != "user_agent" || store.lastCreate.UserID != "u1" || store.lastCreate.AgentID != "a1" {
		t.Fatalf("unexpected owner fields: %+v", store.lastCreate)
	}
	if got.Confidence == nil || *got.Confidence != confidence {
		t.Fatalf("unexpected confidence: %+v", got.Confidence)
	}
	var meta map[string]string
	if err := json.Unmarshal(got.Metadata, &meta); err != nil {
		t.Fatalf("metadata json: %v", err)
	}
	if meta["created-at"] == "" || len(meta) != 1 {
		t.Fatalf("unexpected metadata: %v", meta)
	}
}

func TestPatchAndDeprecateKnowledgeUseKnowledgeTool(t *testing.T) {
	store := &memoryKnowledgeStore{
		knowledge: []pkgplugins.KnowledgeEntry{{
			ID:            "k1",
			Name:          "sprint-context",
			Description:   "current sprint context",
			Content:       "Old context",
			KnowledgeType: pkgplugins.KnowledgeTypeContext,
			Status:        statusDraft,
			Metadata:      json.RawMessage(`{"created-at":"2026-06-22T00:00:00Z"}`),
		}},
		owners: map[string]knowledgeOwner{
			"k1": {scope: "user", userID: "u1"},
		},
	}
	tool := NewTool(store)
	ctx := ctxWithUserAndAgent("u1", "a1")

	if _, err := tool.patch(ctx, map[string]any{
		"name":    "sprint-context",
		"kind":    "context",
		"status":  "active",
		"content": "New context",
	}); err != nil {
		t.Fatalf("patch knowledge: %v", err)
	}
	if store.knowledge[0].Status != statusActive || store.knowledge[0].Content != "New context" {
		t.Fatalf("unexpected patched knowledge: %+v", store.knowledge[0])
	}
	if _, err := tool.deprecate(ctx, map[string]any{
		"name": "sprint-context",
		"kind": "context",
	}); err != nil {
		t.Fatalf("deprecate knowledge: %v", err)
	}
	if store.knowledge[0].Status != statusDeprecated {
		t.Fatalf("expected deprecated, got %+v", store.knowledge[0])
	}
}

func ctxWithUserAndAgent(userID, agentID string) context.Context {
	ctx := context.Background()
	ctx = memory.WithUserID(ctx, userID)
	return memory.WithAgentID(ctx, agentID)
}
