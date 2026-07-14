// Package policy is the concrete, PostgreSQL-backed implementation of the
// authz.Authorizer / authz.Evaluation contract defined in internal/authz.
//
// It owns three things the pure authz leaf deliberately does not:
//   - the per-resource custom-policy attribute schemas and the typed builders
//     that produce validated authz.Resource values (no map[string]any ever
//     crosses this boundary);
//   - the revision-verified immutable evaluation snapshot, backed by a
//     commit-ordered PostgreSQL revision counter;
//   - the custom-policy mutation service that owns the transaction + revision
//     bump, plus the resource-activation catalog that keeps not-yet-cut-over
//     resources inert.
//
// This subphase (Stack 2 / #707 B) is shadow-only: nothing here is wired into a
// production decision path, so it cannot create a dual-authoritative decision.
package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/CherryHQ/stella/internal/authz"
)

// ErrSchema is returned when an attribute name, operator, or value does not
// satisfy a resource's custom-policy attribute schema.
var ErrSchema = errors.New("authz/policy: attribute schema violation")

// attrKind is the value domain of a custom-policy attribute.
type attrKind uint8

const (
	kindString attrKind = iota // free-form string
	kindBool                   // "true" / "false"
	kindEnum                   // one of a fixed set
)

// operator is a custom-policy predicate operator. The set is intentionally
// small and closed; an unknown operator fails schema validation.
type operator string

const (
	opEq    operator = "eq"
	opNeq   operator = "neq"
	opIn    operator = "in"
	opNotIn operator = "not_in"
)

// attrSpec describes one attribute of a resource: its value kind, the enum
// members when kindEnum, and the operators a predicate may use against it.
type attrSpec struct {
	kind attrKind
	enum []string
	ops  []operator
}

func (s attrSpec) allowsOp(op operator) bool {
	return slices.Contains(s.ops, op)
}

func (s attrSpec) validValue(v string) bool {
	switch s.kind {
	case kindBool:
		return v == "true" || v == "false"
	case kindEnum:
		return slices.Contains(s.enum, v)
	default:
		return true
	}
}

// resourceSchema is the closed set of custom-policy attributes for one resource
// type. A resource with no meaningful policy attributes still has a schema (an
// empty attribute set) so that every catalog member is covered and any predicate
// against it fails validation.
type resourceSchema struct {
	attrs map[string]attrSpec
}

// reusable attribute specs.
var (
	boolAttr   = attrSpec{kind: kindBool, ops: []operator{opEq, opNeq}}
	stringAttr = attrSpec{kind: kindString, ops: []operator{opEq, opNeq, opIn, opNotIn}}
)

func enumAttr(members ...string) attrSpec {
	return attrSpec{kind: kindEnum, enum: members, ops: []operator{opEq, opNeq, opIn, opNotIn}}
}

// schemas is the per-resource custom-policy attribute schema for every catalog
// ResourceType. Entries are grounded in the plan's authorization matrix
// (owner/agent/scope/kind/state/status/…); only the Agent schema is exercised by
// an active policy in this shadow subphase, but a schema exists for every
// resource so validation is total and a coverage test can assert completeness.
var schemas = map[authz.ResourceType]resourceSchema{
	authz.ResourceAgent: {attrs: map[string]attrSpec{
		"scope":       enumAttr("system", "user", "shared"),
		"assigned":    boolAttr,
		"creator":     stringAttr,
		"is_creator":  boolAttr,
		"is_executor": boolAttr,
		"dedicated":   boolAttr,
		"status":      enumAttr("enabled", "disabled"),
	}},
	authz.ResourceSession:   {attrs: ownerAgentKindState()},
	authz.ResourceWorkspace: {attrs: ownerAgentKindState()},
	authz.ResourceSkill: {attrs: map[string]attrSpec{
		"scope":  enumAttr("system", "system_agent", "user", "user_agent"),
		"owner":  stringAttr,
		"agent":  stringAttr,
		"source": stringAttr,
		// Derived by the PEP from the immutable Authority and the loaded skill
		// row; a route or request body can never assert it.
		"is_owner": boolAttr,
	}},
	authz.ResourceGoal:      {attrs: ownerAgentState()},
	authz.ResourceWorkflow:  {attrs: ownerAgentState()},
	authz.ResourceScheduler: {attrs: ownerAgentKindState()},
	// #711: Vault entries live in four durable scopes (user/user_agent are
	// user-owned; system/system_agent are admin-managed). is_owner is derived by
	// the vault PEP from the Authority and the loaded entry's owner/agent columns.
	authz.ResourceVault: {attrs: map[string]attrSpec{
		"scope":    enumAttr("user", "user_agent", "system", "system_agent"),
		"owner":    stringAttr,
		"agent":    stringAttr,
		"is_owner": boolAttr,
	}},
	// Connection/Email/Share/Recally are Authority-bound user capabilities enforced
	// by their domain Access services and user-scoped durable queries, not custom
	// policy. These dormant schemas exist only so schema coverage stays total for
	// every catalog member; no active policy ever evaluates them.
	authz.ResourceConnection: {attrs: map[string]attrSpec{
		"owner": stringAttr, "agent": stringAttr, "type": stringAttr,
	}},
	authz.ResourceEmail: {attrs: map[string]attrSpec{
		"owner": stringAttr, "agent": stringAttr, "type": stringAttr,
	}},
	authz.ResourceShare: {attrs: ownerAgentSensitivity()},
	authz.ResourceRecally: {attrs: map[string]attrSpec{
		"owner": stringAttr, "agent": stringAttr, "type": stringAttr,
	}},
	authz.ResourceUserData: {attrs: map[string]attrSpec{
		"owner": stringAttr, "agent": stringAttr,
	}},
	authz.ResourceProvider: {attrs: kindStatus()},
	authz.ResourceSettings: {attrs: kindStatus()},
	authz.ResourcePlugin:   {attrs: kindOwnerStatus()},
	authz.ResourceChannel:  {attrs: kindOwnerStatus()},
	authz.ResourceTool: {attrs: map[string]attrSpec{
		"scope": enumAttr("public", "group"),
		"owner": stringAttr,
	}},
	authz.ResourceUser:  {attrs: map[string]attrSpec{"owner": stringAttr}},
	authz.ResourceGroup: {attrs: map[string]attrSpec{"owner": stringAttr}},
	authz.ResourceMembership: {attrs: map[string]attrSpec{
		"owner": stringAttr, "agent": stringAttr,
	}},
	authz.ResourceToken:        {attrs: map[string]attrSpec{"owner": stringAttr}},
	authz.ResourceMCP:          {attrs: ownerStatus()},
	authz.ResourceAuth:         {attrs: map[string]attrSpec{"kind": stringAttr}},
	authz.ResourceWebhook:      {attrs: ownerStatus()},
	authz.ResourceEmbeddingJob: {attrs: ownerStatus()},
	authz.ResourceSystemCatalog: {attrs: map[string]attrSpec{
		"scope": enumAttr("public"),
	}},
}

func ownerAgentState() map[string]attrSpec {
	return map[string]attrSpec{
		"owner": stringAttr, "agent": stringAttr, "state": stringAttr,
		// Derived by the PEP from the immutable Authority and the durable
		// resource facts; a route or request body can assert neither bit.
		"is_owner": boolAttr, "is_executor": boolAttr,
	}
}

func ownerAgentKindState() map[string]attrSpec {
	return map[string]attrSpec{
		"owner": stringAttr, "agent": stringAttr, "kind": stringAttr, "state": stringAttr,
		// Derived from the immutable Authority and the durable resource facts;
		// callers cannot assert either bit through a route or request body.
		"is_owner": boolAttr, "is_executor": boolAttr, "is_group": boolAttr, "is_same_group": boolAttr,
	}
}

// ownerAgentSensitivity is the dormant fact shape for the Share catalog
// placeholder. Share is enforced by its domain Access service, not policy; this
// schema exists only so schema coverage stays total for every catalog member.
func ownerAgentSensitivity() map[string]attrSpec {
	return map[string]attrSpec{"owner": stringAttr, "agent": stringAttr, "sensitivity": stringAttr}
}

func kindStatus() map[string]attrSpec {
	return map[string]attrSpec{"kind": stringAttr, "status": stringAttr}
}

func kindOwnerStatus() map[string]attrSpec {
	return map[string]attrSpec{"kind": stringAttr, "owner": stringAttr, "status": stringAttr}
}

func ownerStatus() map[string]attrSpec {
	return map[string]attrSpec{"owner": stringAttr, "status": stringAttr}
}

// SchemaFor returns the custom-policy attribute schema for a resource type and
// whether one exists. Every catalog member has a schema.
func schemaFor(rt authz.ResourceType) (resourceSchema, bool) {
	s, ok := schemas[rt]
	return s, ok
}

// ResourceBuilder assembles a validated authz.Resource with typed attributes.
// It is the ONLY sanctioned way for a domain to attach custom-policy attributes
// to a resource: every WithString/WithBool/WithEnum call is checked against the
// resource's schema, so an unknown attribute, wrong type, or bad enum value is
// rejected at build time. Transport code never sees this — it produces typed
// domain values, and the builder turns them into the internal string form.
type ResourceBuilder struct {
	typ     authz.ResourceType
	id      string
	ownerID string
	attrs   map[string]string
	err     error
}

// NewResourceBuilder starts a builder for a resource type/id/owner.
func NewResourceBuilder(rt authz.ResourceType, id, ownerID string) *ResourceBuilder {
	b := &ResourceBuilder{typ: rt, id: id, ownerID: ownerID, attrs: map[string]string{}}
	if !rt.Valid() {
		b.err = authz.ErrInvalidResource
		return b
	}
	if _, ok := schemaFor(rt); !ok {
		b.err = fmt.Errorf("%w: no schema for resource %s", ErrSchema, rt)
	}
	return b
}

func (b *ResourceBuilder) set(name, value string, want attrKind) *ResourceBuilder {
	if b.err != nil {
		return b
	}
	s, _ := schemaFor(b.typ)
	spec, ok := s.attrs[name]
	if !ok {
		b.err = fmt.Errorf("%w: resource %s has no attribute %q", ErrSchema, b.typ, name)
		return b
	}
	if spec.kind != want {
		b.err = fmt.Errorf("%w: attribute %q on %s is not of the expected kind", ErrSchema, name, b.typ)
		return b
	}
	if !spec.validValue(value) {
		b.err = fmt.Errorf("%w: value %q is not valid for attribute %q on %s", ErrSchema, value, name, b.typ)
		return b
	}
	b.attrs[name] = value
	return b
}

// WithString sets a string attribute.
func (b *ResourceBuilder) WithString(name, value string) *ResourceBuilder {
	return b.set(name, value, kindString)
}

// WithEnum sets an enum attribute, validated against the allowed members.
func (b *ResourceBuilder) WithEnum(name, value string) *ResourceBuilder {
	return b.set(name, value, kindEnum)
}

// WithBool sets a boolean attribute.
func (b *ResourceBuilder) WithBool(name string, value bool) *ResourceBuilder {
	v := "false"
	if value {
		v = "true"
	}
	return b.set(name, v, kindBool)
}

// Build returns the validated resource, or the first error encountered.
func (b *ResourceBuilder) Build() (authz.Resource, error) {
	if b.err != nil {
		return authz.Resource{}, b.err
	}
	return authz.NewResourceWithAttrs(b.typ, b.id, b.ownerID, b.attrs)
}

// AgentFacts is the complete canonical set of per-agent policy facts. Every
// agent request uses it so an accepted custom-policy predicate can never become
// a silently missing attribute.
type AgentFacts struct {
	Scope     string
	Assigned  bool
	Creator   string
	IsCreator bool
	// IsExecutor is true only when an AgentActor or GroupAgentActor names
	// this exact agent. It is never inferred from a role or assignment.
	IsExecutor bool
	Dedicated  bool
	Status     string
}

// AgentResource builds an Agent resource carrying every accepted schema fact.
func AgentResource(id, ownerID string, facts AgentFacts) (authz.Resource, error) {
	return NewResourceBuilder(authz.ResourceAgent, id, ownerID).
		WithEnum("scope", facts.Scope).
		WithBool("assigned", facts.Assigned).
		WithString("creator", facts.Creator).
		WithBool("is_creator", facts.IsCreator).
		WithBool("is_executor", facts.IsExecutor).
		WithBool("dedicated", facts.Dedicated).
		WithEnum("status", facts.Status).
		Build()
}

// SessionFacts is the complete durable fact set for both Session and Workspace
// policy resources. The three boolean facts are derived by the PEP from the
// Authority and loaded conversation; they are never caller-controlled.
type SessionFacts struct {
	Owner       string
	Agent       string
	Kind        string
	State       string
	IsOwner     bool
	IsExecutor  bool
	IsGroup     bool
	IsSameGroup bool
}

// SessionResource builds a Session resource with every accepted durable fact.
func SessionResource(id, ownerID string, facts SessionFacts) (authz.Resource, error) {
	return ownerAgentKindStateResource(authz.ResourceSession, id, ownerID, facts)
}

// WorkspaceResource builds a Workspace resource with the same session-derived
// facts. A workspace has no independent owner or executor.
func WorkspaceResource(id, ownerID string, facts SessionFacts) (authz.Resource, error) {
	return ownerAgentKindStateResource(authz.ResourceWorkspace, id, ownerID, facts)
}

func ownerAgentKindStateResource(rt authz.ResourceType, id, ownerID string, facts SessionFacts) (authz.Resource, error) {
	return NewResourceBuilder(rt, id, ownerID).
		WithString("owner", facts.Owner).
		WithString("agent", facts.Agent).
		WithString("kind", facts.Kind).
		WithString("state", facts.State).
		WithBool("is_owner", facts.IsOwner).
		WithBool("is_executor", facts.IsExecutor).
		WithBool("is_group", facts.IsGroup).
		WithBool("is_same_group", facts.IsSameGroup).
		Build()
}

// GoalFacts is the complete durable fact set for a Goal policy resource.
// IsOwner/IsExecutor are derived by the PEP from the Authority and durable state:
// interactive use compares the loaded goal binding, while a dequeue use compares
// the persisted attempt executor. They are never caller-controlled.
type GoalFacts struct {
	Owner      string
	Agent      string
	State      string
	IsOwner    bool
	IsExecutor bool
}

// GoalResource builds a Goal resource carrying every accepted durable fact.
func GoalResource(id, ownerID string, facts GoalFacts) (authz.Resource, error) {
	return NewResourceBuilder(authz.ResourceGoal, id, ownerID).
		WithString("owner", facts.Owner).
		WithString("agent", facts.Agent).
		WithString("state", facts.State).
		WithBool("is_owner", facts.IsOwner).
		WithBool("is_executor", facts.IsExecutor).
		Build()
}

// WorkflowFacts is the complete durable fact set for a Workflow policy resource.
// IsOwner/IsExecutor are derived by the PEP from the Authority and the loaded
// workflow row; they are never caller-controlled.
type WorkflowFacts struct {
	Owner      string
	Agent      string
	State      string
	IsOwner    bool
	IsExecutor bool
}

// WorkflowResource builds a Workflow resource with every accepted durable fact.
func WorkflowResource(id, ownerID string, facts WorkflowFacts) (authz.Resource, error) {
	return NewResourceBuilder(authz.ResourceWorkflow, id, ownerID).
		WithString("owner", facts.Owner).
		WithString("agent", facts.Agent).
		WithString("state", facts.State).
		WithBool("is_owner", facts.IsOwner).
		WithBool("is_executor", facts.IsExecutor).
		Build()
}

// SchedulerFacts is the complete durable fact set for a Scheduler job policy
// resource. IsOwner/IsExecutor are derived by the PEP from the Authority and the
// loaded job row; they are never caller-controlled.
type SchedulerFacts struct {
	Owner      string
	Agent      string
	Kind       string
	State      string
	IsOwner    bool
	IsExecutor bool
}

// SchedulerResource builds a Scheduler resource carrying every accepted fact.
func SchedulerResource(id, ownerID string, facts SchedulerFacts) (authz.Resource, error) {
	return NewResourceBuilder(authz.ResourceScheduler, id, ownerID).
		WithString("owner", facts.Owner).
		WithString("agent", facts.Agent).
		WithString("kind", facts.Kind).
		WithString("state", facts.State).
		WithBool("is_owner", facts.IsOwner).
		WithBool("is_executor", facts.IsExecutor).
		Build()
}

// SkillFacts is the complete durable fact set for a Skill policy resource. The
// four scopes are the durable DB buckets (system, system_agent, user,
// user_agent). IsOwner is derived by the PEP from the Authority and the loaded
// skill row (or the write target's owner columns); it is never caller-supplied.
type SkillFacts struct {
	Scope   string
	Owner   string
	Agent   string
	Source  string
	IsOwner bool
}

// SkillResource builds a Skill resource carrying every accepted durable fact.
func SkillResource(id, ownerID string, facts SkillFacts) (authz.Resource, error) {
	return NewResourceBuilder(authz.ResourceSkill, id, ownerID).
		WithEnum("scope", facts.Scope).
		WithString("owner", facts.Owner).
		WithString("agent", facts.Agent).
		WithString("source", facts.Source).
		WithBool("is_owner", facts.IsOwner).
		Build()
}

// VaultFacts is the complete durable fact set for a Vault entry policy resource.
// Scope is the durable bucket; IsOwner is derived by the vault PEP from the
// Authority and the entry's owner/agent columns (and, for an agent-scoped actor,
// the exact bound agent). It is never caller-supplied.
type VaultFacts struct {
	Scope   string
	Owner   string
	Agent   string
	IsOwner bool
}

// VaultResource builds a Vault resource carrying every accepted durable fact.
func VaultResource(id, ownerID string, facts VaultFacts) (authz.Resource, error) {
	return NewResourceBuilder(authz.ResourceVault, id, ownerID).
		WithEnum("scope", facts.Scope).
		WithString("owner", facts.Owner).
		WithString("agent", facts.Agent).
		WithBool("is_owner", facts.IsOwner).
		Build()
}

// predicate is one attribute comparison inside a custom policy.
type predicate struct {
	Attr  string   `json:"attr"`
	Op    operator `json:"op"`
	Value string   `json:"value,omitempty"`
	// Values holds the members for in/not_in operators.
	Values []string `json:"values,omitempty"`
}

// attributeDoc is the JSON shape stored in authz_policy.attributes.
type attributeDoc struct {
	Predicates []predicate `json:"predicates"`
}

// validatePredicates checks a predicate set against a resource's schema.
// Unknown attribute, disallowed operator, or invalid value fails closed.
func validatePredicates(rt authz.ResourceType, preds []predicate) error {
	s, ok := schemaFor(rt)
	if !ok {
		return fmt.Errorf("%w: no schema for resource %s", ErrSchema, rt)
	}
	for _, p := range preds {
		spec, ok := s.attrs[p.Attr]
		if !ok {
			return fmt.Errorf("%w: unknown attribute %q for %s", ErrSchema, p.Attr, rt)
		}
		if !spec.allowsOp(p.Op) {
			return fmt.Errorf("%w: operator %q not allowed on %q of %s", ErrSchema, p.Op, p.Attr, rt)
		}
		switch p.Op {
		case opEq, opNeq:
			if !spec.validValue(p.Value) {
				return fmt.Errorf("%w: value %q invalid for %q of %s", ErrSchema, p.Value, p.Attr, rt)
			}
		case opIn, opNotIn:
			if len(p.Values) == 0 {
				return fmt.Errorf("%w: operator %q on %q needs values", ErrSchema, p.Op, p.Attr)
			}
			for _, v := range p.Values {
				if !spec.validValue(v) {
					return fmt.Errorf("%w: value %q invalid for %q of %s", ErrSchema, v, p.Attr, rt)
				}
			}
		default:
			return fmt.Errorf("%w: unknown operator %q", ErrSchema, p.Op)
		}
	}
	return nil
}

// marshalAttributes encodes a predicate set for storage.
func marshalAttributes(preds []predicate) (json.RawMessage, error) {
	if len(preds) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return json.Marshal(attributeDoc{Predicates: preds})
}

// unmarshalAttributes decodes a stored predicate set.
func unmarshalAttributes(raw json.RawMessage) ([]predicate, error) {
	if len(raw) == 0 || string(raw) == "{}" || string(raw) == "null" {
		return nil, nil
	}
	var doc attributeDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("authz/policy: decode attributes: %w", err)
	}
	return doc.Predicates, nil
}
