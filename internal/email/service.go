package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/toolctx"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const noEmailConfigMessage = "no email account configured — ask the user to add one under Settings → Email or via stella email config"

type Queries interface {
	DeleteExpiredEmailSendDedup(context.Context) error
	CreateEmailSendDedup(context.Context, sqlc.CreateEmailSendDedupParams) (sqlc.EmailSendDedup, error)
	GetEmailSendDedup(context.Context, sqlc.GetEmailSendDedupParams) (sqlc.EmailSendDedup, error)
	DeleteEmailSendDedup(context.Context, sqlc.DeleteEmailSendDedupParams) error
}

type SendResult struct {
	Status    string
	Duplicate bool
}

type AccountList struct {
	Accounts []string
	Default  string
}

type Service struct {
	vaultSvc *vault.Service
	q        Queries
	sendFunc func(EmailAccount, SendOptions) error
}

func NewService(vaultSvc *vault.Service, q Queries) *Service {
	return &Service{vaultSvc: vaultSvc, q: q, sendFunc: Send}
}

func (s *Service) SetSendFunc(fn func(EmailAccount, SendOptions) error) {
	if fn == nil {
		s.sendFunc = Send
		return
	}
	s.sendFunc = fn
}

func (s *Service) AccountsOwned(ctx context.Context, ident toolctx.Identity) (AccountList, error) {
	cfg, err := s.loadConfig(ctx, ident)
	if err != nil {
		return AccountList{}, err
	}
	return AccountList{Accounts: cfg.AccountNames(), Default: cfg.Default}, nil
}

func (s *Service) FoldersOwned(ctx context.Context, ident toolctx.Identity, account string) ([]string, error) {
	acct, err := s.loadAccount(ctx, ident, account)
	if err != nil {
		return nil, err
	}
	return Folders(acct)
}

func (s *Service) ListOwned(ctx context.Context, ident toolctx.Identity, account string, opts ListOptions) ([]Envelope, error) {
	acct, err := s.loadAccount(ctx, ident, account)
	if err != nil {
		return nil, err
	}
	return List(acct, opts)
}

func (s *Service) ReadOwned(ctx context.Context, ident toolctx.Identity, account, folder string, uid uint32) (*Message, error) {
	acct, err := s.loadAccount(ctx, ident, account)
	if err != nil {
		return nil, err
	}
	return Read(acct, folder, uid)
}

func (s *Service) MarkSeenOwned(ctx context.Context, ident toolctx.Identity, account, folder string, uid uint32, seen bool) error {
	acct, err := s.loadAccount(ctx, ident, account)
	if err != nil {
		return err
	}
	return MarkSeen(acct, folder, uid, seen)
}

func (s *Service) SendOwned(ctx context.Context, ident toolctx.Identity, account string, opts SendOptions, idempotencyKey string) (SendResult, error) {
	if strings.TrimSpace(idempotencyKey) == "" {
		return SendResult{}, fmt.Errorf("idempotency_key is required — generate a stable unique key for this send and retry")
	}
	acct, err := s.loadAccount(ctx, ident, account)
	if err != nil {
		return SendResult{}, err
	}
	if s.q == nil {
		return SendResult{}, fmt.Errorf("email idempotency store is unavailable — try again later")
	}
	_ = s.q.DeleteExpiredEmailSendDedup(ctx)
	_, err = s.q.CreateEmailSendDedup(ctx, sqlc.CreateEmailSendDedupParams{UserID: ident.UserID, IdempotencyKey: idempotencyKey})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SendResult{Status: "already sent (duplicate suppressed)", Duplicate: true}, nil
		}
		return SendResult{}, err
	}
	if err := s.sendFunc(acct, opts); err != nil {
		_ = s.q.DeleteEmailSendDedup(ctx, sqlc.DeleteEmailSendDedupParams{UserID: ident.UserID, IdempotencyKey: idempotencyKey})
		return SendResult{}, err
	}
	return SendResult{Status: "sent"}, nil
}

// loadAccount always validates egress: DialPublicTCP re-checks addresses per
// dial, but only ValidateAccountEgress rejects imap_tls/smtp_tls "none", and
// read paths log in over IMAP too — cleartext credentials must be blocked on
// every operation, matching the old HTTP behavior.
func (s *Service) loadAccount(ctx context.Context, ident toolctx.Identity, name string) (EmailAccount, error) {
	cfg, err := s.loadConfig(ctx, ident)
	if err != nil {
		return EmailAccount{}, err
	}
	acct, err := cfg.Resolve(name)
	if err != nil {
		return EmailAccount{}, fmt.Errorf("resolve email account: %w", err)
	}
	if err := ValidateAccountEgress(acct); err != nil {
		return EmailAccount{}, err
	}
	return acct, nil
}

func (s *Service) loadConfig(ctx context.Context, ident toolctx.Identity) (*Config, error) {
	if err := ident.RequireUser(); err != nil {
		return nil, err
	}
	if s == nil || s.vaultSvc == nil {
		return nil, fmt.Errorf("vault not configured")
	}
	value, err := s.vaultSvc.Get(ctx, ident.UserID, "EMAIL_CONFIG")
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New(noEmailConfigMessage)
		}
		return nil, err
	}
	cfg := &Config{Accounts: make(map[string]EmailAccount)}
	if value != "" && value != "{}" {
		if err := json.Unmarshal([]byte(value), cfg); err != nil {
			return nil, fmt.Errorf("malformed EMAIL_CONFIG in vault")
		}
	}
	if cfg.Accounts == nil {
		cfg.Accounts = make(map[string]EmailAccount)
	}
	if len(cfg.Accounts) == 0 {
		return nil, errors.New(noEmailConfigMessage)
	}
	return cfg, nil
}
