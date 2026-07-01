package credential

import (
	"context"
	"fmt"
)

// resolveScoped is the sandbox scoped-token sub-resolver. Verification is
// unchanged and delegated to the token backend; this adapter only maps the
// result into a Principal. The agent-ownership boundary is applied later, in
// Enforce (layer 3), not here.
func (s *Service) resolveScoped(ctx context.Context, raw string) (*Principal, error) {
	if s.tokens == nil {
		return nil, fmt.Errorf("credential: scoped auth not configured")
	}
	res, err := s.tokens.AuthenticateScoped(ctx, raw)
	if err != nil {
		return nil, err
	}
	if !res.Identity.IsActive {
		return nil, fmt.Errorf("credential: scoped token user deactivated")
	}
	return &Principal{
		Kind:      KindScoped,
		UserID:    res.Identity.UserID,
		AgentID:   res.AgentID,
		SessionID: res.SessionID,
		ProjectID: res.ProjectID,
		Scopes:    res.Scopes,
		Username:  res.Identity.Username,
		Email:     res.Identity.Email,
		Name:      res.Identity.Name,
		AvatarURL: res.Identity.AvatarURL,
		Role:      res.Identity.Role,
		IsAdmin:   false,
	}, nil
}
