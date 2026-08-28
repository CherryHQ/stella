package connections

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/tools"
)

// ListTool is the oauth action that lists what this agent can reach. Error
// prose points at it, so a rename shows up here rather than in a string.
const ListTool = "oauth_list"

// actionDescriptions is the model-facing description per generated tool. A
// split tool's schema is exact, so each description only says what the call
// does and what it costs.
var actionDescriptions = map[string]string{
	"list":        "List the external OAuth providers this user can connect and which are already connected, with their granted scopes.",
	"connect":     "Start a device authorization flow for one provider, adding any requested scopes to this user's cumulative set; the consent screen decides what is granted. Give the user the returned verification_uri and user_code and never run commands for them, then poll oauth_flow_status with the flow_id.",
	"flow_status": "Poll one in-flight authorization by flow_id and provider until it reports connected or expired. Never returns tokens.",
	"disconnect":  "Revoke this user's stored credentials for one provider. Anything relying on that connection stops working until it is connected again.",
}

// Tool is one generated oauth action. The tool name carries the action, so the
// provider validates arguments against an exact schema before dispatch.
type Tool struct {
	spec ActionTool
	svc  *Service
}

// NewTool builds one oauth action tool.
func NewTool(svc *Service, spec ActionTool) *Tool { return &Tool{spec: spec, svc: svc} }

func (t *Tool) Definition() tools.Definition {
	return t.spec.Definition(actionDescriptions[t.spec.Action])
}

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.svc == nil {
		return "", fmt.Errorf("oauth service is unavailable — try again later")
	}
	ident, err := authz.ToolIdentity(ctx, t.spec.Name)
	if err != nil {
		return "", err
	}
	// The runtime context identity is the trusted adapter: a delegated agent turn
	// becomes a confined AgentActor. Model-supplied arguments never form identity.
	authority, err := ident.ToAuthority()
	if err != nil {
		return "", t.mapError(err)
	}
	out, err := Dispatch(ctx, oauthHandler{svc: t.svc, authority: authority}, t.spec.Action, args)
	if err != nil {
		return "", t.mapError(err)
	}
	return tools.MarshalResult(out)
}

// mapError keeps the flow-specific not-found wording: an expired device flow is
// not a missing resource the caller can look up, it is one they must restart.
func (t *Tool) mapError(err error) error {
	if errors.Is(err, authz.ErrNotFound) {
		return fmt.Errorf("%s: flow expired or unknown — start a new oauth_connect", t.spec.Name)
	}
	return authz.MapToolError(t.spec.Name, ListTool, err)
}

type oauthHandler struct {
	svc       *Service
	authority authz.Authority
}

func (h oauthHandler) access() (*Access, error) {
	return h.svc.Access(h.authority)
}

func (h oauthHandler) Connect(ctx context.Context, in ConnectInput) (any, error) {
	acc, err := h.access()
	if err != nil {
		return nil, err
	}
	status, err := acc.StartFlow(ctx, in.Provider, scopeItems(in.Scopes))
	if err != nil {
		return nil, err
	}
	return oauthFlowSummary(status), nil
}

func scopeItems(items []any) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if scope, ok := item.(string); ok {
			out = append(out, scope)
		}
	}
	return out
}

func (h oauthHandler) FlowStatus(ctx context.Context, in FlowStatusInput) (any, error) {
	acc, err := h.access()
	if err != nil {
		return nil, err
	}
	status, _, err := acc.PollFlow(ctx, in.Provider, in.FlowId)
	if err != nil {
		return nil, err
	}
	return oauthFlowSummary(status), nil
}

func (h oauthHandler) List(ctx context.Context, _ ListInput) (any, error) {
	acc, err := h.access()
	if err != nil {
		return nil, err
	}
	providers, err := acc.Statuses(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]oauthProviderResponse, 0, len(providers))
	for _, provider := range providers {
		items = append(items, oauthProviderSummary(provider))
	}
	return map[string]any{"providers": items}, nil
}

func (h oauthHandler) Disconnect(ctx context.Context, in DisconnectInput) (any, error) {
	acc, err := h.access()
	if err != nil {
		return nil, err
	}
	if err := acc.Disconnect(ctx, in.Provider); err != nil {
		return nil, err
	}
	return map[string]any{"provider": in.Provider, "status": "disconnected"}, nil
}

type oauthFlowResponse struct {
	Provider        string   `json:"provider"`
	FlowID          string   `json:"flow_id"`
	VerificationURI string   `json:"verification_uri"`
	UserCode        string   `json:"user_code,omitempty"`
	ExpiresAt       string   `json:"expires_at"`
	State           string   `json:"state"`
	RequestedScopes []string `json:"requested_scopes,omitempty"`
}
type oauthProviderResponse struct {
	Provider        string   `json:"provider"`
	Configured      bool     `json:"configured"`
	Connected       bool     `json:"connected"`
	Username        string   `json:"username,omitempty"`
	RequestedScopes []string `json:"requested_scopes,omitempty"`
	GrantedScopes   []string `json:"granted_scopes,omitempty"`
	NeedsReconnect  bool     `json:"needs_reconnect,omitempty"`
	ReconnectReason string   `json:"reconnect_reason,omitempty"`
}

func oauthFlowSummary(status FlowStatus) oauthFlowResponse {
	return oauthFlowResponse{Provider: status.Provider, FlowID: status.FlowID, VerificationURI: status.VerificationURI, UserCode: status.UserCode, ExpiresAt: status.ExpiresAt.UTC().Format(time.RFC3339), State: status.State, RequestedScopes: status.RequestedScopes}
}

func oauthProviderSummary(status ProviderStatus) oauthProviderResponse {
	return oauthProviderResponse{
		Provider: status.Provider, Configured: status.Configured, Connected: status.Connected,
		Username: status.Username, RequestedScopes: status.RequestedScopes,
		GrantedScopes: status.GrantedScopes, NeedsReconnect: status.NeedsReconnect,
		ReconnectReason: status.ReconnectReason,
	}
}
