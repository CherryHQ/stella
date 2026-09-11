package outbox

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// retryBackoff is the fixed retry schedule for retryable send failures until
// a platform-supplied hint (e.g. Telegram retry_after) overrides it.
const retryBackoff = 10 * time.Second

// ProcessDue claims due operations for one channel and sends them through
// sender, recording each outcome fenced by its attempt token. Claim and
// complete are separate short transactions so no DB lock is held across the
// external call. Returns the number of ops attempted.
func (s *Store) ProcessDue(ctx context.Context, channelID, ownerToken string, sender pkgchannel.OperationSender) (int, error) {
	claimed, err := s.claimDue(ctx, channelID, ownerToken)
	if err != nil {
		return 0, err
	}
	attempted := 0
	for _, c := range claimed {
		// Re-admit before every external call: the lease may have moved to
		// another replica between the claim batch and this op. A fenced-out
		// owner leaves the op claimed-but-unsent for the reaper to expire to
		// 'unknown', which is safer than a stale-owner send.
		if ownerToken != "" {
			ok, err := sqlc.New(s.db).ChannelSendAdmission(ctx, sqlc.ChannelSendAdmissionParams{
				ID:           channelID,
				RuntimeToken: pgtype.Text{String: ownerToken, Valid: true},
			})
			if err != nil {
				return attempted, err
			}
			if !ok {
				break
			}
		}
		// Draft ops are fenced twice before the platform call: the run must
		// still be live and no newer snapshot may have been sent already.
		// Either check failing cancels the op without a send — a stale edit
		// must never overwrite the terminal reply.
		if c.op.Kind == OpDraftUpdate {
			if superseded, err := s.draftSuperseded(ctx, c.row.RunID.String, c.op); err != nil {
				return attempted, err
			} else if superseded {
				attempted++
				if _, err := s.complete(ctx, c.row.ID, c.attemptToken, Outcome{State: StateCanceled, ErrorCode: "superseded"}); err != nil {
					slog.WarnContext(ctx, "outbox complete failed", "op", c.row.ID, "error", err)
				}
				continue
			}
		}
		// notify ops carry no triggering account — the channel's current bot
		// identity is always the right sender, so skip the account fence.
		if checker, ok := sender.(pkgchannel.AccountChecker); ok && c.op.Kind != OpNotify && c.op.SourceAccountKey != "" && !checker.OwnsAccount(c.op.SourceAccountKey) {
			// The channel now speaks for a different bot — this op's reply
			// must not go out under the new account.
			attempted++
			if _, err := s.complete(ctx, c.row.ID, c.attemptToken, Outcome{State: StateFailed, ErrorCode: ErrCodeAccountMismatch}); err != nil {
				slog.WarnContext(ctx, "outbox complete failed", "op", c.row.ID, "error", err)
			}
			continue
		}
		attempted++
		var res pkgchannel.SendResult
		var sendErr error
		if c.op.Kind == OpDraftUpdate {
			if ds, ok := sender.(pkgchannel.DraftSender); ok {
				res, sendErr = ds.SendDraftUpdate(ctx, c.op)
			} else {
				// The producer only enqueues draft ops for draft-capable
				// adapters; reaching this is a configuration swap mid-run.
				sendErr = pkgchannel.SendErrorf(pkgchannel.SendPermanent, "draft_update unsupported by adapter")
			}
		} else {
			res, sendErr = sender.SendOperation(ctx, c.op)
		}
		outcome := classifyOutcome(res, sendErr)
		if _, err := s.complete(ctx, c.row.ID, c.attemptToken, outcome); err != nil {
			// The completion write is fenced: a stale attempt loses silently,
			// and a lost commit leaves the attempt for the reaper to expire.
			slog.WarnContext(ctx, "outbox complete failed", "op", c.row.ID, "error", err)
		}
	}
	return attempted, nil
}

// draftSuperseded fences a claimed draft op: the run must still be live
// (a terminal run's reply owns the message now) and the op's snapshot must
// be newer than anything already sent (a requeued 'unknown' can never land
// an older version over a newer one).
func (s *Store) draftSuperseded(ctx context.Context, runID string, op pkgchannel.OutboundOp) (bool, error) {
	sent, err := sqlc.New(s.db).MaxSentDraftSeq(ctx, op.DeliveryKey)
	if err != nil {
		return false, err
	}
	if snapshotSuperseded(op.Payload, sent) {
		return true, nil
	}
	run, err := sqlc.New(s.db).GetAgentRun(ctx, runID)
	if err != nil {
		return false, err
	}
	return run.State != "queued" && run.State != "running", nil
}

// snapshotSuperseded reports whether a draft snapshot can no longer add
// anything: undecodable, or at or below the newest version already sent.
func snapshotSuperseded(raw []byte, sentSeq int64) bool {
	var payload pkgchannel.DraftUpdatePayload
	return json.Unmarshal(raw, &payload) != nil || payload.Seq <= sentSeq
}

// admitForSend verifies inside the claim transaction that ownerToken still
// holds the channel lease; legacy mode (empty token) skips the check.
func admitForSend(ctx context.Context, tx pgx.Tx, channelID, ownerToken string) (bool, error) {
	if ownerToken == "" {
		return true, nil
	}
	return sqlc.New(tx).ChannelSendAdmission(ctx, sqlc.ChannelSendAdmissionParams{
		ID:           channelID,
		RuntimeToken: pgtype.Text{String: ownerToken, Valid: true},
	})
}

type claimedOp struct {
	row          sqlc.ChannelOutbox
	attemptToken string
	op           pkgchannel.OutboundOp
}

func (s *Store) claimDue(ctx context.Context, channelID, ownerToken string) ([]claimedOp, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if ok, err := admitForSend(ctx, tx, channelID, ownerToken); err != nil {
		return nil, err
	} else if !ok {
		return nil, nil
	}
	rows, err := s.ListDue(ctx, tx, channelID)
	if err != nil {
		return nil, err
	}
	claimed := make([]claimedOp, 0, len(rows))
	for _, row := range rows {
		token, ok, err := s.ClaimAttempt(ctx, tx, row.ID, ownerToken)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		op, err := decodeOp(row)
		if err != nil {
			// An undecodable op is a permanent failure — it can never be sent
			// correctly and must not block the channel.
			if _, cerr := s.CompleteAttempt(ctx, tx, row.ID, token, Outcome{State: StateFailed, ErrorCode: ErrCodePermanent}); cerr != nil {
				return nil, cerr
			}
			continue
		}
		// Hand the adapter the run's stable draft identity so create/edit
		// operations and the terminal reply all target one platform message.
		if row.RunID.Valid && (op.Kind == OpDraftUpdate || op.Kind == OpSendReply) {
			if msgID, derr := sqlc.New(tx).LatestSentDraftMessageID(ctx, row.RunID); derr == nil {
				op.DraftMessageID = msgID
			}
		}
		claimed = append(claimed, claimedOp{row: row, attemptToken: token, op: op})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return claimed, nil
}

func (s *Store) complete(ctx context.Context, id, attemptToken string, o Outcome) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ok, err := s.CompleteAttempt(ctx, tx, id, attemptToken, o)
	if err != nil {
		return false, err
	}
	return ok, tx.Commit(ctx)
}

func decodeOp(row sqlc.ChannelOutbox) (pkgchannel.OutboundOp, error) {
	var addr struct {
		ChatKey    string `json:"chat_key"`
		ThreadKey  string `json:"thread_key"`
		ReplyToKey string `json:"reply_to_key"`
		Scope      string `json:"scope"`
		Token      string `json:"token"`
	}
	if err := json.Unmarshal(row.Address, &addr); err != nil {
		return pkgchannel.OutboundOp{}, err
	}
	return pkgchannel.OutboundOp{
		Kind:             row.OperationKind,
		DeliveryKey:      row.DeliveryKey,
		OperationIndex:   int(row.OperationIndex),
		SourceAccountKey: row.SourceAccountKey,
		Address: pkgchannel.OutboundAddress{
			ChatKey:    addr.ChatKey,
			ThreadKey:  addr.ThreadKey,
			ReplyToKey: addr.ReplyToKey,
			Scope:      addr.Scope,
			Token:      addr.Token,
		},
		Payload: row.Payload,
	}, nil
}

// classifyOutcome maps the adapter's result to the ledger outcome. Only a
// platform-confirmed receipt marks sent; retryable failures re-schedule,
// permanent failures stop, and unknown holds for a probe — never a resend.
func classifyOutcome(res pkgchannel.SendResult, err error) Outcome {
	if err == nil {
		return Outcome{State: StateSent, PlatformMessageID: res.PlatformMessageID}
	}
	switch pkgchannel.SendClassify(err) {
	case pkgchannel.SendRetryable:
		next := time.Now().UTC().Add(retryBackoff)
		return Outcome{State: StatePending, ErrorCode: "retryable", NextAttemptAt: &next}
	case pkgchannel.SendPermanent:
		return Outcome{State: StateFailed, ErrorCode: ErrCodePermanent}
	default:
		return Outcome{State: StateUnknown, ErrorCode: ErrCodeUnknown}
	}
}
