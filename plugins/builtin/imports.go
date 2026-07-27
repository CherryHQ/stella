// Package builtin links every plugin shipped in the stellad binary into the
// process-wide plugin catalog. Production and release validation import this
// package so the builtin list has a single source of truth.
package builtin

import (
	// Plugin channels.
	_ "github.com/CherryHQ/stella/plugins/channels/feishu"
	_ "github.com/CherryHQ/stella/plugins/channels/qq"
	_ "github.com/CherryHQ/stella/plugins/channels/telegram"
	_ "github.com/CherryHQ/stella/plugins/channels/webhook"
	_ "github.com/CherryHQ/stella/plugins/channels/weixin"

	// Plugin providers.
	_ "github.com/CherryHQ/stella/plugins/providers/anthropic"
	_ "github.com/CherryHQ/stella/plugins/providers/openai"
	_ "github.com/CherryHQ/stella/plugins/providers/openai-response"

	// Plugin hooks, sandbox backends, and tools.
	_ "github.com/CherryHQ/stella/plugins/hooks/rtk"
	_ "github.com/CherryHQ/stella/plugins/sandbox"
	_ "github.com/CherryHQ/stella/plugins/tools/webfetch"
)
