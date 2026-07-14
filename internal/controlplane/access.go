package controlplane

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/authz"
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
// the email PEP: validate the Authority, acquire one immutable Evaluation,
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
// Control-plane resources deliberately carry no policy facts: they are
// admin-only, and the built-in admin-full-access policy is their sole grant.
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

func (a *Access) authorize(resource authz.ResourceType, action authz.Action, id string) error {
	res, err := authz.NewResource(resource, id, "")
	if err != nil {
		return a.decide(authz.Request{}, err)
	}
	req, err := authz.NewRequest(action, res, authz.InvocationFacts{})
	return a.decide(req, err)
}

func (a *Access) authorizeProvider(action authz.Action, id string) error {
	return a.authorize(authz.ResourceProvider, action, id)
}

func (a *Access) authorizeProviderList() error {
	return a.authorizeProvider(authz.ActionList, "")
}

func (a *Access) authorizeSettings(action authz.Action, id string) error {
	return a.authorize(authz.ResourceSettings, action, id)
}

func (a *Access) authorizePlugin(action authz.Action, id string) error {
	return a.authorize(authz.ResourcePlugin, action, id)
}

func (a *Access) authorizePluginList() error {
	return a.authorizePlugin(authz.ActionList, "")
}

func (a *Access) authorizeChannel(action authz.Action, id string) error {
	return a.authorize(authz.ResourceChannel, action, id)
}

func (a *Access) authorizeChannelList() error {
	return a.authorizeChannel(authz.ActionList, "")
}
