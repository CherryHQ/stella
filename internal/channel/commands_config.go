package channel

import (
	"context"
	"fmt"
	"strings"

	"github.com/vaayne/anna/internal/vault"
)

// handleConfig processes the /config KEY VALUE command. It is the only
// secure plaintext-ingest path for manual secrets. List and delete operations
// are available via the credentials tool instead.
// Returns (message, ok) where ok is false on error or bad input.
func handleConfig(ctx context.Context, vaultSvc *vault.Service, userID int64, args string) (string, bool) {
	if vaultSvc == nil {
		return "Vault is not configured on this server.", false
	}
	if userID == 0 {
		return "You must be logged in to manage secrets.", false
	}

	fields := strings.Fields(args)
	if len(fields) < 2 {
		return "Usage: /config KEY VALUE\n\nStores a secret securely. Use the credentials tool to list or delete secrets.", false
	}

	key := strings.ToUpper(fields[0])
	value := strings.TrimSpace(strings.TrimPrefix(args, fields[0]))
	if err := vaultSvc.Set(ctx, userID, key, value); err != nil {
		return fmt.Sprintf("Error: %v", err), false
	}
	return fmt.Sprintf("Secret %s saved.", key), true
}
