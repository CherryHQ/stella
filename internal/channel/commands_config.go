package channel

import (
	"context"
	"fmt"
	"strings"

	"github.com/vaayne/anna/internal/vault"
)

const configUsage = `Usage: /config <subcommand> [args]

Subcommands:
  list               List all stored secrets (names only)
  add <KEY> <VALUE>  Add or update a secret
  remove <KEY>       Remove a secret

Keys must be uppercase (e.g. MY_API_KEY).`

func handleConfig(ctx context.Context, vaultSvc *vault.Service, userID int64, args string) string {
	if vaultSvc == nil {
		return "Vault is not configured on this server."
	}
	if userID == 0 {
		return "You must be logged in to manage secrets."
	}

	fields := strings.Fields(args)
	sub := "list"
	if len(fields) > 0 {
		sub = strings.ToLower(fields[0])
	}

	switch sub {
	case "list":
		return configList(ctx, vaultSvc, userID)
	case "add", "set":
		if len(fields) < 3 {
			return "Usage: /config add <KEY> <VALUE>"
		}
		key := strings.ToUpper(fields[1])
		value := strings.TrimSpace(strings.TrimPrefix(args, fields[0]))
		value = strings.TrimSpace(strings.TrimPrefix(value, fields[1]))
		return configSet(ctx, vaultSvc, userID, key, value)
	case "remove", "delete", "rm":
		if len(fields) < 2 {
			return "Usage: /config remove <KEY>"
		}
		key := strings.ToUpper(fields[1])
		return configDelete(ctx, vaultSvc, userID, key)
	default:
		return configUsage
	}
}

func configList(ctx context.Context, svc *vault.Service, userID int64) string {
	entries, err := svc.List(ctx, userID)
	if err != nil {
		return fmt.Sprintf("Error listing secrets: %v", err)
	}
	if len(entries) == 0 {
		return "No secrets stored. Use /config add <KEY> <VALUE> to add one."
	}
	var b strings.Builder
	b.WriteString("Stored secrets:\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "  %s  (updated %s)\n", e.Name, e.UpdatedAt)
	}
	return b.String()
}

func configSet(ctx context.Context, svc *vault.Service, userID int64, key, value string) string {
	if err := svc.Set(ctx, userID, key, value); err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	return fmt.Sprintf("Secret %s saved.", key)
}

func configDelete(ctx context.Context, svc *vault.Service, userID int64, key string) string {
	if err := svc.Delete(ctx, userID, key); err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	return fmt.Sprintf("Secret %s removed.", key)
}
