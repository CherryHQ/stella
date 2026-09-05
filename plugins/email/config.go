package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"

	"github.com/jackc/pgx/v5"

	pkgemail "github.com/CherryHQ/stella/pkg/email"
)

const ConfigName = pkgemail.ConfigName

// ConfigReader is the smallest storage seam Email needs: read this user's
// EMAIL_CONFIG value. The nil function preserves the old unconfigured-service
// behavior without putting a typed nil vault service in an interface.
type ConfigReader func(context.Context, string) (string, error)

// ConfigAvailable maps the vault metadata lookup used by tool visibility to
// Email's user-facing availability fact. The caller supplies only the
// user-scoped presence check, so Email owns the sentinel/error semantics.
func ConfigAvailable(ctx context.Context, userID string, readMeta func(context.Context, string) error) (bool, error) {
	if readMeta == nil {
		return true, nil
	}
	err := readMeta(ctx, userID)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, pgx.ErrNoRows):
		return false, nil
	default:
		return false, fmt.Errorf("read email config: %w", err)
	}
}

var accountNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// EmailAccount stores one user's IMAP/SMTP credentials and endpoints.
type EmailAccount struct {
	IMAPHost string `json:"imap_host,omitempty"`
	IMAPPort int    `json:"imap_port,omitempty"`
	IMAPTLS  string `json:"imap_tls,omitempty"`
	SMTPHost string `json:"smtp_host,omitempty"`
	SMTPPort int    `json:"smtp_port,omitempty"`
	SMTPTLS  string `json:"smtp_tls,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	From     string `json:"from,omitempty"`
}

type Config struct {
	Default  string                  `json:"default"`
	Accounts map[string]EmailAccount `json:"accounts"`
}

func ValidateAccountName(name string) error {
	if !accountNameRe.MatchString(name) {
		return fmt.Errorf("account name %q must match ^[a-z][a-z0-9_]{0,31}$", name)
	}
	return nil
}

func (c *Config) Validate() error {
	if c.Default != "" {
		if _, ok := c.Accounts[c.Default]; !ok {
			return fmt.Errorf("default account %q not found in accounts", c.Default)
		}
	}
	for name, acct := range c.Accounts {
		if err := ValidateAccountName(name); err != nil {
			return err
		}
		if acct.IMAPHost == "" {
			return fmt.Errorf("account %q: imap_host is required", name)
		}
		if acct.SMTPHost == "" {
			return fmt.Errorf("account %q: smtp_host is required", name)
		}
		if acct.Username == "" {
			return fmt.Errorf("account %q: username is required", name)
		}
		if acct.From == "" {
			return fmt.Errorf("account %q: from is required", name)
		}
		if acct.IMAPPort != 0 && (acct.IMAPPort < 1 || acct.IMAPPort > 65535) {
			return fmt.Errorf("account %q: imap_port must be between 1 and 65535", name)
		}
		if acct.SMTPPort != 0 && (acct.SMTPPort < 1 || acct.SMTPPort > 65535) {
			return fmt.Errorf("account %q: smtp_port must be between 1 and 65535", name)
		}
	}
	return nil
}

func LoadFromEnv() (*Config, error) {
	val, ok := os.LookupEnv("EMAIL_CONFIG")
	if !ok {
		return nil, fmt.Errorf("%s environment variable is not set", ConfigName)
	}
	cfg := &Config{Accounts: make(map[string]EmailAccount)}
	if val == "" || val == "{}" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(val), cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", ConfigName, err)
	}
	if cfg.Accounts == nil {
		cfg.Accounts = make(map[string]EmailAccount)
	}
	return cfg, nil
}

// ParseConfigValue keeps the read path deliberately permissive. Vault writes
// validate required fields, while an existing value is only decoded here so
// its historical missing-account and malformed-value errors remain unchanged.
func parseConfigValue(value string) (*Config, error) {
	cfg := &Config{Accounts: make(map[string]EmailAccount)}
	if value != "" && value != "{}" {
		if err := json.Unmarshal([]byte(value), cfg); err != nil {
			return nil, fmt.Errorf("malformed EMAIL_CONFIG in vault")
		}
	}
	if cfg.Accounts == nil {
		cfg.Accounts = make(map[string]EmailAccount)
	}
	return cfg, nil
}

// ValidateConfigValue is the strict write-time policy for the EMAIL_CONFIG
// vault entry, including the historical endpoint error envelope.
func ValidateConfigValue(value string) error {
	var cfg Config
	if err := json.Unmarshal([]byte(value), &cfg); err != nil {
		return fmt.Errorf("invalid EMAIL_CONFIG: malformed JSON")
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid email config: %w", err)
	}
	return nil
}

func (c *Config) Resolve(name string) (EmailAccount, error) {
	if name == "" {
		if c.Default == "" {
			return EmailAccount{}, errors.New("no default account set")
		}
		name = c.Default
	}
	acct, ok := c.Accounts[name]
	if !ok {
		return EmailAccount{}, fmt.Errorf("account %q not found", name)
	}
	if acct.IMAPPort == 0 {
		acct.IMAPPort = 993
	}
	if acct.SMTPPort == 0 {
		acct.SMTPPort = 587
	}
	if acct.IMAPTLS == "" {
		acct.IMAPTLS = "ssl"
	}
	if acct.SMTPTLS == "" {
		acct.SMTPTLS = "starttls"
	}
	return acct, nil
}

func (c *Config) Upsert(name string, partial EmailAccount) {
	if c.Accounts == nil {
		c.Accounts = make(map[string]EmailAccount)
	}
	existing := c.Accounts[name]
	if partial.IMAPHost != "" {
		existing.IMAPHost = partial.IMAPHost
	}
	if partial.IMAPPort != 0 {
		existing.IMAPPort = partial.IMAPPort
	}
	if partial.IMAPTLS != "" {
		existing.IMAPTLS = partial.IMAPTLS
	}
	if partial.SMTPHost != "" {
		existing.SMTPHost = partial.SMTPHost
	}
	if partial.SMTPPort != 0 {
		existing.SMTPPort = partial.SMTPPort
	}
	if partial.SMTPTLS != "" {
		existing.SMTPTLS = partial.SMTPTLS
	}
	if partial.Username != "" {
		existing.Username = partial.Username
	}
	if partial.Password != "" {
		existing.Password = partial.Password
	}
	if partial.From != "" {
		existing.From = partial.From
	}
	c.Accounts[name] = existing
}

func (c *Config) Remove(name string) error {
	if _, ok := c.Accounts[name]; !ok {
		return fmt.Errorf("account %q not found", name)
	}
	delete(c.Accounts, name)
	if c.Default == name {
		c.Default = ""
	}
	return nil
}

func (c *Config) SetDefault(name string) error {
	if _, ok := c.Accounts[name]; !ok {
		return fmt.Errorf("account %q not found", name)
	}
	c.Default = name
	return nil
}

func (c *Config) AccountNames() []string {
	return slices.Sorted(maps.Keys(c.Accounts))
}
