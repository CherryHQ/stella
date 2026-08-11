package connections

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/tools"
)

type Tool struct{ svc *Service }

func NewTool(svc *Service) *Tool { return &Tool{svc: svc} }
func (t *Tool) Definition() tools.Definition {
	return tools.Definition{Name: ToolName, Description: "Connect and manage external OAuth providers for this user. Actions: list providers, connect, status, disconnect. connect accepts optional scopes and adds them to this user's cumulative requested scopes; the provider's consent screen decides what is actually granted. Give the user the returned verification_uri and user_code, ask them to authorize and tell you when done, then call action=status with the flow_id. Never tell the user to run commands; never expose tokens.", InputSchema: InputSchema()}
}

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.svc == nil {
		return "", fmt.Errorf("oauth service is unavailable — try again later")
	}
	ident, err := authz.ToolIdentity(ctx, "oauth")
	if err != nil {
		return "", err
	}
	// The runtime context identity is the trusted adapter: a delegated agent turn
	// becomes a confined AgentActor. Model-supplied arguments never form identity.
	authority, err := ident.ToAuthority()
	if err != nil {
		return "", mapOAuthToolError(err)
	}
	action, err := tools.ActionArg(args, "oauth")
	if err != nil {
		return "", err
	}
	out, err := Dispatch(ctx, oauthHandler{svc: t.svc, authority: authority}, action, args)
	if err != nil {
		return "", mapOAuthToolError(err)
	}
	return tools.MarshalResult(out)
}

func mapOAuthToolError(err error) error {
	if errors.Is(err, authz.ErrNotFound) {
		return fmt.Errorf("flow expired or unknown — start a new connect")
	}
	return authz.MapError("oauth", err)
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

func (h oauthHandler) Status(ctx context.Context, in StatusInput) (any, error) {
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
