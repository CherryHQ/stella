package channel

import (
	pkgchannel "github.com/vaayne/anna/pkg/channel"
)

// Re-exported from pkg/channel for internal callers.
var (
	SplitMessage   = pkgchannel.SplitMessage
	FormatDuration = pkgchannel.FormatDuration
)
