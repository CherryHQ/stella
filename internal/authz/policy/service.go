package policy

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// policy lifecycle statuses (validated in Go, not a DB CHECK, so the set can
// grow without a migration).
const (
	statusActive      = "active"
	statusQuarantined = "quarantined"
	statusInactive    = "inactive"
)

var (
	// ErrResourceInactive is returned when a custom-policy write targets a
	// resource that is not activated for custom policies in this subphase. It
	// fails closed: no row is written and the revision is not bumped.
	ErrResourceInactive = errors.New("authz/policy: resource does not accept custom policies")
	// ErrPolicyNotFound is returned when an update/activate/delete targets a
	// missing policy id.
	ErrPolicyNotFound = errors.New("authz/policy: policy not found")
	// ErrPolicyNotActive is returned when UpdatePolicy targets a row that is not
	// currently active. Update never silently activates a quarantined/inactive
	// row — use ActivatePolicy for that explicit transition.
	ErrPolicyNotActive = errors.New("authz/policy: policy is not active")
	// ErrPolicyAlreadyActive is returned when ActivatePolicy targets a row that
	// is already active. Use UpdatePolicy to edit an active policy.
	ErrPolicyAlreadyActive = errors.New("authz/policy: policy is already active")
)

// Effect is the allow/deny axis of a custom policy.
type Effect string

const (
	// EffectAllow grants the matched action.
	EffectAllow Effect = "allow"
	// EffectDeny forbids it; deny overrides any allow.
	EffectDeny Effect = "deny"
)

// Predicate is one attribute comparison in a custom policy. Construct with the
// Eq/Neq/In/NotIn helpers rather than a struct literal.
type Predicate = predicate

// Eq builds an equality predicate.
func Eq(attr, value string) Predicate { return Predicate{Attr: attr, Op: opEq, Value: value} }

// Neq builds an inequality predicate.
func Neq(attr, value string) Predicate { return Predicate{Attr: attr, Op: opNeq, Value: value} }

// In builds a set-membership predicate.
func In(attr string, values ...string) Predicate {
	return Predicate{Attr: attr, Op: opIn, Values: values}
}

// NotIn builds a set-exclusion predicate.
func NotIn(attr string, values ...string) Predicate {
	return Predicate{Attr: attr, Op: opNotIn, Values: values}
}

// PolicyInput is a validated custom-policy authoring request. Resource/Action
// are typed catalog members; Subjects is a required typed selector (the zero
// selector is rejected); Predicates are validated against the resource schema.
// There is no map[string]any anywhere in this shape.
type PolicyInput struct {
	Name       string
	Resource   authz.ResourceType
	Action     authz.Action
	Effect     Effect
	Subjects   Selector
	Predicates []Predicate
	Priority   int64
}

// Service owns custom-policy mutation. Every method that changes policy state
// runs one transaction that bumps the commit-ordered revision counter; there is
// no public API that mutates the revision directly.
//
// AUTHORIZATION OWNERSHIP: mutating authorization policy is itself a protected,
// admin-only control-plane use case. This shadow-only subphase does NOT enforce
// that — the Service takes no Authority and is not wired to any transport. Before
// any production callsite invokes these methods, the platform/control-plane stack
// (#712) must gate them behind an Authorizer decision (an admin control-plane
// policy over the policy resource), so that authoring policy is not a privilege
// escalation. This obligation is recorded here and in foundation-baseline.md §11.
type Service struct {
	store *policyStore

	// beforeCommit is a test-only hook run inside the mutation transaction after
	// the write and before commit. nil in production.
	beforeCommit func()
}

// NewService builds a mutation Service sharing an Authorizer's private store.
func NewService(az *Authorizer) *Service {
	return &Service{store: az.store}
}

// CreatePolicy validates and writes a new active custom policy for a
// shadow-enabled resource, bumping the revision in the same transaction. It
// returns the new policy id and the committed revision. A write to an inactive
// resource is rejected before any transaction begins (fail closed).
func (s *Service) CreatePolicy(ctx context.Context, in PolicyInput) (id string, revision int64, err error) {
	attrs, subjects, err := s.validate(in)
	if err != nil {
		return "", 0, err
	}
	id = uuid.Must(uuid.NewV7()).String()
	rev, err := s.store.mutate(ctx, s.beforeCommit, func(qtx *sqlc.Queries, _ int64) error {
		_, e := qtx.CreateAuthzPolicy(ctx, sqlc.CreateAuthzPolicyParams{
			ID:               id,
			Name:             in.Name,
			ResourceType:     in.Resource.String(),
			Action:           in.Action.String(),
			Effect:           string(in.Effect),
			Subjects:         subjects,
			Attributes:       attrs,
			CatalogVersion:   int64(authz.CatalogVersion),
			Status:           statusActive,
			QuarantineReason: "",
			Priority:         in.Priority,
		})
		return e
	})
	if err != nil {
		return "", 0, err
	}
	return id, rev, nil
}

// UpdatePolicy replaces an ALREADY-ACTIVE policy's definition and bumps the
// revision. It rejects a quarantined/inactive target (ErrPolicyNotActive): an
// update can never silently activate a row. The target resource must accept
// custom policies.
func (s *Service) UpdatePolicy(ctx context.Context, id string, in PolicyInput) (revision int64, err error) {
	attrs, subjects, err := s.validate(in)
	if err != nil {
		return 0, err
	}
	return s.store.mutate(ctx, s.beforeCommit, func(qtx *sqlc.Queries, _ int64) error {
		row, e := qtx.GetAuthzPolicy(ctx, id)
		if e != nil {
			return mapGetErr(id, e)
		}
		if row.Status != statusActive {
			return fmt.Errorf("%w: %s (status %q)", ErrPolicyNotActive, id, row.Status)
		}
		return writePolicy(ctx, qtx, id, in, subjects, attrs)
	})
}

// ActivatePolicy re-authors and activates an existing quarantined/inactive row
// from a complete, validated PolicyInput, explicitly transitioning it to active
// and bumping the revision atomically. It rejects an already-active target
// (ErrPolicyAlreadyActive) and an inactive resource (ErrResourceInactive), so
// activation can only target a shadow-enabled resource (currently only Agent).
func (s *Service) ActivatePolicy(ctx context.Context, id string, in PolicyInput) (revision int64, err error) {
	attrs, subjects, err := s.validate(in)
	if err != nil {
		return 0, err
	}
	return s.store.mutate(ctx, s.beforeCommit, func(qtx *sqlc.Queries, _ int64) error {
		row, e := qtx.GetAuthzPolicy(ctx, id)
		if e != nil {
			return mapGetErr(id, e)
		}
		if row.Status == statusActive {
			return fmt.Errorf("%w: %s", ErrPolicyAlreadyActive, id)
		}
		return writePolicy(ctx, qtx, id, in, subjects, attrs)
	})
}

// DeletePolicy removes a policy and bumps the revision.
func (s *Service) DeletePolicy(ctx context.Context, id string) (revision int64, err error) {
	return s.store.mutate(ctx, s.beforeCommit, func(qtx *sqlc.Queries, _ int64) error {
		if _, e := qtx.GetAuthzPolicy(ctx, id); e != nil {
			return mapGetErr(id, e)
		}
		return qtx.DeleteAuthzPolicy(ctx, id)
	})
}

// writePolicy updates a row to an active state carrying the validated
// definition. It is shared by Update (active→active edit) and Activate
// (quarantined/inactive→active re-author).
func writePolicy(ctx context.Context, qtx *sqlc.Queries, id string, in PolicyInput, subjects, attrs []byte) error {
	_, e := qtx.UpdateAuthzPolicy(ctx, sqlc.UpdateAuthzPolicyParams{
		ID:               id,
		Name:             in.Name,
		ResourceType:     in.Resource.String(),
		Action:           in.Action.String(),
		Effect:           string(in.Effect),
		Subjects:         subjects,
		Attributes:       attrs,
		CatalogVersion:   int64(authz.CatalogVersion),
		Status:           statusActive,
		QuarantineReason: "",
		Priority:         in.Priority,
	})
	return e
}

// mapGetErr maps a GetAuthzPolicy error: only a genuine no-rows result becomes
// ErrPolicyNotFound; context cancellation, connection loss, and every other DB
// error propagate unchanged so callers do not mistake infrastructure failure for
// "not found".
func mapGetErr(id string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrPolicyNotFound, id)
	}
	return fmt.Errorf("authz/policy: load policy %s: %w", id, err)
}

// validate enforces activation + catalog + subject + schema before a write,
// returning the encoded (subjects, attributes) documents. It never touches the
// database.
func (s *Service) validate(in PolicyInput) (attrs, subjects []byte, err error) {
	if !resourceAcceptsCustomPolicy(in.Resource) {
		return nil, nil, fmt.Errorf("%w: %s", ErrResourceInactive, in.Resource)
	}
	// Compile as a dry run so a bad subject/resource/action/effect/predicate is
	// rejected before the transaction, matching what the loader enforces at read.
	if _, err := compileCustom("", in.Resource.String(), in.Action.String(), string(in.Effect), in.Subjects, in.Predicates); err != nil {
		return nil, nil, err
	}
	attrsRaw, err := marshalAttributes(in.Predicates)
	if err != nil {
		return nil, nil, err
	}
	subjectsRaw, err := marshalSubjects(in.Subjects)
	if err != nil {
		return nil, nil, err
	}
	return attrsRaw, subjectsRaw, nil
}

// QuarantinedPolicy is an operator-facing view of an inert policy row.
type QuarantinedPolicy struct {
	ID     string
	Name   string
	Reason string
}

// ListQuarantined returns the quarantined policy rows with their operator
// diagnostics. It is a read-only diagnostic surface, not a decision path.
func (s *Service) ListQuarantined(ctx context.Context) ([]QuarantinedPolicy, error) {
	rows, err := s.store.q.ListQuarantinedAuthzPolicy(ctx)
	if err != nil {
		return nil, fmt.Errorf("authz/policy: list quarantined: %w", err)
	}
	out := make([]QuarantinedPolicy, 0, len(rows))
	for _, r := range rows {
		out = append(out, QuarantinedPolicy{ID: r.ID, Name: r.Name, Reason: r.QuarantineReason})
	}
	return out, nil
}
