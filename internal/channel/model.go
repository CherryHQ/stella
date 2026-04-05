package channel

import (
	pkgchannel "github.com/vaayne/anna/pkg/channel"
)

// Type aliases re-exported from pkg/channel so that internal callers
// continue to compile without import changes.

// Platform identifiers.
const (
	PlatformTelegram = pkgchannel.PlatformTelegram
	PlatformQQ       = pkgchannel.PlatformQQ
	PlatformFeishu   = pkgchannel.PlatformFeishu
	PlatformWeixin   = pkgchannel.PlatformWeixin
	PlatformCLI      = pkgchannel.PlatformCLI
)

// Channel is the messaging platform adapter interface.
type Channel = pkgchannel.Channel

// Notification is a push message to send to a chat.
type Notification = pkgchannel.Notification

// ModelOption represents a selectable provider/model combination.
type ModelOption = pkgchannel.ModelOption

// AgentInfo is agent metadata for display in channel UIs.
type AgentInfo = pkgchannel.AgentInfo

// ModelListFunc returns the current list of available models.
type ModelListFunc func() []ModelOption

// ModelSwitchFunc switches the active model in the pool.
type ModelSwitchFunc func(provider, model string) error
