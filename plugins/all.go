// Package plugins triggers self-registration of all plugin tools and hooks
// via blank imports. Import this package once at the application entry point.
package plugins

import (
	// Plugin tools.
	_ "github.com/vaayne/anna/plugins/tools/webfetch"

	// Plugin hooks.
	_ "github.com/vaayne/anna/plugins/hooks/rtk"
)
