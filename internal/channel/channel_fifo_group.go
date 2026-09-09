package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// ErrChannelFIFOQuotaExceeded means durable admission could not reserve the
// binding, principal, or deployment queue budget. The transaction is rolled
// back, so callers can surface this as backpressure without leaving a partial
// route or a side queue row behind.
var ErrChannelFIFOQuotaExceeded = errors.New("channel fifo quota exceeded")

// GroupResponderEnqueue is the durable handoff from classification to the
// channel FIFO. The caller must pass a Queries value bound to the same
// transaction that creates/completes the GroupRoute row.
type GroupResponderEnqueue struct {
	RouteID   string
	MessageID string
	GroupID   string
	AgentID   string
	ChannelID string
	Platform  string
	ChatKey   string
	ThreadKey string
	Seq       int64
	Kind      string
	Decision  string
	Reason    string
}

type groupResponderPayload struct {
	Kind      string `json:"kind"`
	RouteID   string `json:"route_id"`
	MessageID string `json:"message_id"`
	GroupID   string `json:"group_id"`
	AgentID   string `json:"agent_id"`
	ChannelID string `json:"channel_id"`
	Seq       int64  `json:"seq"`
	Decision  string `json:"decision"`
	Reason    string `json:"reason,omitempty"`
}

const (
	channelDeploymentQuotaKey     = "deployment"
	channelBindingDefaultRows     = int64(1000)
	channelBindingDefaultBytes    = int64(64 << 20)
	channelPrincipalDefaultRows   = int64(10000)
	channelPrincipalDefaultBytes  = int64(512 << 20)
	channelDeploymentDefaultRows  = int64(100000)
	channelDeploymentDefaultBytes = int64(8 << 30)
)

// EnqueueGroupResponderTx atomically admits one classified group responder to
// the durable FIFO. Stable route/agent identity is checked while the
// binding is locked, quotas are reserved in deployment -> principal ->
// binding order, and the item is inserted before the caller commits its route
// CAS. A duplicate returns the existing item with inserted=false.
func EnqueueGroupResponderTx(ctx context.Context, q *sqlc.Queries, in GroupResponderEnqueue) (sqlc.ChannelFifoItem, bool, error) {
	if q == nil {
		return sqlc.ChannelFifoItem{}, false, errors.New("nil durable FIFO queries")
	}
	for name, value := range map[string]string{
		"route id": in.RouteID, "message id": in.MessageID, "group id": in.GroupID,
		"agent id": in.AgentID, "channel id": in.ChannelID, "platform": in.Platform,
		"chat key": in.ChatKey,
	} {
		if strings.TrimSpace(value) == "" {
			return sqlc.ChannelFifoItem{}, false, fmt.Errorf("group responder %s is required", name)
		}
	}
	if in.Seq <= 0 {
		return sqlc.ChannelFifoItem{}, false, errors.New("group responder sequence must be positive")
	}
	if in.Kind != groupRouteActionWake && in.Kind != groupRouteActionNudge {
		return sqlc.ChannelFifoItem{}, false, fmt.Errorf("invalid group responder kind %q", in.Kind)
	}

	payload, err := json.Marshal(groupResponderPayload{
		Kind: in.Kind, RouteID: in.RouteID, MessageID: in.MessageID,
		GroupID: in.GroupID, AgentID: in.AgentID, ChannelID: in.ChannelID,
		Seq: in.Seq, Decision: in.Decision, Reason: in.Reason,
	})
	if err != nil {
		return sqlc.ChannelFifoItem{}, false, fmt.Errorf("encode group responder payload: %w", err)
	}
	payloadBytes := int64(len(payload))
	if payloadBytes <= 0 {
		return sqlc.ChannelFifoItem{}, false, errors.New("empty group responder payload")
	}

	if err := q.EnsureChannelBinding(ctx, sqlc.EnsureChannelBindingParams{
		ID: uuid.Must(uuid.NewV7()).String(), ChannelID: in.ChannelID,
		Platform: in.Platform, ChatKey: in.ChatKey, ThreadKey: in.ThreadKey,
		PrincipalKind: "group", PrincipalID: in.GroupID, AgentID: in.AgentID,
	}); err != nil {
		return sqlc.ChannelFifoItem{}, false, fmt.Errorf("ensure group responder binding: %w", err)
	}
	binding, err := q.GetChannelBindingByKey(ctx, sqlc.GetChannelBindingByKeyParams{
		ChannelID: in.ChannelID, Platform: in.Platform, ChatKey: in.ChatKey, ThreadKey: in.ThreadKey,
	})
	if err != nil {
		return sqlc.ChannelFifoItem{}, false, fmt.Errorf("get group responder binding: %w", err)
	}
	if binding.PrincipalKind != "group" || binding.PrincipalID != in.GroupID || binding.AgentID != in.AgentID {
		return sqlc.ChannelFifoItem{}, false, errors.New("group responder binding identity changed")
	}

	principalKey := "group:" + in.GroupID
	if _, err := q.EnsureChannelDeploymentQuota(ctx, sqlc.EnsureChannelDeploymentQuotaParams{
		ChannelID: channelDeploymentQuotaKey, MaxRows: channelDeploymentDefaultRows, MaxBytes: channelDeploymentDefaultBytes,
	}); err != nil {
		return sqlc.ChannelFifoItem{}, false, fmt.Errorf("ensure channel deployment quota: %w", err)
	}
	if _, err := q.EnsureChannelPrincipalQuota(ctx, sqlc.EnsureChannelPrincipalQuotaParams{
		PrincipalKey: principalKey, PrincipalKind: "group", PrincipalID: in.GroupID,
		MaxRows: channelPrincipalDefaultRows, MaxBytes: channelPrincipalDefaultBytes,
	}); err != nil {
		return sqlc.ChannelFifoItem{}, false, fmt.Errorf("ensure group principal quota: %w", err)
	}
	// Lock order is part of the admission contract. Keep it aligned with
	// ordinary ingress so two workers cannot deadlock on shared quotas.
	if _, err := q.GetChannelDeploymentQuotaForUpdate(ctx, channelDeploymentQuotaKey); err != nil {
		return sqlc.ChannelFifoItem{}, false, fmt.Errorf("lock channel deployment quota: %w", err)
	}
	if _, err := q.GetChannelPrincipalQuotaForUpdate(ctx, principalKey); err != nil {
		return sqlc.ChannelFifoItem{}, false, fmt.Errorf("lock group principal quota: %w", err)
	}
	binding, err = q.GetChannelBindingForUpdate(ctx, binding.ID)
	if err != nil {
		return sqlc.ChannelFifoItem{}, false, fmt.Errorf("lock group responder binding: %w", err)
	}
	if binding.State != "active" {
		return sqlc.ChannelFifoItem{}, false, fmt.Errorf("group responder binding is %s", binding.State)
	}

	// Kind is immutable payload, not dedup identity. A route/agent pair can
	// transition from wake to nudge while a retry is in flight, but it must
	// still own exactly one FIFO item.
	sourceKey := "group-route:" + in.RouteID + ":" + in.AgentID
	if existing, err := q.GetChannelFIFOItemBySource(ctx, sqlc.GetChannelFIFOItemBySourceParams{
		BindingID: binding.ID, SourceKey: sourceKey,
	}); err == nil {
		return existing, false, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return sqlc.ChannelFifoItem{}, false, fmt.Errorf("check group responder duplicate: %w", err)
	}

	reserve := func() error {
		if rows, err := q.ReserveChannelDeploymentQuota(ctx, sqlc.ReserveChannelDeploymentQuotaParams{ChannelID: channelDeploymentQuotaKey, ByteCost: payloadBytes}); err != nil {
			return fmt.Errorf("reserve deployment quota: %w", err)
		} else if rows != 1 {
			return ErrChannelFIFOQuotaExceeded
		}
		if rows, err := q.ReserveChannelPrincipalQuota(ctx, sqlc.ReserveChannelPrincipalQuotaParams{PrincipalKey: principalKey, ByteCost: payloadBytes}); err != nil {
			return fmt.Errorf("reserve principal quota: %w", err)
		} else if rows != 1 {
			return ErrChannelFIFOQuotaExceeded
		}
		if rows, err := q.ReserveChannelBindingQuota(ctx, sqlc.ReserveChannelBindingQuotaParams{BindingID: binding.ID, ByteCost: payloadBytes}); err != nil {
			return fmt.Errorf("reserve binding quota: %w", err)
		} else if rows != 1 {
			return ErrChannelFIFOQuotaExceeded
		}
		return nil
	}
	if err := reserve(); err != nil {
		return sqlc.ChannelFifoItem{}, false, err
	}
	seq, err := q.AllocateChannelBindingSeq(ctx, binding.ID)
	if err != nil {
		return sqlc.ChannelFifoItem{}, false, fmt.Errorf("allocate group responder sequence: %w", err)
	}
	item, err := q.InsertChannelFIFOItem(ctx, sqlc.InsertChannelFIFOItemParams{
		ID: uuid.Must(uuid.NewV7()).String(), BindingID: binding.ID, PrincipalKey: principalKey, Seq: int64(seq),
		SourceKey: sourceKey, SchemaVersion: 1, Payload: payload,
		PayloadBytes: payloadBytes, MediaBytes: 0, CapabilityBytes: 0, ByteCost: payloadBytes,
		Command: "group_responder", Args: in.Kind,
		// Group responders bind to the lane revision at execution time. Only a
		// direct /new item carries an expected revision for compare-and-rotate.
		ExpectedSessionID: "", ExpectedBindingRevision: nil,
	})
	if err != nil {
		return sqlc.ChannelFifoItem{}, false, fmt.Errorf("insert group responder FIFO item: %w", err)
	}
	return item, true, nil
}
