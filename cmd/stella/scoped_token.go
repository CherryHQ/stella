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

func scopedAgentIDFromEnv() (string, error) {
	claims, err := scopedTokenClaimsFromEnv()
	if err != nil {
		return "", err
	}
	if claims.AgentID == "" {
		return "", fmt.Errorf("permission denied")
	}
	return claims.AgentID, nil
}

func scopedArtifactContextFromEnv() (string, string, error) {
	claims, err := scopedTokenClaimsFromEnv()
	if err != nil {
		return "", "", err
	}
	if claims.AgentID == "" || claims.SessionID == "" {
		return "", "", fmt.Errorf("permission denied")
	}
	return claims.AgentID, claims.SessionID, nil
}
