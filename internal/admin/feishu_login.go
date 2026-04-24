package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	pkgchannel "github.com/vaayne/anna/pkg/channel"
)

// FeishuLoginAvailability describes the current state of Feishu login.
type FeishuLoginAvailability struct {
	Available   bool   `json:"available"`
	Reason      string `json:"reason,omitempty"`
	InstanceID  string `json:"instance_id,omitempty"`
	HasConflict bool   `json:"has_conflict"`
}

// LoginEnabledFeishuConfig holds the resolved configuration for Feishu web login.
type LoginEnabledFeishuConfig struct {
	InstanceID    string
	AppID         string
	AppSecret     string
	TenantKey     string
	AutoProvision bool
}

// findLoginEnabledFeishuInstance discovers exactly one Feishu channel instance
// enabled for web login. It returns availability status and the config if found.
// The following states are possible:
//   - Available: exactly one valid login-enabled instance found
//   - Zero instances: not available, reason "no_login_instance"
//   - Multiple instances: not available, has_conflict=true, reason "multiple_login_instances"
//   - Missing credentials: not available, reason "missing_credentials"
func (s *Server) findLoginEnabledFeishuInstance(ctx context.Context) (FeishuLoginAvailability, *LoginEnabledFeishuConfig) {
	channels, err := s.store.ListChannels(ctx)
	if err != nil {
		return FeishuLoginAvailability{
			Available: false,
			Reason:    "store_error",
		}, nil
	}

	var found *LoginEnabledFeishuConfig
	var foundCount int

	for _, ch := range channels {
		if !ch.Enabled {
			continue
		}
		if ch.Type != pkgchannel.PlatformFeishu && ch.ID != pkgchannel.PlatformFeishu {
			continue
		}

		cfg, err := parseFeishuChannelConfig(ch.Config)
		if err != nil {
			continue
		}

		if !cfg.EnableLogin {
			continue
		}

		foundCount++
		if foundCount == 1 {
			found = &LoginEnabledFeishuConfig{
				InstanceID:    ch.ID,
				AppID:         cfg.AppID,
				AppSecret:     cfg.AppSecret,
				TenantKey:     cfg.TenantKey,
				AutoProvision: cfg.AutoProvision,
			}
		}
	}

	if foundCount == 0 {
		return FeishuLoginAvailability{
			Available: false,
			Reason:    "no_login_instance",
		}, nil
	}

	if foundCount > 1 {
		return FeishuLoginAvailability{
			Available:   false,
			HasConflict: true,
			Reason:      "multiple_login_instances",
		}, nil
	}

	// Validate credentials
	if found.AppID == "" || found.AppSecret == "" {
		return FeishuLoginAvailability{
			Available:  false,
			Reason:     "missing_credentials",
			InstanceID: found.InstanceID,
		}, nil
	}

	return FeishuLoginAvailability{
		Available:  true,
		InstanceID: found.InstanceID,
	}, found
}

// parseFeishuChannelConfig parses the JSON config string from a Feishu channel.
func parseFeishuChannelConfig(configJSON string) (*pkgchannel.FeishuConfig, error) {
	var cfg pkgchannel.FeishuConfig
	if configJSON == "" {
		return &cfg, nil
	}
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return nil, fmt.Errorf("parse feishu config: %w", err)
	}
	return &cfg, nil
}

// feishuLoginAvailabilityHandler handles GET /api/auth/login/feishu/availability.
// This endpoint is public (unauthenticated) so the login page can show/hide the button.
func (s *Server) feishuLoginAvailabilityHandler(w http.ResponseWriter, r *http.Request) {
	availability, _ := s.findLoginEnabledFeishuInstance(r.Context())
	writeData(w, http.StatusOK, availability)
}
