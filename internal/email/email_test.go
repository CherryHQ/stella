package email_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/email"
)

func repeat(s string, n int) string {
	out := make([]byte, len(s)*n)
	for i := range n {
		copy(out[i*len(s):], s)
	}
	return string(out)
}

func TestValidateAccountName(t *testing.T) {
	valid := []string{"a", "ab", "a1", "a_b", "abc123", "a" + repeat("a", 31)}
	for _, name := range valid {
		if err := email.ValidateAccountName(name); err != nil {
			t.Errorf("expected %q to be valid, got: %v", name, err)
		}
	}

	invalid := []string{
		"",
		"A",
		"1abc",
		"_abc",
		"abc-def",
		"abc def",
		"ABC",
		"a" + repeat("a", 32), // 33 chars total → too long
	}
	for _, name := range invalid {
		if err := email.ValidateAccountName(name); err == nil {
			t.Errorf("expected %q to be invalid, but got nil error", name)
		}
	}
}

func TestValidateAccountEgressRejectsPrivateHosts(t *testing.T) {
	acct := email.EmailAccount{IMAPHost: "127.0.0.1", SMTPHost: "8.8.8.8"}
	if err := email.ValidateAccountEgress(acct); err == nil {
		t.Fatal("expected loopback IMAP host to be rejected")
	}

	acct = email.EmailAccount{IMAPHost: "8.8.8.8", SMTPHost: "10.0.0.1"}
	if err := email.ValidateAccountEgress(acct); err == nil {
		t.Fatal("expected private SMTP host to be rejected")
	}
}

func TestValidateAccountEgressAllowsPublicLiteralHosts(t *testing.T) {
	acct := email.EmailAccount{IMAPHost: "8.8.8.8", SMTPHost: "1.1.1.1"}
	if err := email.ValidateAccountEgress(acct); err != nil {
		t.Fatalf("expected public literal hosts to pass: %v", err)
	}
}

func TestConfigJSONRoundTrip(t *testing.T) {
	cfg := &email.Config{
		Default: "work",
		Accounts: map[string]email.EmailAccount{
			"work": {
				IMAPHost: "imap.example.com",
				IMAPPort: 993,
				IMAPTLS:  "ssl",
				SMTPHost: "smtp.example.com",
				SMTPPort: 587,
				SMTPTLS:  "starttls",
				Username: "user@example.com",
				Password: "secret",
				From:     "User <user@example.com>",
			},
		},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got email.Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Default != cfg.Default {
		t.Errorf("Default: want %q, got %q", cfg.Default, got.Default)
	}
	acct := got.Accounts["work"]
	orig := cfg.Accounts["work"]
	if acct != orig {
		t.Errorf("accounts[work]: want %+v, got %+v", orig, acct)
	}
}

func TestResolve(t *testing.T) {
	cfg := &email.Config{
		Default: "work",
		Accounts: map[string]email.EmailAccount{
			"work": {
				IMAPHost: "imap.example.com",
				Username: "user@example.com",
			},
			"personal": {
				IMAPHost: "imap.personal.com",
				IMAPPort: 993,
				Username: "me@personal.com",
			},
		},
	}

	t.Run("default account", func(t *testing.T) {
		acct, err := cfg.Resolve("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if acct.IMAPHost != "imap.example.com" {
			t.Errorf("want imap.example.com, got %q", acct.IMAPHost)
		}
		if acct.IMAPPort != 993 {
			t.Errorf("want IMAPPort 993, got %d", acct.IMAPPort)
		}
		if acct.SMTPPort != 587 {
			t.Errorf("want SMTPPort 587, got %d", acct.SMTPPort)
		}
		if acct.IMAPTLS != "ssl" {
			t.Errorf("want IMAPTLS ssl, got %q", acct.IMAPTLS)
		}
		if acct.SMTPTLS != "starttls" {
			t.Errorf("want SMTPTLS starttls, got %q", acct.SMTPTLS)
		}
	})

	t.Run("named account", func(t *testing.T) {
		acct, err := cfg.Resolve("personal")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if acct.IMAPHost != "imap.personal.com" {
			t.Errorf("want imap.personal.com, got %q", acct.IMAPHost)
		}
	})

	t.Run("missing account", func(t *testing.T) {
		if _, err := cfg.Resolve("missing"); err == nil {
			t.Fatal("expected error for missing account")
		}
	})

	t.Run("no default set", func(t *testing.T) {
		empty := &email.Config{Accounts: map[string]email.EmailAccount{}}
		if _, err := empty.Resolve(""); err == nil {
			t.Fatal("expected error when no default is set")
		}
	})
}

func TestResolveDefaultsNotPersisted(t *testing.T) {
	cfg := &email.Config{
		Default: "work",
		Accounts: map[string]email.EmailAccount{
			"work": {IMAPHost: "imap.example.com"},
		},
	}
	acct, _ := cfg.Resolve("")
	if acct.IMAPPort != 993 {
		t.Fatalf("expected default port 993, got %d", acct.IMAPPort)
	}
	stored := cfg.Accounts["work"]
	if stored.IMAPPort != 0 {
		t.Errorf("default port leaked into stored account: %d", stored.IMAPPort)
	}
}

func TestUpsert(t *testing.T) {
	t.Run("new account", func(t *testing.T) {
		cfg := &email.Config{}
		cfg.Upsert("work", email.EmailAccount{
			IMAPHost: "imap.example.com",
			Username: "user@example.com",
		})
		acct, ok := cfg.Accounts["work"]
		if !ok {
			t.Fatal("account not created")
		}
		if acct.IMAPHost != "imap.example.com" {
			t.Errorf("want imap.example.com, got %q", acct.IMAPHost)
		}
	})

	t.Run("partial update only overwrites non-zero fields", func(t *testing.T) {
		cfg := &email.Config{
			Accounts: map[string]email.EmailAccount{
				"work": {
					IMAPHost: "imap.example.com",
					Username: "original@example.com",
					Password: "oldpass",
				},
			},
		}
		cfg.Upsert("work", email.EmailAccount{
			Password: "newpass",
		})
		acct := cfg.Accounts["work"]
		if acct.IMAPHost != "imap.example.com" {
			t.Errorf("IMAPHost should be unchanged: got %q", acct.IMAPHost)
		}
		if acct.Username != "original@example.com" {
			t.Errorf("Username should be unchanged: got %q", acct.Username)
		}
		if acct.Password != "newpass" {
			t.Errorf("Password should be updated: got %q", acct.Password)
		}
	})
}

func TestRemove(t *testing.T) {
	t.Run("exists", func(t *testing.T) {
		cfg := &email.Config{
			Default: "work",
			Accounts: map[string]email.EmailAccount{
				"work": {},
			},
		}
		if err := cfg.Remove("work"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := cfg.Accounts["work"]; ok {
			t.Error("account still present after removal")
		}
		if cfg.Default != "" {
			t.Errorf("default should be cleared, got %q", cfg.Default)
		}
	})

	t.Run("does not exist", func(t *testing.T) {
		cfg := &email.Config{Accounts: map[string]email.EmailAccount{}}
		if err := cfg.Remove("missing"); err == nil {
			t.Fatal("expected error for non-existent account")
		}
	})

	t.Run("remove non-default does not clear default", func(t *testing.T) {
		cfg := &email.Config{
			Default: "work",
			Accounts: map[string]email.EmailAccount{
				"work":     {},
				"personal": {},
			},
		}
		if err := cfg.Remove("personal"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Default != "work" {
			t.Errorf("default should remain work, got %q", cfg.Default)
		}
	})
}

func TestSetDefault(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		cfg := &email.Config{
			Accounts: map[string]email.EmailAccount{
				"work": {},
			},
		}
		if err := cfg.SetDefault("work"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Default != "work" {
			t.Errorf("want default work, got %q", cfg.Default)
		}
	})

	t.Run("account does not exist", func(t *testing.T) {
		cfg := &email.Config{Accounts: map[string]email.EmailAccount{}}
		if err := cfg.SetDefault("missing"); err == nil {
			t.Fatal("expected error for non-existent account")
		}
	})
}

func TestAccountNames(t *testing.T) {
	cfg := &email.Config{
		Accounts: map[string]email.EmailAccount{
			"zebra": {},
			"alpha": {},
			"beta":  {},
		},
	}
	names := cfg.AccountNames()
	want := []string{"alpha", "beta", "zebra"}
	if len(names) != len(want) {
		t.Fatalf("want %v, got %v", want, names)
	}
	for i, n := range names {
		if n != want[i] {
			t.Errorf("names[%d]: want %q, got %q", i, want[i], n)
		}
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Run("valid JSON", func(t *testing.T) {
		t.Setenv("EMAIL_CONFIG", `{"default":"work","accounts":{"work":{"imap_host":"imap.example.com","username":"u@example.com"}}}`)
		cfg, err := email.LoadFromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Default != "work" {
			t.Errorf("want default work, got %q", cfg.Default)
		}
		if cfg.Accounts["work"].IMAPHost != "imap.example.com" {
			t.Errorf("unexpected imap_host: %q", cfg.Accounts["work"].IMAPHost)
		}
	})

	t.Run("env var not set", func(t *testing.T) {
		// Use t.Setenv to guard cleanup, then manually unset to simulate absence.
		t.Setenv("EMAIL_CONFIG", "placeholder")
		if err := os.Unsetenv("EMAIL_CONFIG"); err != nil {
			t.Fatalf("unsetenv: %v", err)
		}
		_, err := email.LoadFromEnv()
		if err == nil {
			t.Fatal("expected error when EMAIL_CONFIG is not set")
		}
	})

	t.Run("empty env var", func(t *testing.T) {
		t.Setenv("EMAIL_CONFIG", "")
		cfg, err := email.LoadFromEnv()
		if err != nil {
			t.Fatalf("unexpected error for empty env var: %v", err)
		}
		if cfg.Accounts == nil {
			t.Error("accounts map should be initialized")
		}
	})

	t.Run("env var is {}", func(t *testing.T) {
		t.Setenv("EMAIL_CONFIG", "{}")
		cfg, err := email.LoadFromEnv()
		if err != nil {
			t.Fatalf("unexpected error for {} env var: %v", err)
		}
		if cfg.Accounts == nil {
			t.Error("accounts map should be initialized")
		}
	})
}

func TestConfigValidate(t *testing.T) {
	t.Run("valid configuration", func(t *testing.T) {
		cfg := &email.Config{
			Default: "work",
			Accounts: map[string]email.EmailAccount{
				"work": {
					IMAPHost: "imap.example.com",
					SMTPHost: "smtp.example.com",
					Username: "user@example.com",
					From:     "user@example.com",
				},
			},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected validation to pass, got: %v", err)
		}
	})

	t.Run("invalid default account name", func(t *testing.T) {
		cfg := &email.Config{
			Default: "nonexistent",
			Accounts: map[string]email.EmailAccount{
				"work": {
					IMAPHost: "imap.example.com",
					SMTPHost: "smtp.example.com",
					Username: "user@example.com",
					From:     "user@example.com",
				},
			},
		}
		if err := cfg.Validate(); err == nil {
			t.Error("expected validation to fail for invalid default account name")
		}
	})

	t.Run("invalid account name key format", func(t *testing.T) {
		cfg := &email.Config{
			Accounts: map[string]email.EmailAccount{
				"invalid-name": {
					IMAPHost: "imap.example.com",
					SMTPHost: "smtp.example.com",
					Username: "user@example.com",
					From:     "user@example.com",
				},
			},
		}
		if err := cfg.Validate(); err == nil {
			t.Error("expected validation to fail for invalid account name format")
		}
	})

	t.Run("default set with empty accounts", func(t *testing.T) {
		cfg := &email.Config{
			Default:  "ghost",
			Accounts: map[string]email.EmailAccount{},
		}
		if err := cfg.Validate(); err == nil {
			t.Error("expected validation to fail when default references nonexistent account in empty map")
		}
	})

	t.Run("missing required fields", func(t *testing.T) {
		cfg := &email.Config{
			Accounts: map[string]email.EmailAccount{
				"work": {
					SMTPHost: "smtp.example.com",
					Username: "user@example.com",
					From:     "user@example.com",
				},
			},
		}
		if err := cfg.Validate(); err == nil {
			t.Error("expected validation to fail for missing IMAP host")
		}
	})

	t.Run("invalid port range", func(t *testing.T) {
		cfg := &email.Config{
			Accounts: map[string]email.EmailAccount{
				"work": {
					IMAPHost: "imap.example.com",
					IMAPPort: 99999,
					SMTPHost: "smtp.example.com",
					Username: "user@example.com",
					From:     "user@example.com",
				},
			},
		}
		if err := cfg.Validate(); err == nil {
			t.Error("expected validation to fail for out-of-range IMAP port")
		}
	})

	t.Run("zero port allowed as default sentinel", func(t *testing.T) {
		cfg := &email.Config{
			Default: "work",
			Accounts: map[string]email.EmailAccount{
				"work": {
					IMAPHost: "imap.example.com",
					IMAPPort: 0,
					SMTPHost: "smtp.example.com",
					SMTPPort: 0,
					Username: "user@example.com",
					From:     "user@example.com",
				},
			},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected validation to pass with zero ports, got: %v", err)
		}
	})
}

func TestConfigValueBoundaries(t *testing.T) {
	for _, value := range []string{"not-json", "[]", `"text"`, `{"accounts":[]}`} {
		if err := email.ValidateConfigValue(value); err == nil || err.Error() != "invalid EMAIL_CONFIG: malformed JSON" {
			t.Errorf("ValidateConfigValue(%q) = %v, want historical malformed JSON error", value, err)
		}
	}

	if err := email.ValidateConfigValue(`{"accounts":{"work":{"imap_host":"imap.example.com","smtp_host":"smtp.example.com","username":"u","from":"u@example.com"}}}`); err != nil {
		t.Fatalf("valid EMAIL_CONFIG rejected: %v", err)
	}
	if err := email.ValidateConfigValue(`{"accounts":{"work":{}}}`); err == nil || !strings.HasPrefix(err.Error(), "invalid email config: ") {
		t.Fatalf("invalid EMAIL_CONFIG error = %v, want validation envelope", err)
	}
}

func TestConfigAvailable(t *testing.T) {
	ctx := context.Background()
	available, err := email.ConfigAvailable(ctx, "u1", nil)
	if err != nil || !available {
		t.Fatalf("nil metadata reader: available=%v err=%v", available, err)
	}
	available, err = email.ConfigAvailable(ctx, "u1", func(context.Context, string) error { return pgx.ErrNoRows })
	if err != nil || available {
		t.Fatalf("missing metadata: available=%v err=%v", available, err)
	}
	lookupErr := errors.New("db down")
	available, err = email.ConfigAvailable(ctx, "u1", func(context.Context, string) error { return lookupErr })
	if available || !errors.Is(err, lookupErr) {
		t.Fatalf("metadata failure: available=%v err=%v", available, err)
	}
}
