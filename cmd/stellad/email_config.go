package main

import (
	"context"

	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/plugins/email"
)

// emailConfigReader adapts the system vault at the composition root. Keeping
// the nil check here means an absent *vault.Service remains a nil
// email.ConfigReader instead of a non-nil interface containing a typed nil.
func emailConfigReader(vaultSvc *vault.Service) email.ConfigReader {
	if vaultSvc == nil {
		return nil
	}
	return func(ctx context.Context, userID string) (string, error) {
		return vaultSvc.Get(ctx, userID, email.ConfigName)
	}
}
