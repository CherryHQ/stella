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

// RedactConfig has nothing secret to hide (auth is via the caller's PAT, not a
// stored token), so it returns the config unchanged.
func RedactConfig(raw map[string]any) map[string]any {
	return pkgchannel.CloneConfigMap(raw)
}
