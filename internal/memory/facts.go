package memory

import (
	"context"
	"encoding/json"
	"time"
)

// FactSubject identifies what a durable fact is about.
type FactSubject string

const (
	FactSubjectUser  FactSubject = "user"
	FactSubjectAgent FactSubject = "agent"
	FactSubjectWorld FactSubject = "world"
)

// FactStatus is the authoritative lifecycle state for a fact.
type FactStatus string

const (
	FactStatusActive     FactStatus = "active"
	FactStatusDeprecated FactStatus = "deprecated"
)

// Fact is the long-term memory record rendered from the facts table and
// serialized into fact changelog payloads.
type Fact struct {
	ID         string          `json:"id"`
	Subject    FactSubject     `json:"subject"`
	Scope      string          `json:"scope"`
	UserID     string          `json:"user_id"`
	AgentID    string          `json:"agent_id"`
	Content    string          `json:"content"`
	Status     FactStatus      `json:"status"`
	Metadata   json.RawMessage `json:"metadata"`
	Supersedes string          `json:"supersedes,omitempty"`
	Version    int64           `json:"version"`
	Source     ChangeSource    `json:"source"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// FactWrite is the input for create/update/replace fact write operations.
type FactWrite struct {
	UserID     string
	AgentID    string
	Subject    FactSubject
	Content    string
	Metadata   json.RawMessage
	Supersedes string
	Source     ChangeSource
}

// FactStore is implemented by providers that can read current active facts for
// a user-agent pair. Profile, soul, and knowledge use different subjects.
type FactStore interface {
	ListActiveFacts(ctx context.Context, userID string, agentID string, subject FactSubject) ([]Fact, error)
}

// VersionedFactStore reconstructs facts at a frozen memory version from the
// fact changelog. Version 0 is the valid empty baseline before the first write;
// callers that need current state must use FactStore.
type VersionedFactStore interface {
	ListActiveFactsAt(ctx context.Context, userID string, agentID string, subject FactSubject, version int64) ([]Fact, error)
}

// KnowledgeUsageTracker records runtime use for returned Reflect-owned world facts.
type KnowledgeUsageTracker interface {
	TouchKnowledgeUsage(ctx context.Context, userID string, agentID string, factIDs []string) error
}
