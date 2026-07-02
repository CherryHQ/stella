package goal

import "strings"

// Default convergence bounds (contract §3.2).
const (
	defaultMaxAttempts = 3
	defaultMaxDepth    = 4
	// defaultMaxConcurrent bounds breadth-of-fanout per root (§5).
	defaultMaxConcurrent = 8
	// defaultMaxReviewAttempts bounds how many agent auto-review attempts are spent
	// per needs_verdict episode — reviews of ONE execution output — before
	// degrading to a human verdict (contract §10.13). A reviewer that cannot
	// produce a verdict after this many tries against the same output leaves the
	// goal blocked(needs_verdict) for a human. A rework that yields a new execution
	// output starts a fresh episode with full budget (see CountRanReviewAttemptsForOutput).
	defaultMaxReviewAttempts = 2
	// defaultPlannerRepairMax bounds structural decomposition repair turns inside
	// the same planning session before the composite blocks as planning_invalid.
	defaultPlannerRepairMax = 2
)

// AcceptanceContract is the composite policy tree of deterministic + judgment
// items that gates a goal's acceptance (contract §3.1). An empty
// contract (no items) is the trivial auto-accept degradation — a "direct goal"
// with no bar. Marshaled to the acceptance_contract TEXT column.
type AcceptanceContract struct {
	Policy string           `json:"policy"` // deterministic_then_judgment | all | any
	Items  []AcceptanceItem `json:"items"`
}

// AcceptanceItem is one check in the contract. Deterministic items carry a
// Command; judgment items carry an Authority + Rubric/Prompt. A non-required
// item is advisory and does not gate acceptance.
type AcceptanceItem struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"` // deterministic | judgment
	Required bool   `json:"required"`
	// deterministic:
	Command    string `json:"command,omitempty"`
	ExpectExit *int   `json:"expect_exit,omitempty"` // default 0
	// judgment:
	Authority string `json:"authority,omitempty"` // agent | human
	Rubric    string `json:"rubric,omitempty"`    // agent reviewer prompt
	Prompt    string `json:"prompt,omitempty"`    // human verdict prompt
}

// ConvergencePolicy bounds the rework loop and the recursion depth (§3.2).
type ConvergencePolicy struct {
	MaxAttempts int    `json:"max_attempts"`        // default 3; exhaustion → blocked(budget_exhausted)
	Escalation  string `json:"escalation"`          // block (default) | abandon
	MaxDepth    int    `json:"max_depth,omitempty"` // recursion ceiling; default 4
	// PlannerRepairMax bounds structural decomposition repair turns inside one
	// planning session; these do not consume MaxAttempts plan budget.
	PlannerRepairMax int `json:"planner_repair_max,omitempty"`
	// MaxConcurrent bounds breadth-of-fanout per root (§5, default 8). Read from
	// the root's policy by the dispatcher; 0 ⇒ default.
	MaxConcurrent int `json:"max_concurrent,omitempty"`
}

// IsTrivial reports whether the contract is the empty auto-accept degradation
// (no items). A trivial leaf accepts immediately; a trivial composite accepts
// when all required children are accepted.
func (c AcceptanceContract) IsTrivial() bool { return len(c.Items) == 0 }

// HasDeterministicItem reports whether the contract carries any deterministic
// (command-checked) item. A deterministic item needs an executed output to run
// its check against; a composite produces no output, so its fold would stall
// pending forever (no event source). Callers reject deterministic items on
// composites — see ErrCompositeDeterministicContract.
func (c AcceptanceContract) HasDeterministicItem() bool {
	for _, it := range c.Items {
		if it.Kind == ItemDeterministic {
			return true
		}
	}
	return false
}

// HasRequiredDeterministicItem reports whether acceptance depends on a command
// check that must run in a sandbox.
func (c AcceptanceContract) HasRequiredDeterministicItem() bool {
	for _, it := range c.Items {
		if it.Kind == ItemDeterministic && it.Required {
			return true
		}
	}
	return false
}

// AgentJudgmentItems returns the required judgment items resolved by a reviewer
// agent (authority=agent) — the items the agent auto-review producer must answer
// with a verdict (contract §10.13). A non-required or human-authority judgment
// item is excluded; the former is advisory, the latter awaits a human verdict.
func (c AcceptanceContract) AgentJudgmentItems() []AcceptanceItem {
	var out []AcceptanceItem
	for _, it := range c.Items {
		if it.Required && it.Kind == ItemJudgment && it.Authority == AuthorityAgent {
			out = append(out, it)
		}
	}
	return out
}

// expectExit returns the item's expected exit code (default 0).
func (i AcceptanceItem) expectExit() int {
	if i.ExpectExit != nil {
		return *i.ExpectExit
	}
	return 0
}

// Valid reports whether the contract's policy and every item are well-formed.
// An empty contract is valid (trivial auto-accept).
func (c AcceptanceContract) Valid() bool {
	if c.IsTrivial() {
		return c.Policy == "" || ValidContractPolicy(c.Policy)
	}
	if !ValidContractPolicy(c.Policy) {
		return false
	}
	seen := make(map[string]struct{}, len(c.Items))
	for _, it := range c.Items {
		if !it.Valid() {
			return false
		}
		if _, dup := seen[it.ID]; dup {
			return false // item ids must be unique within a contract
		}
		seen[it.ID] = struct{}{}
	}
	return true
}

// Valid reports whether a single item is structurally sound: a deterministic
// item needs a command; a judgment item needs a known authority.
func (i AcceptanceItem) Valid() bool {
	if i.ID == "" || !ValidItemKind(i.Kind) {
		return false
	}
	switch i.Kind {
	case ItemDeterministic:
		return strings.TrimSpace(i.Command) != ""
	case ItemJudgment:
		return i.Authority == AuthorityAgent || i.Authority == AuthorityHuman
	}
	return false
}

// Valid reports whether the convergence policy is well-formed. A zero-value
// policy is valid; Normalized fills the defaults.
func (p ConvergencePolicy) Valid() bool {
	if p.MaxAttempts < 0 || p.MaxDepth < 0 || p.PlannerRepairMax < 0 || p.MaxConcurrent < 0 {
		return false
	}
	return p.Escalation == "" || ValidEscalation(p.Escalation)
}

// Normalized returns the policy with defaults applied (MaxAttempts 3,
// Escalation block, MaxDepth 4, PlannerRepairMax 2, MaxConcurrent 8). It never
// mutates the receiver.
func (p ConvergencePolicy) Normalized() ConvergencePolicy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = defaultMaxAttempts
	}
	if p.Escalation == "" {
		p.Escalation = EscalationBlock
	}
	if p.MaxDepth <= 0 {
		p.MaxDepth = defaultMaxDepth
	}
	if p.PlannerRepairMax <= 0 {
		p.PlannerRepairMax = defaultPlannerRepairMax
	}
	if p.MaxConcurrent <= 0 {
		p.MaxConcurrent = defaultMaxConcurrent
	}
	return p
}

// Criterion is one natural-language acceptance criterion handed to the splitter.
// Operationalizable carries a machine-checkable command; everything else is
// routed to a judgment item.
type Criterion struct {
	ID   string // stable item id
	Text string // the NL criterion
	// Command, when non-empty, marks the criterion operationalizable: it becomes
	// a deterministic item bound to this command. Empty ⇒ judgment.
	Command string
	// Authority routes a judgment criterion (agent | human). Defaults to agent
	// when a judgment criterion leaves it blank.
	Authority string
	// Required marks whether the criterion gates acceptance (default true).
	Required bool
}

// SplitCriteria turns natural-language criteria into a deterministic|judgment
// item set under the deterministic_then_judgment policy (contract §3.1). A
// criterion with a Command becomes a binding deterministic item; one without
// becomes a judgment item (agent rubric or human prompt). The model never
// pretends prose is executable — the caller (planner) decides the split by
// supplying or omitting Command.
func SplitCriteria(crits []Criterion) AcceptanceContract {
	c := AcceptanceContract{Policy: PolicyDetThenJudgment}
	for _, cr := range crits {
		item := AcceptanceItem{ID: cr.ID, Required: cr.Required}
		if strings.TrimSpace(cr.Command) != "" {
			item.Kind = ItemDeterministic
			item.Command = cr.Command
		} else {
			item.Kind = ItemJudgment
			item.Authority = cr.Authority
			if item.Authority == "" {
				item.Authority = AuthorityAgent
			}
			if item.Authority == AuthorityHuman {
				item.Prompt = cr.Text
			} else {
				item.Rubric = cr.Text
			}
		}
		c.Items = append(c.Items, item)
	}
	return c
}
