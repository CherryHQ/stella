package main

import (
	// Plugin providers.
	_ "github.com/vaayne/anna/plugins/providers/anthropic"
	_ "github.com/vaayne/anna/plugins/providers/openai"
	_ "github.com/vaayne/anna/plugins/providers/openai-response"

	// Plugin tools.
	_ "github.com/vaayne/anna/plugins/tools/bash"
	_ "github.com/vaayne/anna/plugins/tools/edit"
	_ "github.com/vaayne/anna/plugins/tools/mcp"
	_ "github.com/vaayne/anna/plugins/tools/read"
	_ "github.com/vaayne/anna/plugins/tools/webfetch"
	_ "github.com/vaayne/anna/plugins/tools/write"

	// Plugin hooks.
	_ "github.com/vaayne/anna/plugins/hooks/rtk"
	_ "github.com/vaayne/anna/plugins/hooks/trace"

	// Plugin memory.
	_ "github.com/vaayne/anna/plugins/memory/lcm"
	_ "github.com/vaayne/anna/plugins/memory/simple"
)
