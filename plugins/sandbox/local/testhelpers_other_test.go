//go:build !linux

package local

import "testing"

func skipIfBwrapNotFunctional(t *testing.T) { t.Helper() }
