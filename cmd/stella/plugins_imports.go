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
	_ "github.com/CherryHQ/stella/plugins/reflect"

	// Plugin tools.
	_ "github.com/CherryHQ/stella/plugins/tools/bash"
	_ "github.com/CherryHQ/stella/plugins/tools/edit"
	_ "github.com/CherryHQ/stella/plugins/tools/lark-cli"
	_ "github.com/CherryHQ/stella/plugins/tools/mcp"
	_ "github.com/CherryHQ/stella/plugins/tools/notify"
	_ "github.com/CherryHQ/stella/plugins/tools/read"
	_ "github.com/CherryHQ/stella/plugins/tools/webfetch"
	_ "github.com/CherryHQ/stella/plugins/tools/write"

	// Plugin hooks.
	_ "github.com/CherryHQ/stella/plugins/hooks/rtk"
	_ "github.com/CherryHQ/stella/plugins/hooks/trace"

	// Plugin memory.
	_ "github.com/CherryHQ/stella/plugins/memory/lcm"
	_ "github.com/CherryHQ/stella/plugins/memory/simple"

	// Plugin sandbox backends.
	_ "github.com/CherryHQ/stella/plugins/sandbox"
)
