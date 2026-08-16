//go:build windows

package main

import "context"

func installGoroutineDumpHandler(context.Context, func(context.Context) error) {}
