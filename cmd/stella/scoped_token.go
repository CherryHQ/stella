package main

import (
	"fmt"
	"os"

	"github.com/CherryHQ/stella/internal/auth"
)

func scopedTokenClaimsFromEnv() (auth.ScopedTokenClaims, error) {
	raw := os.Getenv("STELLA_TOKEN")
	if raw == "" {
		return auth.ScopedTokenClaims{}, fmt.Errorf("STELLA_TOKEN env var is required")
	}
	claims, err := auth.ParseScopedTokenUnverified(raw)
	if err != nil {
		return auth.ScopedTokenClaims{}, fmt.Errorf("STELLA_TOKEN must be a scoped sandbox token: %w", err)
	}
	return claims, nil
}
