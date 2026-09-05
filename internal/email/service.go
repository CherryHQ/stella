package email

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const noEmailConfigMessage = "no email account configured — ask the user to add one under Settings → Email"

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
	configReader ConfigReader
	q            Queries
	sendFunc     func(EmailAccount, SendOptions) error
}

func NewService(configReader ConfigReader, q Queries) *Service {
	return &Service{configReader: configReader, q: q, sendFunc: Send}
}

// NewServiceForPool creates an email service that owns the sqlc query set for
// the email tables, so callers pass only the pgx pool.
func NewServiceForPool(configReader ConfigReader, pool *pgxpool.Pool) *Service {
	return NewService(configReader, sqlc.New(pool))
}

func (s *Service) SetSendFunc(fn func(EmailAccount, SendOptions) error) {
	if fn == nil {
		s.sendFunc = Send
		return
	}
	s.sendFunc = fn
}

// send performs the authorized send. Authorization is decided by the Access PEP
// (Send); this method owns only the durable dedup + egress-validated delivery.
func (s *Service) send(ctx context.Context, userID, account string, opts SendOptions, idempotencyKey string) (SendResult, error) {
	if strings.TrimSpace(idempotencyKey) == "" {
		return SendResult{}, fmt.Errorf("idempotency_key is required — generate a stable unique key for this send and retry")
	}
	acct, err := s.loadAccount(ctx, userID, account)
	if err != nil {
		return SendResult{}, err
	}
	if s.q == nil {
		return SendResult{}, fmt.Errorf("email idempotency store is unavailable — try again later")
	}
	_ = s.q.DeleteExpiredEmailSendDedup(ctx)
	_, err = s.q.CreateEmailSendDedup(ctx, sqlc.CreateEmailSendDedupParams{UserID: userID, IdempotencyKey: idempotencyKey})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SendResult{Status: "already sent (duplicate suppressed)", Duplicate: true}, nil
		}
		return SendResult{}, err
	}
	if err := s.sendFunc(acct, opts); err != nil {
		_ = s.q.DeleteEmailSendDedup(ctx, sqlc.DeleteEmailSendDedupParams{UserID: userID, IdempotencyKey: idempotencyKey})
		return SendResult{}, err
	}
	return SendResult{Status: "sent"}, nil
}

// loadAccount always validates egress: DialPublicTCP re-checks addresses per
// dial, but only ValidateAccountEgress rejects imap_tls/smtp_tls "none", and
// read paths log in over IMAP too — cleartext credentials must be blocked on
// every operation, matching the old HTTP behavior.
func (s *Service) loadAccount(ctx context.Context, userID, name string) (EmailAccount, error) {
	cfg, err := s.loadConfig(ctx, userID)
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

func (s *Service) loadConfig(ctx context.Context, userID string) (*Config, error) {
	if userID == "" {
		return nil, authz.ErrUnauthenticated
	}
	if s == nil || s.configReader == nil {
		return nil, fmt.Errorf("vault not configured")
	}
	value, err := s.configReader(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New(noEmailConfigMessage)
		}
		return nil, err
	}
	cfg, err := parseConfigValue(value)
	if err != nil {
		return nil, err
	}
	if len(cfg.Accounts) == 0 {
		return nil, errors.New(noEmailConfigMessage)
	}
	return cfg, nil
}
