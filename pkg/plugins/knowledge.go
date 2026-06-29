package plugins

import "time"

// KnowledgeType classifies a facts-backed knowledge entry rendered in prompts.
type KnowledgeType string

const (
	KnowledgeTypeFact    KnowledgeType = "fact"    // durable project/domain fact
	KnowledgeTypeContext KnowledgeType = "context" // time-bound background info
)

// KnowledgeEntry is a subject=world fact projected into the Knowledge prompt
// section. It is not a callable skill and is not stored in the skills table.
type KnowledgeEntry struct {
	ID            string
	Name          string
	Description   string
	Content       string
	KnowledgeType KnowledgeType
	Status        string // draft | active | deprecated
	CreatedAt     time.Time
	UpdatedAt     time.Time
}
