package sqlc

import (
	"context"
)

// VaultUser holds the age key fields the vault service needs from a user record.
type VaultUser struct {
	AgePublicKey  string
	AgePrivateKey string
}

// GetVaultUser returns the age key fields for a user. Satisfies vault.UserKeyReader.
func (q *Queries) GetVaultUser(ctx context.Context, id string) (VaultUser, error) {
	const qry = `SELECT age_public_key, age_private_key FROM auth_user WHERE id = $1 LIMIT 1`
	var v VaultUser
	row := q.db.QueryRow(ctx, qry, id)
	if err := row.Scan(&v.AgePublicKey, &v.AgePrivateKey); err != nil {
		return VaultUser{}, err
	}
	return v, nil
}
