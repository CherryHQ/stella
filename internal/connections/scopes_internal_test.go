package connections

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/authz"
	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/vault"
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

func TestDesiredScopes_UnionAndPolicy(t *testing.T) {
	svc := NewService(nil, nil, oauth.NewFlowStore(), "http://localhost:8080")
	reg := oauth.NewProviderRegistry()
	reg.Register(oauth.ProviderConfig{
		ID: "acme", VaultKey: "ACME_OAUTH",
		Scopes: []string{"profile"}, AllowedScopes: []string{"profile", "documents.read"},
	})
	svc.SetRegistry(reg)

	got, err := svc.desiredScopes(context.Background(), "user-1", "acme", []string{"documents.read", "profile"})
	if err != nil {
		t.Fatalf("desiredScopes: %v", err)
	}
	if want := []string{"profile", "documents.read"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("desiredScopes = %v, want %v", got, want)
	}

	_, err = svc.desiredScopes(context.Background(), "user-1", "acme", []string{"admin.write"})
	var denied *ScopeNotAllowedError
	if !errors.As(err, &denied) {
		t.Fatalf("desiredScopes denied error = %T %v, want *ScopeNotAllowedError", err, err)
	}
	if want := []string{"admin.write"}; !reflect.DeepEqual(denied.Scopes, want) {
		t.Fatalf("denied scopes = %v, want %v", denied.Scopes, want)
	}
}

func TestPolicyNarrowingFiltersHistoricalDesiredScopes(t *testing.T) {
	bundle := &oauth.OAuthBundle{DesiredScopes: []string{"documents.read", "admin.write"}}
	stored := bundleDesiredScopes(bundle, []string{"profile"})
	if want := []string{"profile", "documents.read", "admin.write"}; !reflect.DeepEqual(stored, want) {
		t.Fatalf("desired scopes = %v, want %v", stored, want)
	}
	policy := []string{"profile", "documents.read"}
	if removed := missingScopes(stored, policy); !reflect.DeepEqual(removed, []string{"admin.write"}) {
		t.Fatalf("removed scopes = %v, want [admin.write]", removed)
	}
	if got := allowedScopes(stored, policy); !reflect.DeepEqual(got, []string{"profile", "documents.read"}) {
		t.Fatalf("filtered desired scopes = %v, want [profile documents.read]", got)
	}
}

func TestPendingFlowReportsUserConsentOutcome(t *testing.T) {
	status := toFlowStatus(oauth.FlowStatus{State: oauth.FlowStatePending})
	if status.Outcome != OAuthOutcomeUserConsentRequired {
		t.Fatalf("pending flow outcome = %q, want %q", status.Outcome, OAuthOutcomeUserConsentRequired)
	}
}

func TestDesiredScopesPersistAcrossIncrementalFlowsAndPolicyNarrowing(t *testing.T) {
	db := dbtest.New(t)
	q := pkgdb.New(db)
	oidc := appdb.NewOIDCStore(db)
	ctx := context.Background()

	masterID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}
	vaultSvc, err := vault.NewService(q, masterID.String(), nil)
	if err != nil {
		t.Fatalf("vault.NewService: %v", err)
	}
	user, err := oidc.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "oauth-scopes@test.invalid", Name: "OAuth Scopes"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	publicKey, encryptedPrivateKey, err := vault.GenerateUserKeys(vaultSvc.MasterRecipient())
	if err != nil {
		t.Fatalf("GenerateUserKeys: %v", err)
	}
	if err := oidc.UpdateUserAgeKeys(ctx, user.ID, publicKey, encryptedPrivateKey); err != nil {
		t.Fatalf("UpdateUserAgeKeys: %v", err)
	}

	reg := oauth.NewProviderRegistry()
	reg.Register(oauth.ProviderConfig{
		ID: "acme", VaultKey: "ACME_OAUTH", ClientID: "client",
		Scopes: []string{"profile"}, AllowedScopes: []string{"profile", "documents.read"},
		Flows: []oauth.ProviderFlowConfig{{Type: "authorization_code", AuthURL: "https://example.test/authorize", TokenURL: "https://example.test/token"}},
	})
	svc := NewService(vaultSvc, q, oauth.NewFlowStore(), "http://localhost:8080")
	svc.SetRegistry(reg)
	authority, err := authz.NewUserAuthority(authz.UserID(user.ID), false)
	if err != nil {
		t.Fatalf("NewUserAuthority: %v", err)
	}
	out, err := (oauthHandler{svc: svc, authority: authority}).Connect(ctx, ConnectInput{
		Provider: "acme",
		Scopes:   []any{"admin.write"},
	})
	if err != nil {
		t.Fatalf("Connect denied scope: %v", err)
	}
	denied, ok := out.(oauthScopeDeniedResponse)
	if !ok || denied.Outcome != OAuthOutcomeScopeNotAllowed || !reflect.DeepEqual(denied.Scopes, []string{"admin.write"}) {
		t.Fatalf("scope denial = %#v", out)
	}
	if err := svc.saveBundle(ctx, "acme", user.ID, "access-1", "refresh-1", time.Now().Add(time.Hour), time.Time{}, "profile", []string{"profile"}); err != nil {
		t.Fatalf("save initial bundle: %v", err)
	}

	desired, err := svc.desiredScopes(ctx, user.ID, "acme", []string{"documents.read"})
	if err != nil {
		t.Fatalf("desiredScopes increment: %v", err)
	}
	if want := []string{"profile", "documents.read"}; !reflect.DeepEqual(desired, want) {
		t.Fatalf("incremental desired scopes = %v, want %v", desired, want)
	}
	if err := svc.saveBundle(ctx, "acme", user.ID, "access-2", "refresh-2", time.Now().Add(time.Hour), time.Time{}, "profile", desired); err != nil {
		t.Fatalf("save incremental bundle: %v", err)
	}
	stored, err := oauth.LoadOAuthBundle(ctx, vaultSvc, user.ID, "ACME_OAUTH")
	if err != nil {
		t.Fatalf("LoadOAuthBundle: %v", err)
	}
	if !reflect.DeepEqual(stored.DesiredScopes, desired) {
		t.Fatalf("stored desired scopes = %v, want %v", stored.DesiredScopes, desired)
	}

	if err := q.UpsertAuthOAuthProvider(ctx, pkgdb.UpsertAuthOAuthProviderParams{
		ID: uuid.Must(uuid.NewV7()).String(), ProviderID: "acme", ClientID: "client",
		Scopes: []string{"profile"}, AllowedScopes: []string{"profile"},
	}); err != nil {
		t.Fatalf("narrow provider policy: %v", err)
	}
	status := svc.getProviderStatus(ctx, user.ID, "acme")
	if !status.NeedsReconnect || status.ReconnectReason != ReconnectReasonScopePolicyChanged {
		t.Fatalf("policy-narrowed status = reconnect %v reason %q", status.NeedsReconnect, status.ReconnectReason)
	}
	if want := []string{"profile"}; !reflect.DeepEqual(status.RequestedScopes, want) {
		t.Fatalf("policy-narrowed requested scopes = %v, want %v", status.RequestedScopes, want)
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
		ID: uuid.Must(uuid.NewV7()).String(), ProviderID: "github", ClientID: "cid", Scopes: []string{}, AllowedScopes: []string{},
	}); err != nil {
		t.Fatalf("upsert empty scopes: %v", err)
	}
	if got := svc.providerScopes(ctx, "github"); !reflect.DeepEqual(got, []string{"repo"}) {
		t.Fatalf("empty override: providerScopes = %v, want [repo]", got)
	}

	// DB row with a non-empty scopes override → override wins, and it works even
	// with no client_id (independent of the credential gate).
	if err := q.UpsertAuthOAuthProvider(ctx, pkgdb.UpsertAuthOAuthProviderParams{
		ID: uuid.Must(uuid.NewV7()).String(), ProviderID: "github", ClientID: "", Scopes: []string{"repo", "read:org"}, AllowedScopes: []string{},
	}); err != nil {
		t.Fatalf("upsert override: %v", err)
	}
	if got := svc.providerScopes(ctx, "github"); !reflect.DeepEqual(got, []string{"repo", "read:org"}) {
		t.Fatalf("override: providerScopes = %v, want [repo read:org]", got)
	}
}
