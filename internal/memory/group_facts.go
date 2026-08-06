package memory

import (
	"context"
	"time"
)

type GroupFactSubject string

const (
	GroupFactSubjectGroup GroupFactSubject = "group"
	GroupFactSubjectHuman GroupFactSubject = "human"
	GroupFactSubjectAgent GroupFactSubject = "agent"
)

type GroupFactStatus string

const (
	GroupFactStatusActive     GroupFactStatus = "active"
	GroupFactStatusDeprecated GroupFactStatus = "deprecated"
)

const GroupFactSourceReflect = "reflect"

// GroupFact is one durable, atomic fact scoped to a single group. SubjectID is
// empty only for group-level facts; human and agent subjects use their stable
// public actor IDs.
type GroupFact struct {
	ID        string
	GroupID   string
	Subject   GroupFactSubject
	SubjectID string
	Content   string
	Status    GroupFactStatus
	Source    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// GroupActorDisplayName is the latest public presentation name observed for a
// typed actor in one group. Identity remains (Subject, SubjectID).
type GroupActorDisplayName struct {
	Subject     GroupFactSubject
	SubjectID   string
	DisplayName string
}

type GroupFactAction string

const (
	GroupFactActionNoop          GroupFactAction = "noop"
	GroupFactActionCreate        GroupFactAction = "create"
	GroupFactActionReplaceMany   GroupFactAction = "replace_many"
	GroupFactActionDeprecateMany GroupFactAction = "deprecate_many"
)

// GroupFactOperation is the persistence-facing mutation contract. Candidate
// refs, evidence, scores, and rationale remain runtime-only groupingest data.
type GroupFactOperation struct {
	Action        GroupFactAction
	Subject       GroupFactSubject
	SubjectID     string
	TargetFactIDs []string
	NewContent    string
}

type GroupFactPlan struct {
	Operations []GroupFactOperation
}

// GroupFactStore is the narrow read capability shared by Group Reflect and the
// runtime cache.
type GroupFactStore interface {
	ListActiveGroupFacts(ctx context.Context, groupID string) ([]GroupFact, error)
	GetGroupFactVersion(ctx context.Context, groupID string) (int64, error)
	ListGroupActorDisplayNames(ctx context.Context, groupID string) ([]GroupActorDisplayName, error)
}
