package webhook

import (
	"fmt"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// DecodeConfig parses the persisted webhook channel config.
func DecodeConfig(raw map[string]any) (pkgchannel.WebhookConfig, error) {
	return pkgchannel.DecodePluginConfig[pkgchannel.WebhookConfig](raw, pkgchannel.PlatformWebhook)
}

// validateConfig returns a non-empty reason when the config is invalid.
func validateConfig(cfg pkgchannel.WebhookConfig) string {
	switch cfg.Provider {
	case "", pkgchannel.WebhookProviderGeneric:
		if len(cfg.GitHubEvents) != 0 || len(cfg.GitHubRepositories) != 0 {
			return "webhook: github allowlists require provider github"
		}
	case pkgchannel.WebhookProviderGitHub:
		if !validAllowlist(cfg.GitHubEvents) || !validAllowlist(cfg.GitHubRepositories) {
			return "webhook: github provider requires non-empty unique event and repository allowlists"
		}
	default:
		return fmt.Sprintf("webhook: invalid provider %q (want generic or github)", cfg.Provider)
	}
	switch cfg.SessionMode {
	case "", pkgchannel.WebhookSessionEphemeral, pkgchannel.WebhookSessionPersistent:
	default:
		return fmt.Sprintf("webhook: invalid session_mode %q (want ephemeral or persistent)", cfg.SessionMode)
	}
	if cfg.WaitTimeoutSeconds < 0 || cfg.WaitTimeoutSeconds > pkgchannel.WebhookWaitTimeoutCeilingSeconds {
		return fmt.Sprintf("webhook: wait_timeout_seconds must be between 0 and %d", pkgchannel.WebhookWaitTimeoutCeilingSeconds)
	}
	if cfg.MaxRunTimeoutSeconds < 0 || cfg.MaxRunTimeoutSeconds > pkgchannel.WebhookRunTimeoutCeilingSeconds {
		return fmt.Sprintf("webhook: max_run_timeout_seconds must be between 0 and %d", pkgchannel.WebhookRunTimeoutCeilingSeconds)
	}
	return ""
}

func validAllowlist(values []string) bool {
	if len(values) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || len(value) > 256 {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

// Validate is the plugin host validation hook.
func Validate(raw map[string]any) error {
	cfg, err := DecodeConfig(raw)
	if err != nil {
		return err
	}
	if reason := validateConfig(cfg); reason != "" {
		return fmt.Errorf("%s", reason)
	}
	return nil
}

// RedactConfig returns the behavior-only config unchanged. URL capability
// verifiers and encrypted provider secrets live in the endpoint domain, never
// in plugin config.
func RedactConfig(raw map[string]any) map[string]any {
	return pkgchannel.CloneConfigMap(raw)
}
