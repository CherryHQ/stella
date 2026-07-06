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
	return tools.Definition{Name: ToolName, Description: "Connect and manage external OAuth providers for this user. Actions: list providers, connect, status, disconnect. For connect, give the user the returned verification_uri and user_code, ask them to authorize and tell you when done, then call action=status with the flow_id. Never tell the user to run commands; never expose tokens.", InputSchema: InputSchema()}
}

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.svc == nil {
		return "", fmt.Errorf("oauth service is unavailable — try again later")
	}
	ident, err := authz.ToolIdentity(ctx, "oauth")
	if err != nil {
		return "", err
	}
	action, err := tools.ActionArg(args, "oauth")
	if err != nil {
		return "", err
	}
	out, err := Dispatch(ctx, oauthHandler{svc: t.svc, ident: ident}, action, args)
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
	svc   *Service
	ident authz.Identity
}

func (h oauthHandler) Connect(ctx context.Context, in ConnectInput) (any, error) {
	status, err := h.svc.As(h.ident).StartFlow(ctx, in.Provider)
	if err != nil {
		return nil, err
	}
	return oauthFlowSummary(status), nil
}

func (h oauthHandler) Status(ctx context.Context, in StatusInput) (any, error) {
	status, _, err := h.svc.As(h.ident).PollFlow(ctx, in.Provider, in.FlowId)
	if err != nil {
		return nil, err
	}
	return oauthFlowSummary(status), nil
}

func (h oauthHandler) List(ctx context.Context, _ ListInput) (any, error) {
	providers, err := h.svc.As(h.ident).Statuses(ctx)
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
	if err := h.svc.As(h.ident).Disconnect(ctx, in.Provider); err != nil {
		return nil, err
	}
	return map[string]any{"provider": in.Provider, "status": "disconnected"}, nil
}

type oauthFlowResponse struct {
	Provider        string `json:"provider"`
	FlowID          string `json:"flow_id"`
	VerificationURI string `json:"verification_uri"`
	UserCode        string `json:"user_code,omitempty"`
	ExpiresAt       string `json:"expires_at"`
	State           string `json:"state"`
}
type oauthProviderResponse struct {
	Provider   string `json:"provider"`
	Configured bool   `json:"configured"`
	Connected  bool   `json:"connected"`
	Username   string `json:"username,omitempty"`
}

func oauthFlowSummary(status FlowStatus) oauthFlowResponse {
	return oauthFlowResponse{Provider: status.Provider, FlowID: status.FlowID, VerificationURI: status.VerificationURI, UserCode: status.UserCode, ExpiresAt: status.ExpiresAt.UTC().Format(time.RFC3339), State: status.State}
}

func oauthProviderSummary(status ProviderStatus) oauthProviderResponse {
	return oauthProviderResponse{Provider: status.Provider, Configured: status.Configured, Connected: status.Connected, Username: status.Username}
}
