package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const noEmailConfigMessage = "no email account configured — ask the user to add one under Settings → Email"

type Queries interface {
	DeleteExpiredEmailSendDedup(context.Context) error
	CreateEmailSendDedup(context.Context, sqlc.CreateEmailSendDedupParams) (sqlc.EmailSendDedup, error)
	GetEmailSendDedup(context.Context, sqlc.GetEmailSendDedupParams) (sqlc.EmailSendDedup, error)
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
	db       *pgxpool.Pool
	sendFunc func(EmailAccount, SendOptions) error
}

func NewService(vaultSvc *vault.Service, q Queries) *Service {
	return &Service{vaultSvc: vaultSvc, q: q, sendFunc: Send}
}

// NewServiceForPool creates an email service that owns the sqlc query set for
// the email tables, so callers pass only the pgx pool.
func NewServiceForPool(vaultSvc *vault.Service, pool *pgxpool.Pool) *Service {
	service := NewService(vaultSvc, sqlc.New(pool))
	service.db = pool
	return service
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
	createDedup := func(q Queries) error {
		_ = q.DeleteExpiredEmailSendDedup(ctx)
		_, createErr := q.CreateEmailSendDedup(ctx, sqlc.CreateEmailSendDedupParams{UserID: userID, IdempotencyKey: idempotencyKey})
		return createErr
	}
	if _, guarded := agentrun.GuardFromContext(ctx); guarded {
		if s.db == nil {
			return SendResult{}, fmt.Errorf("email AgentRun fencing is unavailable — try again later")
		}
		err = agentrun.WriteTx(ctx, s.db, func(q *sqlc.Queries) error { return createDedup(q) })
	} else {
		err = createDedup(s.q)
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SendResult{Status: "already sent (duplicate suppressed)", Duplicate: true}, nil
		}
		return SendResult{}, err
	}
	// The durable dedupe row makes this one-attempt on an ambiguous outcome;
	// validate ownership immediately before crossing the SMTP boundary.
	if err := agentrun.Check(ctx); err != nil {
		return SendResult{}, err
	}
	if err := s.sendFunc(acct, opts); err != nil {
		// SMTP errors after Send starts are outcome-unknown: the remote server
		// may already have accepted the message. Keep the durable dedupe row so
		// this logical send is never made transparently retryable.
		return SendResult{}, fmt.Errorf("email delivery outcome is unknown; duplicate retry suppressed: %w", err)
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
	if s == nil || s.vaultSvc == nil {
		return nil, fmt.Errorf("vault not configured")
	}
	value, err := s.vaultSvc.Get(ctx, userID, "EMAIL_CONFIG")
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
