package agent

import (
	"context"
	"fmt"
	"strings"

	agentsandbox "github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/vault"
)

type execSecretResolver struct {
	cfg agentsandbox.Config
}

func newExecSecretResolver(cfg agentsandbox.Config) *execSecretResolver {
	if cfg.VaultEnvLoader == nil {
		return nil
	}
	return &execSecretResolver{cfg: cfg}
}

func (r *execSecretResolver) ResolveExecSecrets(ctx context.Context, names []string, command string) (map[string]string, []string, error) {
	if r.cfg.GroupID != "" {
		return nil, nil, fmt.Errorf("secrets are disabled for group sessions")
	}
	if r.cfg.UserID == "" || r.cfg.AgentID == "" {
		return nil, nil, fmt.Errorf("secrets require a user and agent session")
	}
	env, valid, err := r.cfg.VaultEnvLoader.ResolveDeclarableEnv(ctx, r.cfg.UserID, r.cfg.AgentID, r.cfg.ProjectID, names)
	if err != nil {
		return nil, valid, err
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		if err := r.cfg.VaultEnvLoader.RecordExecSecretUse(ctx, r.cfg.UserID, r.cfg.AgentID, r.cfg.SessionID, name, command); err != nil {
			return nil, valid, err
		}
	}
	return env, valid, nil
}

func formatDeclarableSecrets(secrets []vault.DeclarableSecret) string {
	var b strings.Builder
	for _, s := range secrets {
		if s.Description == "" {
			fmt.Fprintf(&b, "- %s\n", s.Name)
		} else {
			fmt.Fprintf(&b, "- %s — %s\n", s.Name, s.Description)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
