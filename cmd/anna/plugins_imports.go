package main

import (
	// Plugin channels.
	_ "github.com/vaayne/anna/plugins/channels/feishu"
	_ "github.com/vaayne/anna/plugins/channels/qq"
	_ "github.com/vaayne/anna/plugins/channels/telegram"
	_ "github.com/vaayne/anna/plugins/channels/weixin"

	// Plugin providers.
	_ "github.com/vaayne/anna/plugins/providers/anthropic"
	_ "github.com/vaayne/anna/plugins/providers/openai"
	_ "github.com/vaayne/anna/plugins/providers/openai-response"

	// Plugin runtimes.
	_ "github.com/vaayne/anna/plugins/reflect"

	// Plugin tools.
	_ "github.com/vaayne/anna/plugins/tools/bash"
	_ "github.com/vaayne/anna/plugins/tools/edit"
	_ "github.com/vaayne/anna/plugins/tools/mcp"
	_ "github.com/vaayne/anna/plugins/tools/mise"
	_ "github.com/vaayne/anna/plugins/tools/notify"
	_ "github.com/vaayne/anna/plugins/tools/read"
	_ "github.com/vaayne/anna/plugins/tools/tap-web"
	_ "github.com/vaayne/anna/plugins/tools/webfetch"
	_ "github.com/vaayne/anna/plugins/tools/write"

	// Plugin hooks.
	_ "github.com/vaayne/anna/plugins/hooks/rtk"
	_ "github.com/vaayne/anna/plugins/hooks/trace"

	// Plugin memory.
	_ "github.com/vaayne/anna/plugins/memory/lcm"
	_ "github.com/vaayne/anna/plugins/memory/simple"

	// Plugin sandbox backends.
	_ "github.com/vaayne/anna/plugins/sandbox"
)
