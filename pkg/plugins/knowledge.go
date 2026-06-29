package plugins

import (
	"context"
	"time"
)

// KnowledgeType classifies a knowledge entry stored in the skills table.
type KnowledgeType string

const (
	KnowledgeTypeSkill   KnowledgeType = "skill"   // existing behavior
	KnowledgeTypeFact    KnowledgeType = "fact"    // durable project/domain fact
	KnowledgeTypeContext KnowledgeType = "context" // time-bound background info
)

// KnowledgeEntry is a fact or context entry derived from the skills table.
// These entries have DisableModelInvocation=true and never appear in the Skills prompt section.
// Active entries are injected into the ## Knowledge system prompt section.
type KnowledgeEntry struct {
	ID            string
	Name          string
	Description   string
	Content       string // the actual knowledge text (from SKILL.md)
	KnowledgeType KnowledgeType
	Status        string // draft | active | deprecated
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// KnowledgeStore queries knowledge entries (fact/context) from the skills table.
// Knowledge entries have disable_model_invocation=true and are never exposed via the skills tool.
// Active entries appear in the ## Knowledge system prompt section.
type KnowledgeStore interface {
	// ListKnowledge returns active knowledge entries. When types is empty, all knowledge
	// types are returned. Pass KnowledgeTypeFact or KnowledgeTypeContext to filter.
	ListKnowledge(ctx context.Context, vc SkillViewContext, types ...KnowledgeType) ([]KnowledgeEntry, error)

	// ExpireKnowledgeDraftsByType deprecates draft knowledge entries of the given type
	// whose created-at timestamp is before the cutoff.
	ExpireKnowledgeDraftsByType(ctx context.Context, knowledgeType KnowledgeType, before time.Time) error
}
