package goal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/tools"
)

const (
	defaultToolPageSize = 20
	maxToolPageSize     = 100
	maxToolAttempts     = 20
	maxToolErrorText    = 4_000
	maxToolIntentText   = 12_000
	maxToolChildTitle   = 1_000
	maxToolChildText    = 20_000
	maxToolAttemptText  = 32_000
	maxToolDetailText   = maxToolIntentText + maxToolChildText + maxToolAttemptText
)

// ListTool is the goal action that lists what this agent can reach. Error
// prose points at it, so a rename shows up here rather than in a string.
const ListTool = "goal_list"

// actionDescriptions is the model-facing description per generated tool. A
// split tool's schema is exact, so each description only says what the call
// does and what it costs; it no longer has to disambiguate sibling actions.
var actionDescriptions = map[string]string{
	"create": "Start a durable background goal for work that must survive this turn or decompose into accepted subwork. The goal runs asynchronously; poll goal_get for its state. Use scheduler_job_create for recurring or future timed prompts instead.",
	"list":   "List this user's goals, newest first, filtered by lifecycle, project, workflow, parent, or free text. Returns summaries only; call goal_get for a goal's intent, children, and attempts.",
	"get":    "Read one goal by id: its intent, acceptance state, children with their progress, and recent attempts. Long text fields are truncated for token safety.",
	"cancel": "Cancel one goal by id, stopping its active attempt and marking it done(cancelled). This is not reversible; the goal cannot be restarted.",
}

// Tool is one generated goal action. The tool name carries the action, so the
// provider validates arguments against an exact schema before dispatch.
type Tool struct {
	spec ActionTool
	svc  *Service
}

// NewTool builds one goal action tool.
func NewTool(svc *Service, spec ActionTool) *Tool { return &Tool{spec: spec, svc: svc} }

func (t *Tool) Definition() tools.Definition {
	return t.spec.Definition(actionDescriptions[t.spec.Action])
}

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.svc == nil {
		return "", fmt.Errorf("goal service is unavailable — try again later")
	}
	ident, err := authz.ToolIdentity(ctx, t.spec.Name)
	if err != nil {
		return "", err
	}
	// The runtime context identity is the trusted adapter: a delegated agent turn
	// becomes a confined AgentActor. Model-supplied arguments never form identity.
	authority, err := ident.ToAuthority()
	if err != nil {
		return "", authz.MapToolError(t.spec.Name, ListTool, err)
	}
	out, err := Dispatch(ctx, goalHandler{svc: t.svc, authority: authority, agentID: ident.AgentID}, t.spec.Action, args)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", fmt.Errorf("%s: goal not found — check the id with %s", t.spec.Name, ListTool)
		}
		return "", authz.MapToolError(t.spec.Name, ListTool, err)
	}
	return tools.MarshalResult(out)
}

type goalHandler struct {
	svc       *Service
	authority authz.Authority
	agentID   string
}

func (h goalHandler) begin(ctx context.Context) (*Access, error) {
	return h.svc.Begin(ctx, h.authority)
}

func (h goalHandler) Create(ctx context.Context, in ToolCreateInput) (any, error) {
	create := CreateInput{AgentID: h.agentID, Title: in.Title, Intent: in.Intent, Kind: KindComposite}
	if in.ProjectId != "" {
		create.ProjectID = in.ProjectId
	}
	if in.Priority != "" {
		create.Priority = in.Priority
	}
	if in.ReviewPolicy != "" {
		create.ReviewPolicy = in.ReviewPolicy
	}
	if len(in.AcceptanceContract) > 0 {
		if err := tools.DecodeMap(in.AcceptanceContract, &create.Contract); err != nil {
			return nil, fmt.Errorf("acceptance_contract is invalid — fix the JSON object and retry")
		}
	}
	if len(in.ConvergencePolicy) > 0 {
		if err := tools.DecodeMap(in.ConvergencePolicy, &create.Convergence); err != nil {
			return nil, fmt.Errorf("convergence_policy is invalid — fix the JSON object and retry")
		}
	}
	create.IdempotencyKey = in.IdempotencyKey
	acc, err := h.begin(ctx)
	if err != nil {
		return nil, err
	}
	row, err := acc.CreateGoal(ctx, create)
	if err != nil {
		return nil, err
	}
	return goalSummary(row), nil
}

func (h goalHandler) Get(ctx context.Context, in GetInput) (any, error) {
	acc, err := h.begin(ctx)
	if err != nil {
		return nil, err
	}
	row, err := acc.Get(ctx, in.Id)
	if err != nil {
		return nil, err
	}
	children, err := acc.ListChildren(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	attempts, err := acc.ListAttemptSummaries(ctx, row.ID, maxToolAttempts+1)
	if err != nil {
		return nil, err
	}
	return goalDetail(row, children, attempts), nil
}

func (h goalHandler) List(ctx context.Context, in ListInput) (any, error) {
	limit, offset, err := tools.ParsePage(in.PageSize, in.PageToken, defaultToolPageSize, maxToolPageSize)
	if err != nil {
		return nil, fmt.Errorf("invalid pagination — use page_size between 1 and %d and pass next_page_token unchanged", maxToolPageSize)
	}
	filter := GoalFilter{Lifecycle: in.Lifecycle, ProjectID: in.ProjectId, WorkflowID: in.WorkflowId, Terminal: in.Terminal, Q: in.Q}
	if in.Archived != nil {
		filter.Archived = *in.Archived
	}
	acc, err := h.begin(ctx)
	if err != nil {
		return nil, err
	}
	var page []Goal
	var next string
	switch {
	case in.Parent != "":
		rows, lerr := acc.ListChildren(ctx, in.Parent)
		if lerr != nil {
			return nil, lerr
		}
		page, next = tools.PageRows(rows, limit, offset)
	case in.Root != "":
		rows, lerr := acc.ListSubtree(ctx, in.Root)
		if lerr != nil {
			return nil, lerr
		}
		page, next = tools.PageRows(rows, limit, offset)
	default:
		// Access.ListGoals owns paging: it scans candidates past denied rows and
		// returns the resume offset, so the token still round-trips as an offset.
		rows, nextOffset, hasMore, lerr := acc.ListGoals(ctx, filter, int64(limit), int64(offset))
		if lerr != nil {
			return nil, lerr
		}
		page = rows
		if hasMore {
			next = tools.OffsetToken(int(nextOffset))
		}
	}
	items := make([]goalResponse, 0, len(page))
	for _, row := range page {
		items = append(items, goalSummary(row))
	}
	return listResponse[goalResponse]{Items: items, HasMore: next != "", NextPageToken: next}, nil
}

func (h goalHandler) Cancel(ctx context.Context, in CancelInput) (any, error) {
	acc, err := h.begin(ctx)
	if err != nil {
		return nil, err
	}
	if err := acc.Cancel(ctx, in.Id, in.Reason); err != nil {
		return nil, err
	}
	row, err := acc.Get(ctx, in.Id)
	if err != nil {
		return nil, err
	}
	return goalSummary(row), nil
}

type goalResponse struct {
	ID              string  `json:"id"`
	Title           string  `json:"title"`
	TitleTruncated  bool    `json:"title_truncated,omitempty"`
	Lifecycle       string  `json:"lifecycle"`
	AcceptanceState string  `json:"acceptance_state"`
	Kind            string  `json:"kind"`
	Priority        string  `json:"priority"`
	BlockReason     string  `json:"block_reason,omitempty"`
	DoneReason      string  `json:"done_reason,omitempty"`
	ProjectID       *string `json:"project_id,omitempty"`
	ParentID        *string `json:"parent_id,omitempty"`
	RootID          string  `json:"root_id"`
	AttemptCount    int64   `json:"attempt_count"`
	ActiveAttemptID *string `json:"active_attempt_id,omitempty"`
	NeedsAttention  bool    `json:"needs_attention"`
	UpdatedAt       string  `json:"updated_at"`
}

type goalDetailResponse struct {
	goalResponse
	Intent          string                `json:"intent"`
	IntentTruncated bool                  `json:"intent_truncated,omitempty"`
	ReviewPolicy    string                `json:"review_policy"`
	Children        []goalResponse        `json:"children"`
	ChildProgress   goalChildProgress     `json:"child_progress"`
	Attempts        []goalAttemptResponse `json:"attempts"`
	AttemptsHasMore bool                  `json:"attempts_has_more"`
}

type goalChildProgress struct {
	Total     int `json:"total"`
	Accepted  int `json:"accepted"`
	Active    int `json:"active"`
	Blocked   int `json:"blocked"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
}

type goalAttemptResponse struct {
	ID             string  `json:"id"`
	Purpose        string  `json:"purpose"`
	AttemptNo      int64   `json:"attempt_no"`
	Status         string  `json:"status"`
	SessionID      string  `json:"session_id"`
	Error          string  `json:"error,omitempty"`
	ErrorTruncated bool    `json:"error_truncated,omitempty"`
	FailureClass   string  `json:"failure_class,omitempty"`
	StartedAt      *string `json:"started_at,omitempty"`
	FinishedAt     *string `json:"finished_at,omitempty"`
	UpdatedAt      string  `json:"updated_at"`
}
type listResponse[T any] struct {
	Items         []T    `json:"items"`
	HasMore       bool   `json:"has_more"`
	NextPageToken string `json:"next_page_token,omitempty"`
}

func goalSummary(row Goal) goalResponse {
	return goalResponse{
		ID: row.ID, Title: row.Title, Lifecycle: row.Lifecycle, AcceptanceState: row.AcceptanceState,
		Kind: row.Kind, Priority: row.Priority, BlockReason: row.BlockReason, DoneReason: row.DoneReason,
		ProjectID: row.ProjectID, ParentID: row.ParentID, RootID: row.RootID, AttemptCount: row.AttemptCount,
		ActiveAttemptID: row.ActiveAttemptID, NeedsAttention: NeedsAttention(row.Lifecycle, row.BlockReason),
		UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func goalDetail(row Goal, children []Goal, attempts []AttemptSummary) goalDetailResponse {
	intentBudget := maxToolIntentText
	intent, intentTruncated := truncateGoalToolText(row.Intent, maxToolIntentText, &intentBudget)
	response := goalDetailResponse{
		goalResponse:    goalSummary(row),
		Intent:          intent,
		IntentTruncated: intentTruncated,
		ReviewPolicy:    row.ReviewPolicy,
		Children:        make([]goalResponse, 0, len(children)),
		Attempts:        make([]goalAttemptResponse, 0, len(attempts)),
	}
	childTextBudget := maxToolChildText
	for i, child := range children {
		summary := goalSummary(child)
		summary.Title, summary.TitleTruncated = truncateGoalToolTextFair(summary.Title, maxToolChildTitle, len(children)-i, &childTextBudget)
		response.Children = append(response.Children, summary)
		response.ChildProgress.Total++
		switch {
		case child.Lifecycle == LifecycleDone && child.DoneReason == DoneReasonAccepted:
			response.ChildProgress.Accepted++
		case child.Lifecycle == LifecycleDone && child.DoneReason == DoneReasonFailed:
			response.ChildProgress.Failed++
		case child.Lifecycle == LifecycleDone && child.DoneReason == DoneReasonCancelled:
			response.ChildProgress.Cancelled++
		case child.Lifecycle == LifecycleBlocked:
			response.ChildProgress.Blocked++
		case !IsTerminalLifecycle(child.Lifecycle):
			response.ChildProgress.Active++
		}
	}
	if len(attempts) > maxToolAttempts {
		response.AttemptsHasMore = true
		attempts = attempts[:maxToolAttempts]
	}
	attemptTextBudget := maxToolAttemptText
	for i, attempt := range attempts {
		response.Attempts = append(response.Attempts, goalAttemptSummary(attempt, len(attempts)-i, &attemptTextBudget))
	}
	return response
}

func goalAttemptSummary(attempt AttemptSummary, remainingItems int, remainingText *int) goalAttemptResponse {
	errorText, errorTruncated := truncateGoalToolTextFair(attempt.Error, maxToolErrorText, remainingItems, remainingText)
	return goalAttemptResponse{
		ID: attempt.ID, Purpose: attempt.Purpose, AttemptNo: attempt.AttemptNo, Status: attempt.Status,
		SessionID: attempt.SessionID, Error: errorText, ErrorTruncated: errorTruncated, FailureClass: attempt.FailureClass,
		StartedAt: formatToolTime(attempt.StartedAt), FinishedAt: formatToolTime(attempt.FinishedAt),
		UpdatedAt: attempt.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func truncateGoalToolTextFair(text string, perFieldLimit, remainingItems int, remaining *int) (string, bool) {
	if remainingItems > 0 {
		perFieldLimit = min(perFieldLimit, *remaining/remainingItems)
	}
	return truncateGoalToolText(text, perFieldLimit, remaining)
}

func truncateGoalToolText(text string, perFieldLimit int, remaining *int) (string, bool) {
	limit := min(perFieldLimit, *remaining)
	if limit <= 0 {
		return "", text != ""
	}
	value, truncated := tools.TruncateText(text, limit)
	*remaining -= len(value)
	return value, truncated
}

func formatToolTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339)
	return &formatted
}
