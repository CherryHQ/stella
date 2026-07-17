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
)

type Tool struct {
	svc *Service
}

func NewTool(svc *Service) *Tool { return &Tool{svc: svc} }

func (t *Tool) Definition() tools.Definition {
	return tools.Definition{
		Name:        ToolName,
		Description: "Manage durable background goals for work that must survive turns, decompose into accepted subwork, or be cancelled later. Actions: create a goal, list goals, get status, cancel. Use scheduler for recurring or future timed prompts, not goal.",
		InputSchema: InputSchema(),
	}
}

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.svc == nil {
		return "", fmt.Errorf("goal service is unavailable — try again later")
	}
	ident, err := authz.ToolIdentity(ctx, "goal")
	if err != nil {
		return "", err
	}
	// The runtime context identity is the trusted adapter: a delegated agent turn
	// becomes a confined AgentActor. Model-supplied arguments never form identity.
	authority, err := ident.ToAuthority()
	if err != nil {
		return "", authz.MapError("goal", err)
	}
	action, err := tools.ActionArg(args, "goal")
	if err != nil {
		return "", err
	}
	out, err := Dispatch(ctx, goalHandler{svc: t.svc, authority: authority, agentID: ident.AgentID}, action, args)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", fmt.Errorf("goal not found — check the id with action=list")
		}
		return "", authz.MapError("goal", err)
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
	return goalSummary(row), nil
}

func (h goalHandler) List(ctx context.Context, in ListInput) (any, error) {
	limit, offset, err := tools.ParsePage(in.PageSize, in.PageToken, defaultToolPageSize, maxToolPageSize)
	if err != nil {
		return nil, fmt.Errorf("invalid pagination — use page_size between 1 and %d and pass next_page_token unchanged", maxToolPageSize)
	}
	filter := GoalFilter{Lifecycle: in.Lifecycle, ProjectID: in.ProjectId, Terminal: in.Terminal, Q: in.Q}
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
	ID              string `json:"id"`
	Title           string `json:"title"`
	Lifecycle       string `json:"lifecycle"`
	AcceptanceState string `json:"acceptance_state"`
	Kind            string `json:"kind"`
	Priority        string `json:"priority"`
	UpdatedAt       string `json:"updated_at"`
}
type listResponse[T any] struct {
	Items         []T    `json:"items"`
	HasMore       bool   `json:"has_more"`
	NextPageToken string `json:"next_page_token,omitempty"`
}

func goalSummary(row Goal) goalResponse {
	return goalResponse{ID: row.ID, Title: row.Title, Lifecycle: row.Lifecycle, AcceptanceState: row.AcceptanceState, Kind: row.Kind, Priority: row.Priority, UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339)}
}
