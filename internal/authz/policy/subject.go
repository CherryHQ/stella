package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/CherryHQ/stella/internal/authz"
)

// ErrInvalidSelector is returned when a subject selector is malformed: the zero
// selector, an "Any" selector that also carries dimensions, or an unknown actor
// kind / role / grant.
var ErrInvalidSelector = errors.New("authz/policy: invalid subject selector")

// Selector is the typed subject-matching half of a custom policy — WHICH actors
// the policy applies to. Every custom policy must name its subjects: the zero
// Selector is invalid and matches nothing (fail closed), and "any actor" is the
// explicit AnySubject() value, never the default.
//
// A selector constrains up to three dimensions:
//   - actor kinds (user/agent/group/group_agent/system),
//   - roles (user/admin),
//   - exact typed grants (kind + key) — this is how an Authority's grants are
//     consumed for system/group/user invocation confinement (a named system
//     maintenance grant, a specific channel binding, a specific group tool).
//
// Within one dimension the listed members are OR'd (any one matches). Across the
// specified dimensions they are AND'd (the actor must satisfy every named
// dimension). An empty dimension is unconstrained on that axis, but at least one
// dimension must be non-empty unless the selector is the explicit Any.
type Selector struct {
	matchAny bool
	kinds    []authz.ActorKind
	roles    []authz.Role
	grants   []authz.Grant
}

// AnySubject is the explicit "matches every actor" selector. It is the only way
// to write an unconstrained subject; a bare/zero Selector never matches.
func AnySubject() Selector { return Selector{matchAny: true} }

// SubjectBuilder assembles a Selector across dimensions. Repeated calls append.
type SubjectBuilder struct{ sel Selector }

// NewSubjectBuilder starts an empty (as-yet invalid) selector builder.
func NewSubjectBuilder() *SubjectBuilder { return &SubjectBuilder{} }

// Kinds adds actor-kind members (OR within the kind dimension).
func (b *SubjectBuilder) Kinds(ks ...authz.ActorKind) *SubjectBuilder {
	b.sel.kinds = append(b.sel.kinds, ks...)
	return b
}

// Roles adds role members (OR within the role dimension).
func (b *SubjectBuilder) Roles(rs ...authz.Role) *SubjectBuilder {
	b.sel.roles = append(b.sel.roles, rs...)
	return b
}

// Grants adds exact typed grant members (OR within the grant dimension). A grant
// matches only on an exact kind+key, so a channel/group/agent grant cannot cross
// into another.
func (b *SubjectBuilder) Grants(gs ...authz.Grant) *SubjectBuilder {
	b.sel.grants = append(b.sel.grants, gs...)
	return b
}

// Build returns the assembled selector (still subject to validation at compile).
func (b *SubjectBuilder) Build() Selector { return b.sel }

// validate reports whether the selector is well-formed. The zero selector and an
// Any-with-dimensions selector are rejected; every named kind/role/grant must be
// a valid catalog member.
func (s Selector) validate() error {
	if s.matchAny {
		if len(s.kinds) > 0 || len(s.roles) > 0 || len(s.grants) > 0 {
			return fmt.Errorf("%w: Any selector must carry no dimensions", ErrInvalidSelector)
		}
		return nil
	}
	if len(s.kinds) == 0 && len(s.roles) == 0 && len(s.grants) == 0 {
		return fmt.Errorf("%w: zero selector matches nothing; use AnySubject() for any actor", ErrInvalidSelector)
	}
	for _, k := range s.kinds {
		if !k.Valid() {
			return fmt.Errorf("%w: unknown actor kind %d", ErrInvalidSelector, k)
		}
	}
	for _, r := range s.roles {
		if !r.Valid() {
			return fmt.Errorf("%w: unknown role %d", ErrInvalidSelector, r)
		}
	}
	for _, g := range s.grants {
		if !g.Valid() {
			return fmt.Errorf("%w: invalid grant (kind+key required)", ErrInvalidSelector)
		}
	}
	return nil
}

// matches reports whether an Authority satisfies the selector. A zero selector
// (which never reaches a compiled policy) matches nothing.
func (s Selector) matches(a authz.Authority) bool {
	if s.matchAny {
		return true
	}
	if len(s.kinds) == 0 && len(s.roles) == 0 && len(s.grants) == 0 {
		return false // fail closed: an unspecified selector grants nothing
	}
	if len(s.kinds) > 0 && !slices.Contains(s.kinds, a.Kind()) {
		return false
	}
	if len(s.roles) > 0 && !slices.ContainsFunc(s.roles, a.HasRole) {
		return false
	}
	if len(s.grants) > 0 && !slices.ContainsFunc(s.grants, a.HasGrant) {
		return false
	}
	return true
}

// grantRef is the JSON form of an exact grant (kind + key).
type grantRef struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
}

// subjectDoc is the JSON shape stored in authz_policy.subjects.
type subjectDoc struct {
	Any    bool       `json:"any,omitempty"`
	Kinds  []string   `json:"kinds,omitempty"`
	Roles  []string   `json:"roles,omitempty"`
	Grants []grantRef `json:"grants,omitempty"`
}

// marshalSubjects validates and encodes a selector for storage. An invalid
// selector cannot be persisted.
func marshalSubjects(s Selector) (json.RawMessage, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	doc := subjectDoc{Any: s.matchAny}
	for _, k := range s.kinds {
		doc.Kinds = append(doc.Kinds, k.String())
	}
	for _, r := range s.roles {
		doc.Roles = append(doc.Roles, r.String())
	}
	for _, g := range s.grants {
		doc.Grants = append(doc.Grants, grantRef{Kind: g.Kind().String(), Key: g.Key()})
	}
	return json.Marshal(doc)
}

// unmarshalSubjects decodes and REVALIDATES a stored selector. A malformed
// document, unknown catalog value, or invalid selector fails closed — a
// malicious/legacy active row therefore fails the whole reload (and thus Begin).
func unmarshalSubjects(raw json.RawMessage) (Selector, error) {
	var doc subjectDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Selector{}, fmt.Errorf("%w: decode: %w", ErrInvalidSelector, err)
	}
	sel := Selector{matchAny: doc.Any}
	for _, ks := range doc.Kinds {
		k, ok := parseActorKind(ks)
		if !ok {
			return Selector{}, fmt.Errorf("%w: unknown actor kind %q", ErrInvalidSelector, ks)
		}
		sel.kinds = append(sel.kinds, k)
	}
	for _, rs := range doc.Roles {
		r, ok := parseRole(rs)
		if !ok {
			return Selector{}, fmt.Errorf("%w: unknown role %q", ErrInvalidSelector, rs)
		}
		sel.roles = append(sel.roles, r)
	}
	for _, gr := range doc.Grants {
		gk, ok := parseGrantKind(gr.Kind)
		if !ok {
			return Selector{}, fmt.Errorf("%w: unknown grant kind %q", ErrInvalidSelector, gr.Kind)
		}
		g, err := authz.NewGrant(gk, gr.Key) // rejects empty key
		if err != nil {
			return Selector{}, fmt.Errorf("%w: %w", ErrInvalidSelector, err)
		}
		sel.grants = append(sel.grants, g)
	}
	if err := sel.validate(); err != nil {
		return Selector{}, err
	}
	return sel, nil
}
