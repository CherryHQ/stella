package knowledge

import (
	"context"
	"encoding/json"
	"time"
)

type Kind string

const (
	KindFact    Kind = "fact"
	KindContext Kind = "context"
)

func (k Kind) Valid() bool {
	return k == KindFact || k == KindContext
}

type Status string

const (
	StatusDraft      Status = "draft"
	StatusActive     Status = "active"
	StatusDeprecated Status = "deprecated"
)

func (s Status) Valid() bool {
	return s == StatusDraft || s == StatusActive || s == StatusDeprecated
}

type ViewContext struct {
	UserID  string
	AgentID string
}

type Entry struct {
	ID         string
	Kind       Kind
	Scope      string
	UserID     string
	AgentID    string
	Name       string
	Content    string
	Status     Status
	Evidence   json.RawMessage
	Confidence *float64
	ExpiresAt  *time.Time
	Supersedes *string
	Metadata   json.RawMessage
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type CreateParams struct {
	Kind       Kind
	Scope      string
	UserID     string
	AgentID    string
	Name       string
	Content    string
	Status     Status
	Evidence   json.RawMessage
	Confidence *float64
	ExpiresAt  *time.Time
	Supersedes *string
	Metadata   json.RawMessage
}

type UpdateParams struct {
	ID         string
	Name       string
	Content    string
	Status     Status
	Evidence   json.RawMessage
	Confidence *float64
	ExpiresAt  *time.Time
	Supersedes *string
	Metadata   json.RawMessage
}

type Store interface {
	Create(ctx context.Context, params CreateParams) (Entry, error)
	Get(ctx context.Context, id string) (Entry, error)
	ListActive(ctx context.Context, vc ViewContext, kinds ...Kind) ([]Entry, error)
	ListByScope(ctx context.Context, scope string, userID string, agentID string) ([]Entry, error)
	ListByNameAndScope(ctx context.Context, name string, scope string, userID string, agentID string) ([]Entry, error)
	Update(ctx context.Context, params UpdateParams) (Entry, error)
	Deprecate(ctx context.Context, id string) error
	ExpireDraftsByType(ctx context.Context, kind Kind, before time.Time) error
}
