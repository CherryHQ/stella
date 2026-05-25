package sqlc

import (
	"context"
	"database/sql"
)

// VaultUser holds the age key fields the vault service needs from a user record.
type VaultUser struct {
	AgePublicKey  string
	AgePrivateKey string
}

// VaultUserRecord holds the fields needed to backfill age keys.
type VaultUserRecord struct {
	ID           string
	AgePublicKey string
}

// GetVaultUser returns the age key fields for a user. Satisfies vault.UserKeyReader.
func (q *Queries) GetVaultUser(ctx context.Context, id string) (VaultUser, error) {
	const qry = `SELECT age_public_key, age_private_key FROM auth_user WHERE id = ? LIMIT 1`
	var v VaultUser
	row := q.db.QueryRowContext(ctx, qry, id)
	if err := row.Scan(&v.AgePublicKey, &v.AgePrivateKey); err != nil {
		if err == sql.ErrNoRows {
			return VaultUser{}, sql.ErrNoRows
		}
		return VaultUser{}, err
	}
	return v, nil
}

// ListVaultUsers returns the ID and age public key for all users.
// Satisfies vault.BackfillDB.
func (q *Queries) ListVaultUsers(ctx context.Context) ([]VaultUserRecord, error) {
	const qry = `SELECT id, age_public_key FROM auth_user ORDER BY created_at`
	rows, err := q.db.QueryContext(ctx, qry)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []VaultUserRecord
	for rows.Next() {
		var r VaultUserRecord
		if err := rows.Scan(&r.ID, &r.AgePublicKey); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateVaultUserAgeKeys updates age keys for a user. Satisfies vault.BackfillDB.
func (q *Queries) UpdateVaultUserAgeKeys(ctx context.Context, id, publicKey, privateKey string) error {
	const qry = `UPDATE auth_user SET age_public_key = ?, age_private_key = ?, updated_at = datetime('now') WHERE id = ?`
	_, err := q.db.ExecContext(ctx, qry, publicKey, privateKey, id)
	return err
}
