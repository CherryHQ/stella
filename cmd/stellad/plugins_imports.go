package main

import (
	// Reflect registers runtime hooks outside the unified plugin catalog.
	_ "github.com/CherryHQ/stella/internal/reflect"

	// Builtin is the shared catalog imported by production and release checks.
	_ "github.com/CherryHQ/stella/plugins/builtin"
)
