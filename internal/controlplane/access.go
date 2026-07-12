package controlplane

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
)

// Access is one control-plane use case bound to exactly one Authorizer
// evaluation. The Service is the sole policy-enforcement point for the control
// plane; the transport passes a trusted authz.Authority and never a bare
// identity. Every method authorizes first, then performs the durable write and
// hot-reload the legacy handler did.
type Access struct {
	svc  *Service
	eval authz.Evaluation
}

// Begin opens exactly one evaluation for one control-plane use case. It mirrors
// the email PEP: validate the Authority, acquire one revision-bound Evaluation,
// and fail closed when the authorizer is not configured.
func (s *Service) Begin(ctx context.Context, authority authz.Authority) (*Access, error) {
	if s == nil || s.authz == nil {
		return nil, fmt.Errorf("%w: authorizer not configured", ErrUnavailable)
	}
	if !authority.Valid() {
		return nil, authz.ErrForbidden
	}
	eval, err := s.authz.Begin(ctx, authority)
	if err != nil {
		return nil, fmt.Errorf("controlplane authorization begin: %w", err)
	}
	return &Access{svc: s, eval: eval}, nil
}

// decide answers one control-plane request against this Access's single revision.
// A build error is treated as a denial (never leaks an internal build failure as
// success); a decide error is wrapped; a not-allowed decision is a 403.
func (a *Access) decide(req authz.Request, buildErr error) error {
	if buildErr != nil {
		return authz.ErrForbidden
	}
	dec, err := a.eval.Decide(req)
	if err != nil {
		return fmt.Errorf("controlplane decide: %w", err)
	}
	if !dec.Allowed() {
		return authz.ErrForbidden
	}
	return nil
}

// ---- Provider ----

func (a *Access) authorizeProvider(action authz.Action, id string, f policy.ProviderFacts) error {
	req, err := policy.ProviderRequest(action, id, "", f)
	return a.decide(req, err)
}

func (a *Access) authorizeProviderList() error {
	req, err := policy.ProviderListRequest()
	return a.decide(req, err)
}

// ---- Settings ----

func (a *Access) authorizeSettings(action authz.Action, id string, f policy.SettingsFacts) error {
	req, err := policy.SettingsRequest(action, id, "", f)
	return a.decide(req, err)
}

// ---- Plugin ----

func (a *Access) authorizePlugin(action authz.Action, id string, f policy.PluginFacts) error {
	req, err := policy.PluginRequest(action, id, "", f)
	return a.decide(req, err)
}

func (a *Access) authorizePluginList() error {
	req, err := policy.PluginListRequest()
	return a.decide(req, err)
}

// ---- Channel ----

func (a *Access) authorizeChannel(action authz.Action, id string, f policy.ChannelFacts) error {
	req, err := policy.ChannelRequest(action, id, "", f)
	return a.decide(req, err)
}

func (a *Access) authorizeChannelList() error {
	req, err := policy.ChannelListRequest()
	return a.decide(req, err)
}
