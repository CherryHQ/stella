package connections

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"

	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	pkgdb "github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestNormalizeScopes(t *testing.T) {
	got := normalizeScopes([]string{"  repo ", "", "read:org", "repo", "  ", "read:org"})
	want := []string{"repo", "read:org"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("normalizeScopes = %v, want %v", got, want)
	}
	if got := normalizeScopes(nil); len(got) != 0 {
		t.Errorf("normalizeScopes(nil) = %v, want empty", got)
	}
}

func TestMissingScopes(t *testing.T) {
	got := missingScopes([]string{"a", "b", "c"}, []string{"b"})
	want := []string{"a", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("missingScopes = %v, want %v", got, want)
	}
	if got := missingScopes([]string{"a"}, []string{"a"}); len(got) != 0 {
		t.Errorf("missingScopes with full grant = %v, want empty", got)
	}
}

func TestReconnectDecision(t *testing.T) {
	tests := []struct {
		name          string
		bundleClient  string
		effectiveID   string
		requested     []string
		granted       []string
		grantedKnown  bool
		wantReconnect bool
		wantReason    string
	}{
		{
			name:         "clean: matching creds and full scopes",
			bundleClient: "cid", effectiveID: "cid",
			requested: []string{"a"}, granted: []string{"a"}, grantedKnown: true,
			wantReconnect: false, wantReason: "",
		},
		{
			name:         "rotated credentials take precedence over scopes",
			bundleClient: "old", effectiveID: "new",
			requested: []string{"a"}, granted: []string{"a"}, grantedKnown: true,
			wantReconnect: true, wantReason: ReconnectReasonCredentialsRotated,
		},
		{
			name:         "missing scopes when granted known",
			bundleClient: "cid", effectiveID: "cid",
			requested: []string{"a", "b"}, granted: []string{"a"}, grantedKnown: true,
			wantReconnect: true, wantReason: ReconnectReasonMissingScopes,
		},
		{
			name:         "unknown granted never asserts missing scopes",
			bundleClient: "cid", effectiveID: "cid",
			requested: []string{"a", "b"}, granted: nil, grantedKnown: false,
			wantReconnect: false, wantReason: "",
		},
		{
			name:         "empty effective client id does not trigger rotation",
			bundleClient: "cid", effectiveID: "",
			requested: nil, granted: nil, grantedKnown: false,
			wantReconnect: false, wantReason: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotReconnect, gotReason := reconnectDecision(tt.bundleClient, tt.effectiveID, tt.requested, tt.granted, tt.grantedKnown)
			if gotReconnect != tt.wantReconnect || gotReason != tt.wantReason {
				t.Errorf("reconnectDecision = (%v, %q), want (%v, %q)", gotReconnect, gotReason, tt.wantReconnect, tt.wantReason)
			}
		})
	}
}

// providerScopes resolves the DB override over the YAML default, independent of
// the client_id credential gate (D2).
func TestProviderScopes_OverrideWinsElseDefault(t *testing.T) {
	db := dbtest.New(t)
	q := pkgdb.New(db)
	svc := NewService(nil, q, oauth.NewFlowStore(), "http://localhost:8080")

	reg := oauth.NewProviderRegistry()
	reg.Register(oauth.ProviderConfig{ID: "github", VaultKey: oauth.VaultKeyGitHub, Scopes: []string{"repo"}})
	svc.SetRegistry(reg)
	ctx := context.Background()

	// No DB row → YAML default.
	if got := svc.providerScopes(ctx, "github"); !reflect.DeepEqual(got, []string{"repo"}) {
		t.Fatalf("no row: providerScopes = %v, want [repo]", got)
	}

	// DB row with empty scopes (credentials-only override) → still YAML default.
	if err := q.UpsertAuthOAuthProvider(ctx, pkgdb.UpsertAuthOAuthProviderParams{
		ID: uuid.Must(uuid.NewV7()).String(), ProviderID: "github", ClientID: "cid", Scopes: []string{},
	}); err != nil {
		t.Fatalf("upsert empty scopes: %v", err)
	}
	if got := svc.providerScopes(ctx, "github"); !reflect.DeepEqual(got, []string{"repo"}) {
		t.Fatalf("empty override: providerScopes = %v, want [repo]", got)
	}

	// DB row with a non-empty scopes override → override wins, and it works even
	// with no client_id (independent of the credential gate).
	if err := q.UpsertAuthOAuthProvider(ctx, pkgdb.UpsertAuthOAuthProviderParams{
		ID: uuid.Must(uuid.NewV7()).String(), ProviderID: "github", ClientID: "", Scopes: []string{"repo", "read:org"},
	}); err != nil {
		t.Fatalf("upsert override: %v", err)
	}
	if got := svc.providerScopes(ctx, "github"); !reflect.DeepEqual(got, []string{"repo", "read:org"}) {
		t.Fatalf("override: providerScopes = %v, want [repo read:org]", got)
	}
}
