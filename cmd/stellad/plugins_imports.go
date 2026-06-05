package main

import (
	// Plugin channels.
	_ "github.com/CherryHQ/stella/plugins/channels/feishu"
	_ "github.com/CherryHQ/stella/plugins/channels/qq"
	_ "github.com/CherryHQ/stella/plugins/channels/telegram"
	_ "github.com/CherryHQ/stella/plugins/channels/weixin"

	// Plugin providers.
	_ "github.com/CherryHQ/stella/plugins/providers/anthropic"
	_ "github.com/CherryHQ/stella/plugins/providers/openai"
	_ "github.com/CherryHQ/stella/plugins/providers/openai-response"

	// Plugin runtimes.
	_ "github.com/CherryHQ/stella/internal/reflect"

	// Plugin tools.
	_ "github.com/CherryHQ/stella/plugins/tools/webfetch"

	// Plugin hooks.
	_ "github.com/CherryHQ/stella/plugins/hooks/rtk"

	// Plugin sandbox backends.
	_ "github.com/CherryHQ/stella/plugins/sandbox"
)
