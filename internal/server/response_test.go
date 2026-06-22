package server

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestIsUniqueViolation guards the post-migration conflict mapping: PostgreSQL
// reports a unique-constraint violation as a *pgconn.PgError with SQLSTATE 23505
// (the SQLite-era strings.Contains(err, "UNIQUE constraint") check is dead under
// PG). The store layer wraps the driver error with %w, so the predicate must see
// through the wrapping.
func TestIsUniqueViolation(t *testing.T) {
	unique := &pgconn.PgError{Code: "23505", Message: `duplicate key value violates unique constraint "idx_skill_owner_name"`}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"direct 23505", unique, true},
		{"wrapped 23505", fmt.Errorf("skills: create skill %q: %w", "x", unique), true},
		{"sqlite-era text only", errors.New("UNIQUE constraint failed: skill.name"), false},
		{"other pg error", &pgconn.PgError{Code: "23503"}, false},
		{"not found", pgx.ErrNoRows, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUniqueViolation(tc.err); got != tc.want {
				t.Fatalf("isUniqueViolation(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestIsNotFound covers the two "resource does not exist" shapes the handlers map
// to 404: an empty result (pgx.ErrNoRows) and a malformed-uuid lookup (22P02).
func TestIsNotFound(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"no rows", pgx.ErrNoRows, true},
		{"invalid uuid text", &pgconn.PgError{Code: "22P02"}, true},
		{"unique violation", &pgconn.PgError{Code: "23505"}, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNotFound(tc.err); got != tc.want {
				t.Fatalf("isNotFound(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
