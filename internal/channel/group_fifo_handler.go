package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// HandleFIFO executes one group responder admitted by the durable channel
// FIFO. The FIFO claim is the only execution lease. ctx_group_dispatch is
// materialized under the same item ID as an accept/publish ledger, then
// claimed only to promote that ledger to running without starting a second
// model execution path. The caller owns completion: it passes the
// durableCompletionProxy that captures the turn's egress outcome and terminalizes
// the source AgentRun after the FIFO item settles.
func (d *GroupDispatcher) HandleFIFO(ctx context.Context, item sqlc.ChannelFifoItem, completion *durableCompletionProxy) error {
	if d == nil || d.q == nil {
		return errors.New("group dispatcher is not configured")
	}
	if item.Command != "group_responder" {
		return fmt.Errorf("unsupported group FIFO command %q", item.Command)
	}
	var payload groupResponderPayload
	if err := json.Unmarshal(item.Payload, &payload); err != nil {
		return fmt.Errorf("decode group responder FIFO payload: %w", err)
	}
	if err := validateGroupResponderPayload(payload, item.Args); err != nil {
		return err
	}

	ctx, _ = startIngress(ctx, "channel.ingress",
		attribute.String("stella.channel.name", "group"),
		attribute.String("stella.channel.dispatch_id", item.ID),
		attribute.String("stella.channel.group_id", payload.GroupID),
		attribute.String("stella.agent_id", payload.AgentID),
		attribute.String("stella.channel.dispatch_kind", payload.Kind),
	)
	defer finishIngress(ctx)

	attempt := max(int64(item.Attempt), 1)
	row, err := d.q.EnsureChannelGroupDispatchLedger(ctx, sqlc.EnsureChannelGroupDispatchLedgerParams{
		ID:             item.ID,
		GroupMessageID: payload.MessageID,
		GroupID:        payload.GroupID,
		AgentID:        payload.AgentID,
		ReplyChannelID: payload.ChannelID,
		AttemptCount:   attempt,
		TriggerSeq:     payload.Seq,
		Kind:           payload.Kind,
	})
	if err != nil {
		return fmt.Errorf("ensure group FIFO dispatch ledger: %w", err)
	}
	if terminalGroupDispatchStatus(row.Status) {
		return nil
	}
	claimed, err := d.q.ClaimChannelGroupDispatchLedger(ctx, sqlc.ClaimChannelGroupDispatchLedgerParams{
		ID: item.ID, AttemptCount: attempt,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// A concurrent retry may have terminalized the ledger between Ensure and
		// Claim. Treat that as already completed; the FIFO remains the source of
		// truth for whether the item itself can be released.
		row, getErr := d.q.GetGroupDispatch(ctx, item.ID)
		if getErr == nil && terminalGroupDispatchStatus(row.Status) {
			return nil
		}
		if getErr != nil {
			return fmt.Errorf("read group FIFO dispatch ledger after claim: %w", getErr)
		}
		return errors.New("group FIFO dispatch ledger claim lost")
	}
	if err != nil {
		return fmt.Errorf("claim group FIFO dispatch ledger: %w", err)
	}
	if terminalGroupDispatchStatus(claimed.Status) {
		return nil
	}
	if err := validateGroupResponderLedger(claimed, payload); err != nil {
		return d.failFIFODispatch(ctx, claimed, err)
	}
	return d.executeClaimedDispatch(ctx, claimed, true, payload.Reason, completion)
}

func validateGroupResponderPayload(payload groupResponderPayload, args string) error {
	if payload.Kind != groupRouteActionWake && payload.Kind != groupRouteActionNudge {
		return fmt.Errorf("invalid group responder kind %q", payload.Kind)
	}
	if args != "" && args != payload.Kind {
		return fmt.Errorf("group responder FIFO args %q disagree with payload kind %q", args, payload.Kind)
	}
	for name, value := range map[string]string{
		"route id": payload.RouteID, "message id": payload.MessageID,
		"group id": payload.GroupID, "agent id": payload.AgentID,
		"channel id": payload.ChannelID,
	} {
		if value == "" {
			return fmt.Errorf("group responder %s is required", name)
		}
	}
	if payload.Seq <= 0 {
		return errors.New("group responder sequence must be positive")
	}
	return nil
}

func validateGroupResponderLedger(row sqlc.CtxGroupDispatch, payload groupResponderPayload) error {
	checks := []struct {
		name, got, want string
	}{
		{"group message", row.GroupMessageID, payload.MessageID},
		{"group", row.GroupID, payload.GroupID},
		{"agent", row.AgentID, payload.AgentID},
		{"reply channel", row.ReplyChannelID, payload.ChannelID},
		{"kind", row.Kind, payload.Kind},
	}
	for _, check := range checks {
		if check.got != check.want {
			return fmt.Errorf("group responder %s changed: ledger=%q payload=%q", check.name, check.got, check.want)
		}
	}
	if row.TriggerSeq != payload.Seq {
		return fmt.Errorf("group responder sequence changed: ledger=%d payload=%d", row.TriggerSeq, payload.Seq)
	}
	return nil
}

func terminalGroupDispatchStatus(status string) bool {
	switch status {
	case "completed", "failed", "silent", "held":
		return true
	default:
		return false
	}
}
