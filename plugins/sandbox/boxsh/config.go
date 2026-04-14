package boxsh

import (
	"fmt"

	"github.com/vaayne/anna/plugins/sandbox/boxsh/boxshclient"
)

const (
	NetworkDisabled  = boxshclient.NetworkDisabled
	NetworkAllowAll  = boxshclient.NetworkAllowAll
	NetworkWhitelist = boxshclient.NetworkWhitelist
)

type NetworkConfig = boxshclient.NetworkConfig

type PreflightConfig struct {
	AnnaHome string
	UserRoot string
	Network  NetworkConfig
}

func (c PreflightConfig) Validate() error {
	switch c.Network.ModeOrDefault() {
	case NetworkDisabled, NetworkAllowAll:
		if len(c.Network.Allowlist) > 0 {
			return fmt.Errorf("sandbox.network.allowlist requires whitelist mode")
		}
		return nil
	case NetworkWhitelist:
		if len(c.Network.Allowlist) == 0 {
			return fmt.Errorf("sandbox.network.allowlist is required when mode is whitelist")
		}
		return nil
	default:
		return fmt.Errorf("sandbox.network.mode must be one of %q, %q, or %q", NetworkDisabled, NetworkAllowAll, NetworkWhitelist)
	}
}
