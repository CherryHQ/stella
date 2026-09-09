package email

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgemail "github.com/CherryHQ/stella/pkg/email"
)

const noEmailConfigMessage = "no email account configured — ask the user to add one under Settings → Email"

const duplicateSuppressedStatus = "prior attempt recorded (delivery outcome may be unknown; duplicate suppressed)"

type Queries interface {
	DeleteExpiredEmailSendDedup(context.Context) error
	CreateEmailSendDedup(context.Context, sqlc.CreateEmailSendDedupParams) (sqlc.EmailSendDedup, error)
	GetEmailSendDedup(context.Context, sqlc.GetEmailSendDedupParams) (sqlc.EmailSendDedup, error)
}

// Option configures trusted host integration without making the replaceable
// email plugin depend on internal runtime packages.
type Option func(*Service)

// WithOwnershipFence installs the host's ownership check and transaction
// validator. The service keeps the dedup insert and validation in one
// transaction; an unconfigured service retains its direct query path for
// ordinary non-agent callers and tests.
func WithOwnershipFence(check func(context.Context) error, validateTx func(context.Context, pgx.Tx) error) Option {
	return func(s *Service) {
		s.ownershipCheck = check
		s.validateTx = validateTx
	}
}

var _ pkgemail.Service = (*Service)(nil)

var (
	errServiceUnavailable       = errors.New("email service is unavailable — try again later")
	errAuthorizationUnavailable = errors.New("email authorization is unavailable — try again later")
	errMissingUser              = errors.New("email access requires an authenticated user")
)

type Service struct {
	resolveUser    ResolveUser
	configReader   ConfigReader
	q              Queries
	db             *pgxpool.Pool
	sendFunc       func(EmailAccount, SendOptions) error
	ownershipCheck func(context.Context) error
	validateTx     func(context.Context, pgx.Tx) error
}

func NewService(resolveUser ResolveUser, configReader ConfigReader, q Queries, options ...Option) *Service {
	service := &Service{resolveUser: resolveUser, configReader: configReader, q: q, sendFunc: Send}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

// NewServiceForPool creates an email service that owns the sqlc query set for
// the email tables, so callers pass only the pgx pool.
func NewServiceForPool(resolveUser ResolveUser, configReader ConfigReader, pool *pgxpool.Pool, options ...Option) *Service {
	service := NewService(resolveUser, configReader, sqlc.New(pool), options...)
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
func (s *Service) send(ctx context.Context, userID, account string, opts SendOptions, idempotencyKey string) (pkgemail.SendResult, error) {
	if strings.TrimSpace(idempotencyKey) == "" {
		return pkgemail.SendResult{}, fmt.Errorf("idempotency_key is required — generate a stable unique key for this send and retry")
	}
	acct, err := s.loadAccount(ctx, userID, account)
	if err != nil {
		return pkgemail.SendResult{}, err
	}
	if s.q == nil {
		return pkgemail.SendResult{}, fmt.Errorf("email idempotency store is unavailable — try again later")
	}
	createDedup := func(q Queries) error {
		_ = q.DeleteExpiredEmailSendDedup(ctx)
		_, createErr := q.CreateEmailSendDedup(ctx, sqlc.CreateEmailSendDedupParams{UserID: userID, IdempotencyKey: idempotencyKey})
		return createErr
	}
	err = s.writeDedup(ctx, createDedup)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pkgemail.SendResult{Status: duplicateSuppressedStatus, Duplicate: true}, nil
		}
		return pkgemail.SendResult{}, err
	}
	// The durable dedupe row suppresses retries within the existing 24-hour window;
	// validate ownership immediately before crossing the SMTP boundary.
	if err := s.checkOwnership(ctx); err != nil {
		return pkgemail.SendResult{}, err
	}
	if err := s.sendFunc(acct, opts); err != nil {
		// SMTP errors after Send starts are outcome-unknown: the remote server
		// may already have accepted the message. Keep the durable dedupe row for
		// its 24-hour lifetime; callers must not infer non-delivery from expiry.
		return pkgemail.SendResult{}, fmt.Errorf("email delivery outcome is unknown; duplicate retry suppressed: %w", err)
	}
	return pkgemail.SendResult{Status: "sent"}, nil
}

func (s *Service) checkOwnership(ctx context.Context) error {
	if s == nil || s.ownershipCheck == nil {
		return nil
	}
	return s.ownershipCheck(ctx)
}

func (s *Service) writeDedup(ctx context.Context, write func(Queries) error) error {
	if s.validateTx == nil {
		return write(s.q)
	}
	if s.db == nil {
		return fmt.Errorf("email ownership transaction database is not configured")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.validateTx(ctx, tx); err != nil {
		return err
	}
	if err := write(sqlc.New(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
		return nil, errMissingUser
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
