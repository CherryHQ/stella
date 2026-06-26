package plugins

import (
	"context"
	"encoding/json"
	"time"
)

// KnowledgeType classifies a first-class knowledge entry.
type KnowledgeType string

const (
	KnowledgeTypeFact    KnowledgeType = "fact"    // durable project/domain fact
	KnowledgeTypeContext KnowledgeType = "context" // time-bound background info
)

// KnowledgeEntry is a fact or context entry from the knowledge domain.
// Active entries are injected into the ## Knowledge system prompt section.
type KnowledgeEntry struct {
	ID            string
	Name          string
	Description   string
	Content       string
	KnowledgeType KnowledgeType
	Status        string // draft | active | deprecated
	Evidence      json.RawMessage
	Confidence    *float64
	ExpiresAt     *time.Time
	Supersedes    *string
	Metadata      json.RawMessage
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// KnowledgeCreateParams describes a new first-class knowledge entry.
type KnowledgeCreateParams struct {
	Name          string
	Description   string
	Content       string
	KnowledgeType KnowledgeType
	Scope         string
	UserID        string
	AgentID       string
	Status        string
	Evidence      json.RawMessage
	Confidence    *float64
	ExpiresAt     *time.Time
	Supersedes    *string
	Metadata      json.RawMessage
}

// KnowledgeUpdateParams replaces mutable fields on an existing knowledge entry.
type KnowledgeUpdateParams struct {
	ID          string
	Name        string
	Description string
	Content     string
	Status      string
	Evidence    json.RawMessage
	Confidence  *float64
	ExpiresAt   *time.Time
	Supersedes  *string
	Metadata    json.RawMessage
}

// KnowledgeStore queries first-class fact/context knowledge.
type KnowledgeStore interface {
	// ListKnowledge returns active knowledge entries. When types is empty, all knowledge
	// types are returned. Pass KnowledgeTypeFact or KnowledgeTypeContext to filter.
	ListKnowledge(ctx context.Context, vc SkillViewContext, types ...KnowledgeType) ([]KnowledgeEntry, error)

	// ExpireKnowledgeDraftsByType deprecates draft knowledge entries of the given type
	// whose created-at timestamp is before the cutoff.
	ExpireKnowledgeDraftsByType(ctx context.Context, knowledgeType KnowledgeType, before time.Time) error
}

// KnowledgeWriter creates and maintains first-class knowledge records from tools.
type KnowledgeWriter interface {
	CreateKnowledge(ctx context.Context, params KnowledgeCreateParams) (KnowledgeEntry, error)
	ListKnowledgeByNameAndScope(ctx context.Context, name string, scope string, userID string, agentID string) ([]KnowledgeEntry, error)
	UpdateKnowledge(ctx context.Context, params KnowledgeUpdateParams) (KnowledgeEntry, error)
	DeprecateKnowledge(ctx context.Context, id string) error
}
