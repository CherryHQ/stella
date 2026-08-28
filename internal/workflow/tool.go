package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/tools"
)

// ListTool is the workflow action that lists what this agent can reach. Error
// prose points at it, so a rename shows up here rather than in a string.
const ListTool = "workflow_list"

// actionDescriptions is the model-facing description per generated tool. A
// split tool's schema is exact, so each description only says what the call
// does and what it costs.
var actionDescriptions = map[string]string{
	"save": "Save an accepted composite goal as a reusable workflow, naming the placeholders its steps should take as inputs. The goal itself is unchanged; the workflow is a frozen copy of its plan.",
	"list": "List this user's saved workflows with their name, version, and inputs.",
	"get":  "Read one workflow by id: its inputs, its frozen plan, and the goal it was saved from.",
	"run":  "Instantiate one workflow with concrete input values, which creates a fresh goal tree that runs asynchronously. Pass idempotency_key so a retry does not start a second run.",
}

// Tool is one generated workflow action. The tool name carries the action, so
// the provider validates arguments against an exact schema before dispatch.
type Tool struct {
	spec ActionTool
	svc  *Service
}

// NewTool builds one workflow action tool.
func NewTool(svc *Service, spec ActionTool) *Tool { return &Tool{spec: spec, svc: svc} }

func (t *Tool) Definition() tools.Definition {
	return t.spec.Definition(actionDescriptions[t.spec.Action])
}

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.svc == nil {
		return "", fmt.Errorf("workflow service is unavailable — try again later")
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
	out, err := Dispatch(ctx, workflowHandler{svc: t.svc, authority: authority}, t.spec.Action, args)
	if err != nil {
		return "", authz.MapToolError(t.spec.Name, ListTool, err)
	}
	return tools.MarshalResult(out)
}

type workflowHandler struct {
	svc       *Service
	authority authz.Authority
}

func (h workflowHandler) begin(ctx context.Context) (*Access, error) {
	return h.svc.Begin(ctx, h.authority)
}

func (h workflowHandler) Get(ctx context.Context, in GetInput) (any, error) {
	acc, err := h.begin(ctx)
	if err != nil {
		return nil, err
	}
	row, err := acc.Get(ctx, in.Id)
	if err != nil {
		return nil, err
	}
	return workflowSummary(row), nil
}

func (h workflowHandler) List(ctx context.Context, _ ListInput) (any, error) {
	acc, err := h.begin(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := acc.List(ctx, "")
	if err != nil {
		return nil, err
	}
	items := make([]workflowResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, workflowSummary(row))
	}
	return listResponse[workflowResponse]{Items: items, HasMore: false}, nil
}

func (h workflowHandler) Run(ctx context.Context, in RunInput) (any, error) {
	inputs, err := stringMap(in.Inputs)
	if err != nil {
		return nil, err
	}
	key := in.IdempotencyKey
	if key == "" {
		key = uuid.NewString()
	}
	acc, err := h.begin(ctx)
	if err != nil {
		return nil, err
	}
	run, created, err := acc.Instantiate(ctx, in.Id, inputs, key)
	if err != nil {
		return nil, err
	}
	out := workflowRunSummary(run)
	out.Created = created
	return out, nil
}

func (h workflowHandler) Save(ctx context.Context, in ToolSaveInput) (any, error) {
	inputs, err := inputSpecs(in.Inputs)
	if err != nil {
		return nil, err
	}
	acc, err := h.begin(ctx)
	if err != nil {
		return nil, err
	}
	row, err := acc.SaveGoalAsWorkflow(ctx, SaveInput{GoalID: in.GoalId, Name: in.Name, Inputs: inputs})
	if err != nil {
		return nil, err
	}
	return workflowSummary(row), nil
}

type workflowResponse struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	Version      int32       `json:"version"`
	Intent       string      `json:"intent"`
	Inputs       []InputSpec `json:"inputs"`
	FullyFrozen  bool        `json:"fully_frozen"`
	SourceGoalID string      `json:"source_goal_id,omitempty"`
	UpdatedAt    string      `json:"updated_at"`
}

type workflowRunResponse struct {
	ID              string            `json:"id"`
	WorkflowID      string            `json:"workflow_id"`
	WorkflowVersion int32             `json:"workflow_version"`
	Status          string            `json:"status"`
	RootGoalID      string            `json:"root_goal_id,omitempty"`
	Inputs          map[string]string `json:"inputs"`
	Created         bool              `json:"created"`
	UpdatedAt       string            `json:"updated_at"`
}

type listResponse[T any] struct {
	Items         []T    `json:"items"`
	HasMore       bool   `json:"has_more"`
	NextPageToken string `json:"next_page_token,omitempty"`
}

func workflowSummary(row Workflow) workflowResponse {
	return workflowResponse{ID: row.ID, Name: row.Name, Version: row.Version, Intent: row.Intent, Inputs: row.Inputs, FullyFrozen: row.FullyFrozen, SourceGoalID: derefString(row.SourceGoalID), UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339)}
}

func workflowRunSummary(row Run) workflowRunResponse {
	return workflowRunResponse{ID: row.ID, WorkflowID: row.WorkflowID, WorkflowVersion: row.WorkflowVersion, Status: row.Status, RootGoalID: derefString(row.RootGoalID), Inputs: row.Inputs, UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339)}
}

func decodeInputSpecs(raw json.RawMessage) []InputSpec {
	var out []InputSpec
	_ = json.Unmarshal(raw, &out)
	return out
}

func inputSpecs(raw []any) ([]InputSpec, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]InputSpec, 0, len(raw))
	for _, item := range raw {
		var spec InputSpec
		if err := tools.DecodeMap(mapItem(item), &spec); err != nil {
			return nil, fmt.Errorf("inputs must be objects with name/description/required/default")
		}
		out = append(out, spec)
	}
	return out, nil
}

func stringMap(raw map[string]any) (map[string]string, error) {
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("inputs.%s must be a string", k)
		}
		out[k] = s
	}
	return out, nil
}

func mapItem(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}
