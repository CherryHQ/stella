// Package plugins triggers self-registration of all plugin tools and hooks
// via blank imports. Import this package once at the application entry point.
package plugins

import (
	// Plugin providers.
	_ "github.com/vaayne/anna/plugins/providers/anthropic"
	_ "github.com/vaayne/anna/plugins/providers/openai"
	_ "github.com/vaayne/anna/plugins/providers/openai-response"

	// Plugin tools.
	_ "github.com/vaayne/anna/plugins/tools/bash"
	_ "github.com/vaayne/anna/plugins/tools/edit"
	_ "github.com/vaayne/anna/plugins/tools/read"
	_ "github.com/vaayne/anna/plugins/tools/webfetch"
	_ "github.com/vaayne/anna/plugins/tools/write"

	// Plugin hooks.
	_ "github.com/vaayne/anna/plugins/hooks/rtk"
)
