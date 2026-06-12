package channel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	defaultGroupDispatchLease       = 5 * time.Minute
	defaultGroupDispatchPoll        = 2 * time.Second
	defaultGroupDispatchMaxAttempts = int64(3)
)

type dispatchChatFunc func(context.Context, sqlc.CtxGroupDispatch, sqlc.CtxGroupMessage, sqlc.CtxGroupState) (*pkgchannel.ChatStream, error)

// GroupDispatcher materializes durable group response decisions and executes
// one selected-agent dispatch at a time. Ingest owns facts; this owns work.
type GroupDispatcher struct {
	db         *sql.DB
	q          *sqlc.Queries
	coord      *Coordinator
	publishers *PublisherRegistry
	log        *slog.Logger

	leaseDuration time.Duration
	pollInterval  time.Duration
	maxAttempts   int64
	wakeC         chan struct{}
	queue         *sessionQueue
	chat          dispatchChatFunc
}

func NewGroupDispatcher(db *sql.DB, coord *Coordinator, publishers *PublisherRegistry) *GroupDispatcher {
	if publishers == nil && coord != nil {
		publishers = coord.publisherRegistry
	}
	d := &GroupDispatcher{
		db:            db,
		q:             sqlc.New(db),
		coord:         coord,
		publishers:    publishers,
		log:           slog.With("component", "group_dispatcher"),
		leaseDuration: defaultGroupDispatchLease,
		pollInterval:  defaultGroupDispatchPoll,
		maxAttempts:   defaultGroupDispatchMaxAttempts,
		wakeC:         make(chan struct{}, 1),
		queue:         newSessionQueue(),
	}
	d.chat = d.chatDispatch
	return d
}

func (d *GroupDispatcher) Wake() {
	if d == nil {
		return
	}
	select {
	case d.wakeC <- struct{}{}:
	default:
	}
}

func (d *GroupDispatcher) startHeartbeat(ctx context.Context, label, id string, extend func(context.Context, time.Time) (int64, error), onLost func()) func() {
	if d == nil || d.leaseDuration <= 0 || extend == nil {
		return func() {}
	}
	interval := d.leaseDuration / 3
	if interval <= 0 {
		interval = time.Minute
	}
	hctx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-hctx.Done():
				return
			case <-ticker.C:
				until := time.Now().UTC().Add(d.leaseDuration)
				rows, err := extend(hctx, until)
				if err != nil {
					d.log.Warn("group dispatch heartbeat failed", "type", label, "id", id, "error", err)
					continue
				}
				if rows == 0 {
					d.log.Debug("group dispatch heartbeat lost ownership", "type", label, "id", id)
					if onLost != nil {
						onLost()
					}
					return
				}
			}
		}
	}()
	return cancel
}

func (d *GroupDispatcher) Run(ctx context.Context) error {
	if d == nil || d.q == nil {
		return errors.New("group dispatcher not configured")
	}
	ticker := time.NewTicker(d.pollInterval)
	defer ticker.Stop()
	for {
		if err := d.poll(ctx); err != nil {
			d.log.Warn("group dispatch poll failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-d.wakeC:
		case <-ticker.C:
		}
	}
}

func (d *GroupDispatcher) ProcessOutbox(ctx context.Context, outbox sqlc.CtxGroupOutbox) error {
	return d.processOutbox(ctx, outbox, nil)
}

func (d *GroupDispatcher) DispatchSync(ctx context.Context, outbox sqlc.CtxGroupOutbox, publisherOverride GroupPublisher) error {
	return d.processOutbox(ctx, outbox, publisherOverride)
}

func (d *GroupDispatcher) poll(ctx context.Context) error {
	if err := d.reapExpired(ctx); err != nil {
		return err
	}
	pending, err := d.q.ListPendingGroupOutbox(ctx, sqlc.ListPendingGroupOutboxParams{
		Now:        nullTime(time.Now().UTC()),
		LimitCount: 25,
	})
	if err != nil {
		return fmt.Errorf("list pending outbox: %w", err)
	}
	var errs []error
	for _, row := range pending {
		if err := d.ProcessOutbox(ctx, row); err != nil {
			errs = append(errs, err)
		}
	}
	dueDispatch, err := d.q.ListPendingGroupDispatch(ctx, sqlc.ListPendingGroupDispatchParams{
		Now:        nullTime(time.Now().UTC()),
		LimitCount: 25,
	})
	if err != nil {
		return fmt.Errorf("list pending dispatch: %w", err)
	}
	for _, row := range dueDispatch {
		if err := d.ExecuteDispatch(ctx, row, nil); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (d *GroupDispatcher) reapExpired(ctx context.Context) error {
	now := time.Now().UTC()
	outboxRows, err := d.q.ListExpiredRunningGroupOutbox(ctx, sqlc.ListExpiredRunningGroupOutboxParams{
		Now:        nullTime(now),
		LimitCount: 50,
	})
	if err != nil {
		return fmt.Errorf("list expired outbox: %w", err)
	}
	for _, row := range outboxRows {
		if row.AttemptCount >= d.maxAttempts {
			if _, err := d.q.MarkGroupOutboxFailed(ctx, sqlc.MarkGroupOutboxFailedParams{ID: row.ID, AttemptCount: row.AttemptCount, LastError: "lease expired"}); err != nil {
				return fmt.Errorf("fail expired outbox: %w", err)
			}
			continue
		}
		if _, err := d.q.RequeueGroupOutbox(ctx, sqlc.RequeueGroupOutboxParams{ID: row.ID, AttemptCount: row.AttemptCount, NextAttemptAt: nullTime(now), LastError: "lease expired"}); err != nil {
			return fmt.Errorf("requeue expired outbox: %w", err)
		}
	}
	dispatchRows, err := d.q.ListExpiredRunningGroupDispatch(ctx, sqlc.ListExpiredRunningGroupDispatchParams{
		Now:        nullTime(now),
		LimitCount: 50,
	})
	if err != nil {
		return fmt.Errorf("list expired dispatch: %w", err)
	}
	for _, row := range dispatchRows {
		if row.AttemptCount >= d.maxAttempts {
			if _, err := d.q.MarkGroupDispatchFailed(ctx, sqlc.MarkGroupDispatchFailedParams{ID: row.ID, AttemptCount: row.AttemptCount, LastError: "lease expired"}); err != nil {
				return fmt.Errorf("fail expired dispatch: %w", err)
			}
			continue
		}
		if _, err := d.q.RequeueGroupDispatch(ctx, sqlc.RequeueGroupDispatchParams{ID: row.ID, AttemptCount: row.AttemptCount, NextAttemptAt: nullTime(now), LastError: "lease expired"}); err != nil {
			return fmt.Errorf("requeue expired dispatch: %w", err)
		}
	}
	return nil
}

func (d *GroupDispatcher) processOutbox(ctx context.Context, outbox sqlc.CtxGroupOutbox, publisherOverride GroupPublisher) error {
	claimed, ok, err := d.claimOutbox(ctx, outbox)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	ownedCtx, cancelOwned := context.WithCancel(ctx)
	defer cancelOwned()
	stopHeartbeat := d.startHeartbeat(ownedCtx, "outbox", claimed.ID, func(ctx context.Context, until time.Time) (int64, error) {
		return d.q.ExtendRunningGroupOutboxLease(ctx, sqlc.ExtendRunningGroupOutboxLeaseParams{
			ID:           claimed.ID,
			LeaseUntil:   nullTime(until),
			AttemptCount: claimed.AttemptCount,
		})
	}, cancelOwned)
	defer stopHeartbeat()
	count, err := d.q.CountGroupDispatchByMessage(ownedCtx, claimed.GroupMessageID)
	if err != nil {
		return d.failOutbox(ctx, claimed, fmt.Errorf("count dispatch rows: %w", err))
	}
	if count == 0 {
		if err := d.materializeDispatchRowsTx(ownedCtx, claimed); err != nil {
			return d.failOutbox(ctx, claimed, err)
		}
	}
	stopHeartbeat()
	rows, err := d.q.MarkGroupOutboxCompleted(ctx, sqlc.MarkGroupOutboxCompletedParams{ID: claimed.ID, AttemptCount: claimed.AttemptCount})
	if err != nil {
		return fmt.Errorf("mark outbox completed: %w", err)
	}
	if rows == 0 {
		return nil
	}
	return d.executeDispatchesByMessage(ctx, claimed.GroupMessageID, publisherOverride)
}

func (d *GroupDispatcher) claimOutbox(ctx context.Context, row sqlc.CtxGroupOutbox) (sqlc.CtxGroupOutbox, bool, error) {
	switch row.Status {
	case "running":
		return row, true, nil
	case "pending":
		claimed, err := d.q.ClaimPendingGroupOutbox(ctx, sqlc.ClaimPendingGroupOutboxParams{
			ID:         row.ID,
			Now:        nullTime(time.Now().UTC()),
			LeaseUntil: nullTime(time.Now().UTC().Add(d.leaseDuration)),
		})
		if errors.Is(err, sql.ErrNoRows) {
			return sqlc.CtxGroupOutbox{}, false, nil
		}
		if err != nil {
			return sqlc.CtxGroupOutbox{}, false, fmt.Errorf("claim outbox: %w", err)
		}
		return claimed, true, nil
	default:
		return sqlc.CtxGroupOutbox{}, false, nil
	}
}

func (d *GroupDispatcher) materializeDispatchRowsTx(ctx context.Context, outbox sqlc.CtxGroupOutbox) error {
	responding, err := d.prepareDispatchResponders(ctx, d.q, outbox)
	if err != nil {
		return err
	}
	if d.db == nil {
		return d.createDispatchRows(ctx, d.q, outbox, responding)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin materialize dispatch rows: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := d.createDispatchRows(ctx, d.q.WithTx(tx), outbox, responding); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit materialize dispatch rows: %w", err)
	}
	committed = true
	return nil
}

func (d *GroupDispatcher) prepareDispatchResponders(ctx context.Context, q *sqlc.Queries, outbox sqlc.CtxGroupOutbox) ([]string, error) {
	message, state, err := d.messageAndStateWithQueries(ctx, q, outbox.GroupMessageID)
	if err != nil {
		return nil, err
	}
	envelope, err := DecodeGroupOutboxEnvelope(outbox.Envelope)
	if err != nil {
		return nil, fmt.Errorf("decode outbox envelope: %w", err)
	}
	members, err := q.ListGroupMembers(ctx, outbox.GroupID)
	if err != nil {
		return nil, fmt.Errorf("list group members: %w", err)
	}
	groupMembers := make([]GroupMember, len(members))
	for i, m := range members {
		groupMembers[i] = GroupMember{AgentID: m.AgentID, ReplyChannelID: m.ReplyChannelID}
	}
	if d.coord != nil {
		d.coord.resolveMentionAgentsWithMembers(ctx, outbox.GroupID, state.Platform, envelope.Mentions, groupMembers)
	}
	return d.decideResponders(ctx, q, outbox, message, state, envelope, groupMembers), nil
}

func (d *GroupDispatcher) createDispatchRows(ctx context.Context, q *sqlc.Queries, outbox sqlc.CtxGroupOutbox, responding []string) error {
	members, err := q.ListGroupMembers(ctx, outbox.GroupID)
	if err != nil {
		return fmt.Errorf("list group members: %w", err)
	}
	groupMembers := make([]GroupMember, len(members))
	for i, m := range members {
		groupMembers[i] = GroupMember{AgentID: m.AgentID, ReplyChannelID: m.ReplyChannelID}
	}
	for _, agentID := range responding {
		replyChannelID := findMemberReplyChannel(groupMembers, agentID)
		if replyChannelID == "" {
			d.log.Warn("selected group agent has no reply channel", "group_id", outbox.GroupID, "agent_id", agentID)
			continue
		}
		if err := q.CreateGroupDispatch(ctx, sqlc.CreateGroupDispatchParams{
			ID:             uuid.NewString(),
			GroupMessageID: outbox.GroupMessageID,
			GroupID:        outbox.GroupID,
			AgentID:        agentID,
			ReplyChannelID: replyChannelID,
			Status:         "pending",
			AttemptCount:   0,
			LeaseUntil:     sql.NullString{},
			NextAttemptAt:  sql.NullString{},
			LastError:      "",
		}); err != nil {
			return fmt.Errorf("create dispatch row: %w", err)
		}
	}
	return nil
}

// decideResponders selects which agents respond. Explicit mentions always take
// the deterministic rule path. A no-mention message uses the semantic arbiter
// when one is configured; otherwise it falls back to existing platform behavior
// (web/always broadcast, mention-mode silence).
func (d *GroupDispatcher) decideResponders(ctx context.Context, q *sqlc.Queries, outbox sqlc.CtxGroupOutbox, message sqlc.CtxGroupMessage, state sqlc.CtxGroupState, envelope GroupOutboxEnvelope, groupMembers []GroupMember) []string {
	if len(envelope.Mentions) > 0 {
		if d.coord != nil && d.coord.arbiter != nil {
			return d.coord.arbiter.Decide(ctx, outbox.GroupID, envelope.Mentions, groupMembers, "", DecideOptions{
				AllMembersFallback: false,
				DisableDebounce:    true,
			}).RespondingAgents
		}
		return fallbackGroupDecision(envelope.Mentions, groupMembers, false).RespondingAgents
	}
	if d.coord != nil && d.coord.semanticGroupArbiter != nil {
		return d.semanticResponders(ctx, q, outbox.GroupID, message, state, groupMembers)
	}
	allMembersFallback := state.Platform == "web" || effectivePlatformGroupMode(ctx, q, groupMembers, state) == "always"
	if d.coord != nil && d.coord.arbiter != nil {
		return d.coord.arbiter.Decide(ctx, outbox.GroupID, envelope.Mentions, groupMembers, "", DecideOptions{
			AllMembersFallback: allMembersFallback,
			DisableDebounce:    true,
		}).RespondingAgents
	}
	return fallbackGroupDecision(envelope.Mentions, groupMembers, allMembersFallback).RespondingAgents
}

// semanticResponders builds the semantic request from current DB facts and asks
// the arbiter. Returns nil (no rows) on any silence/failure — the arbiter itself
// already collapses failures to silence.
func (d *GroupDispatcher) semanticResponders(ctx context.Context, q *sqlc.Queries, groupID string, message sqlc.CtxGroupMessage, state sqlc.CtxGroupState, groupMembers []GroupMember) []string {
	smembers := make([]SemanticGroupMember, 0, len(groupMembers))
	for _, m := range groupMembers {
		a, err := q.GetAgent(ctx, m.AgentID)
		if err != nil {
			d.log.Warn("semantic routing: get agent failed", "group_id", groupID, "agent_id", m.AgentID, "error", err)
			continue
		}
		summary := a.SystemPrompt
		smembers = append(smembers, SemanticGroupMember{
			AgentID:        m.AgentID,
			Name:           a.Name,
			Scope:          a.Scope,
			CreatorID:      a.CreatorID,
			Summary:        summary,
			ReplyChannelID: m.ReplyChannelID,
		})
	}
	if len(smembers) == 0 {
		return nil
	}
	ownerUserID := ""
	if state.Platform == "web" {
		ownerUserID = nullStringValue(state.CreatedByUserID)
	}
	decision := d.coord.semanticGroupArbiter.Decide(ctx, SemanticGroupRequest{
		Message:       message.Content,
		RecentContext: d.recentGroupContext(ctx, q, groupID, message.Seq),
		Members:       smembers,
		OwnerUserID:   ownerUserID,
	})
	if !decision.ShouldReply {
		return nil
	}
	return decision.RespondingAgents
}

// recentGroupContext returns prior group messages oldest→newest. It caps by
// seq so delayed outbox retries never route using future messages.
func (d *GroupDispatcher) recentGroupContext(ctx context.Context, q *sqlc.Queries, groupID string, currentSeq int64) []SemanticGroupContextMessage {
	rows, err := q.ListRecentGroupMessagesBeforeSeq(ctx, sqlc.ListRecentGroupMessagesBeforeSeqParams{
		GroupID:   groupID,
		BeforeSeq: currentSeq,
		MaxCount:  int64(semanticMaxContextMessages),
	})
	if err != nil {
		d.log.Warn("semantic routing: list recent messages failed", "group_id", groupID, "error", err)
		return nil
	}
	out := make([]SemanticGroupContextMessage, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		out = append(out, SemanticGroupContextMessage{
			ActorType: r.ActorType,
			ActorID:   r.ActorID,
			Content:   r.Content,
		})
	}
	return out
}

func (d *GroupDispatcher) executeDispatchesByMessage(ctx context.Context, groupMessageID string, publisherOverride GroupPublisher) error {
	rows, err := d.q.ListPendingGroupDispatchByMessage(ctx, sqlc.ListPendingGroupDispatchByMessageParams{
		GroupMessageID: groupMessageID,
		Now:            nullTime(time.Now().UTC()),
	})
	if err != nil {
		return fmt.Errorf("list dispatch rows: %w", err)
	}
	var errs []error
	for _, row := range rows {
		if err := d.ExecuteDispatch(ctx, row, publisherOverride); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (d *GroupDispatcher) ExecuteDispatch(ctx context.Context, row sqlc.CtxGroupDispatch, publisherOverride GroupPublisher) error {
	claimed, ok, err := d.claimDispatch(ctx, row)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	ownedCtx, cancelOwned := context.WithCancel(ctx)
	defer cancelOwned()
	stopHeartbeat := d.startHeartbeat(ownedCtx, "dispatch", claimed.ID, func(ctx context.Context, until time.Time) (int64, error) {
		return d.q.ExtendRunningGroupDispatchLease(ctx, sqlc.ExtendRunningGroupDispatchLeaseParams{
			ID:           claimed.ID,
			LeaseUntil:   nullTime(until),
			AttemptCount: claimed.AttemptCount,
		})
	}, cancelOwned)
	defer stopHeartbeat()
	message, state, err := d.messageAndState(ownedCtx, claimed.GroupMessageID)
	if err != nil {
		return d.failDispatch(ctx, claimed, err)
	}
	publisher, err := d.publisherFor(state, claimed, publisherOverride)
	if err != nil {
		return d.failDispatch(ctx, claimed, err)
	}
	agentName := claimed.AgentID
	if a, err := d.q.GetAgent(ownedCtx, claimed.AgentID); err == nil && a.Name != "" {
		agentName = a.Name
	}
	stream, err := d.chat(ownedCtx, claimed, message, state)
	if err != nil {
		return d.failDispatch(ctx, claimed, err)
	}
	if err := publisher.Publish(ownedCtx, GroupPublishRequest{
		GroupID:          claimed.GroupID,
		AgentID:          claimed.AgentID,
		AgentName:        agentName,
		ReplyChannelID:   claimed.ReplyChannelID,
		Platform:         state.Platform,
		PlatformGroupID:  state.PlatformGroupID,
		PlatformThreadID: state.PlatformThreadID,
		ReplyTo:          nullStringValue(message.PlatformMessageID),
		Stream:           stream,
	}); err != nil {
		return d.failDispatch(ctx, claimed, fmt.Errorf("publish: %w", err))
	}
	rows, err := d.q.MarkGroupDispatchCompleted(ctx, sqlc.MarkGroupDispatchCompletedParams{ID: claimed.ID, AttemptCount: claimed.AttemptCount})
	if err != nil {
		return fmt.Errorf("mark dispatch completed: %w", err)
	}
	if rows == 0 {
		return nil
	}
	return nil
}

func (d *GroupDispatcher) claimDispatch(ctx context.Context, row sqlc.CtxGroupDispatch) (sqlc.CtxGroupDispatch, bool, error) {
	switch row.Status {
	case "running":
		return row, true, nil
	case "pending":
		claimed, err := d.q.ClaimPendingGroupDispatch(ctx, sqlc.ClaimPendingGroupDispatchParams{
			ID:         row.ID,
			Now:        nullTime(time.Now().UTC()),
			LeaseUntil: nullTime(time.Now().UTC().Add(d.leaseDuration)),
		})
		if errors.Is(err, sql.ErrNoRows) {
			return sqlc.CtxGroupDispatch{}, false, nil
		}
		if err != nil {
			return sqlc.CtxGroupDispatch{}, false, fmt.Errorf("claim dispatch: %w", err)
		}
		return claimed, true, nil
	default:
		return sqlc.CtxGroupDispatch{}, false, nil
	}
}

func (d *GroupDispatcher) publisherFor(state sqlc.CtxGroupState, row sqlc.CtxGroupDispatch, override GroupPublisher) (GroupPublisher, error) {
	if override != nil {
		return override, nil
	}
	if publisher, ok := d.publishers.Get(row.ReplyChannelID); ok {
		return publisher, nil
	}
	if state.Platform == "web" {
		return NoopGroupPublisher(), nil
	}
	return nil, fmt.Errorf("publisher %q not registered", row.ReplyChannelID)
}

func (d *GroupDispatcher) failOutbox(ctx context.Context, row sqlc.CtxGroupOutbox, cause error) error {
	if row.AttemptCount >= d.maxAttempts {
		if _, err := d.q.MarkGroupOutboxFailed(ctx, sqlc.MarkGroupOutboxFailedParams{ID: row.ID, AttemptCount: row.AttemptCount, LastError: cause.Error()}); err != nil {
			return fmt.Errorf("mark outbox failed: %w", err)
		}
		return cause
	}
	if _, err := d.q.RequeueGroupOutbox(ctx, sqlc.RequeueGroupOutboxParams{
		ID:            row.ID,
		AttemptCount:  row.AttemptCount,
		NextAttemptAt: nullTime(time.Now().UTC().Add(backoff(row.AttemptCount))),
		LastError:     cause.Error(),
	}); err != nil {
		return fmt.Errorf("requeue outbox: %w", err)
	}
	return cause
}

func (d *GroupDispatcher) failDispatch(ctx context.Context, row sqlc.CtxGroupDispatch, cause error) error {
	if row.AttemptCount >= d.maxAttempts {
		if _, err := d.q.MarkGroupDispatchFailed(ctx, sqlc.MarkGroupDispatchFailedParams{ID: row.ID, AttemptCount: row.AttemptCount, LastError: cause.Error()}); err != nil {
			return fmt.Errorf("mark dispatch failed: %w", err)
		}
		return cause
	}
	if _, err := d.q.RequeueGroupDispatch(ctx, sqlc.RequeueGroupDispatchParams{
		ID:            row.ID,
		AttemptCount:  row.AttemptCount,
		NextAttemptAt: nullTime(time.Now().UTC().Add(backoff(row.AttemptCount))),
		LastError:     cause.Error(),
	}); err != nil {
		return fmt.Errorf("requeue dispatch: %w", err)
	}
	return cause
}

func (d *GroupDispatcher) messageAndState(ctx context.Context, messageID string) (sqlc.CtxGroupMessage, sqlc.CtxGroupState, error) {
	return d.messageAndStateWithQueries(ctx, d.q, messageID)
}

func (d *GroupDispatcher) messageAndStateWithQueries(ctx context.Context, q *sqlc.Queries, messageID string) (sqlc.CtxGroupMessage, sqlc.CtxGroupState, error) {
	message, err := q.GetGroupMessage(ctx, messageID)
	if err != nil {
		return sqlc.CtxGroupMessage{}, sqlc.CtxGroupState{}, fmt.Errorf("get group message: %w", err)
	}
	state, err := q.GetGroupStateByID(ctx, message.GroupID)
	if err != nil {
		return sqlc.CtxGroupMessage{}, sqlc.CtxGroupState{}, fmt.Errorf("get group state: %w", err)
	}
	return message, state, nil
}

type groupModeConfig struct {
	GroupMode string                          `json:"group_mode"`
	Groups    map[string]groupModeGroupConfig `json:"groups"`
}

type groupModeGroupConfig struct {
	GroupMode string `json:"group_mode"`
}

func effectivePlatformGroupMode(ctx context.Context, q *sqlc.Queries, members []GroupMember, state sqlc.CtxGroupState) string {
	if state.Platform == "web" {
		return "mention"
	}
	for _, member := range members {
		if member.ReplyChannelID == "" {
			continue
		}
		ch, err := q.GetChannel(ctx, member.ReplyChannelID)
		if err != nil {
			continue
		}
		var cfg groupModeConfig
		if err := json.Unmarshal([]byte(ch.Config), &cfg); err != nil {
			continue
		}
		mode := cfg.GroupMode
		if gc, ok := cfg.Groups[state.PlatformGroupID]; ok && gc.GroupMode != "" {
			mode = gc.GroupMode
		}
		if mode == "always" {
			return "always"
		}
	}
	return "mention"
}

func (d *GroupDispatcher) chatDispatch(ctx context.Context, row sqlc.CtxGroupDispatch, message sqlc.CtxGroupMessage, state sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
	sessionKey := agent.BuildGroupSessionKey(row.AgentID, row.GroupID)
	stream, doneC, err := d.queue.Enqueue(ctx, sessionKey, func(qctx context.Context) (*pkgchannel.ChatStream, error) {
		return d.chatDispatchUnqueued(qctx, row, message, state)
	})
	if err != nil {
		return nil, err
	}
	out := make(chan pkgchannel.Event, 100)
	go func() {
		defer close(doneC)
		defer close(out)
		for evt := range stream.Events {
			select {
			case out <- evt:
			case <-ctx.Done():
				out <- pkgchannel.Event{Err: ctx.Err()}
				for range stream.Events {
				}
				return
			}
		}
	}()
	return &pkgchannel.ChatStream{Events: out, SessionID: stream.SessionID}, nil
}

func (d *GroupDispatcher) chatDispatchUnqueued(ctx context.Context, row sqlc.CtxGroupDispatch, message sqlc.CtxGroupMessage, state sqlc.CtxGroupState) (*pkgchannel.ChatStream, error) {
	if d.coord == nil {
		return nil, errors.New("coordinator not configured")
	}
	ctx = memory.WithGroupSeq(ctx, message.Seq)
	content := []ai.ContentBlock{ai.TextContent{Text: message.Content}}
	var stream *pkgchannel.ChatStream
	var err error
	if state.Platform == "web" {
		stream, err = d.chatWeb(ctx, row, message)
	} else {
		var rc *ResolvedChat
		rc, err = d.coord.resolveGroupChat(ctx, pkgchannel.IncomingMessage{
			Platform:  state.Platform,
			ChannelID: row.ReplyChannelID,
			SenderID:  message.ActorID,
			ChatID:    state.PlatformGroupID,
			IsGroup:   true,
			ThreadID:  state.PlatformThreadID,
			Content:   content,
			MessageID: nullStringValue(message.PlatformMessageID),
			ReplyTo:   nullStringValue(message.ReplyTo),
		}, row.GroupID, row.AgentID, row.ReplyChannelID)
		if err == nil {
			stream, err = d.coord.chatWithRC(ctx, rc, content)
		}
	}
	if err != nil {
		return nil, err
	}
	return d.wrapGroupResponseStream(ctx, row.GroupID, row.AgentID, stream), nil
}

func (d *GroupDispatcher) chatWeb(ctx context.Context, row sqlc.CtxGroupDispatch, message sqlc.CtxGroupMessage) (*pkgchannel.ChatStream, error) {
	svc := d.coord.serviceManager.GetService(row.AgentID)
	if svc == nil {
		return nil, fmt.Errorf("agent service %q not found", row.AgentID)
	}
	sessionKey := agent.BuildGroupSessionKey(row.AgentID, row.GroupID)
	channelStr := "group:" + row.GroupID
	info, err := svc.ResolveChannelSession(ctx, sessionKey, row.GroupID, row.AgentID, session.Channel(channelStr))
	if err != nil {
		return nil, fmt.Errorf("resolve session: %w", err)
	}
	speaker := webGroupSpeaker(message)
	// CtxGroupMessage carries no display name; fill it best-effort from the auth
	// user so the prompt shows a real name instead of "Unknown". Fail-soft.
	if speaker.UserID != "" && speaker.DisplayName == "" && d.coord.auth != nil {
		if u, err := d.coord.auth.GetUser(ctx, speaker.UserID); err == nil && u.Name != "" {
			speaker.DisplayName = u.Name
		}
	}
	events := svc.Chat(ctx, agent.ChatRequest{
		SessionID:      info.ID,
		UserID:         row.GroupID,
		AgentID:        row.AgentID,
		Kind:           session.KindChat,
		GroupID:        row.GroupID,
		Channel:        session.Channel(channelStr),
		Message:        message.Content,
		CurrentSpeaker: speaker,
	})
	out := make(chan pkgchannel.Event, 100)
	go func() {
		defer close(out)
		for evt := range events {
			select {
			case out <- convertEvent(evt):
			case <-ctx.Done():
				out <- pkgchannel.Event{Err: ctx.Err()}
				for range events {
				}
				return
			}
		}
	}()
	return &pkgchannel.ChatStream{Events: out, SessionID: info.ID}, nil
}

// webGroupSpeaker derives the per-turn speaker for a Web group dispatch. Web
// senders authenticate and SendGroupMessage persists the auth user id as
// actor_id, so it is a safe profile target — but only for a genuine human actor.
// Any other actor type or an empty id fails closed (zero speaker) so a malformed
// row never injects an arbitrary user's private profile.
func webGroupSpeaker(message sqlc.CtxGroupMessage) memory.CurrentSpeaker {
	if message.ActorType != string(eventlog.ActorHuman) || message.ActorID == "" {
		return memory.CurrentSpeaker{}
	}
	return memory.CurrentSpeaker{
		Platform:       "web",
		PlatformUserID: message.ActorID,
		UserID:         message.ActorID,
	}
}

func (d *GroupDispatcher) wrapGroupResponseStream(ctx context.Context, groupID, agentID string, stream *pkgchannel.ChatStream) *pkgchannel.ChatStream {
	out := make(chan pkgchannel.Event, 100)
	go func() {
		defer close(out)
		var textBuf strings.Builder
		sawErr := false
		for evt := range stream.Events {
			if evt.Err != nil {
				sawErr = true
			}
			if evt.Text != "" {
				textBuf.WriteString(evt.Text)
			}
			select {
			case out <- evt:
			case <-ctx.Done():
			}
		}
		// The forwarding select can win the race against ctx.Done, so
		// cancellation must be re-checked deterministically before treating the
		// buffered text as a complete reply.
		if ctx.Err() != nil {
			sawErr = true
		}
		if !sawErr && textBuf.Len() > 0 && d.coord != nil && d.coord.eventLog != nil {
			writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if _, err := d.coord.eventLog.AppendToGroup(writeCtx, groupID, eventlog.GroupMessage{
				ActorType:      eventlog.ActorAgent,
				ActorID:        agentID,
				Content:        textBuf.String(),
				AgentSessionID: stream.SessionID,
			}); err != nil {
				d.log.Warn("failed to write group response", "group_id", groupID, "agent_id", agentID, "error", err)
			}
		}
	}()
	return &pkgchannel.ChatStream{Events: out, SessionID: stream.SessionID}
}

func fallbackGroupDecision(mentions []pkgchannel.Mention, members []GroupMember, allMembersFallback bool) ArbiterDecision {
	mentioned := mentionedAgentIDs(mentions)
	if len(mentioned) > 0 {
		memberSet := make(map[string]struct{}, len(members))
		for _, m := range members {
			memberSet[m.AgentID] = struct{}{}
		}
		var responding []string
		for _, id := range mentioned {
			if _, ok := memberSet[id]; ok {
				responding = append(responding, id)
			}
		}
		return ArbiterDecision{RespondingAgents: responding}
	}
	if allMembersFallback {
		responding := make([]string, len(members))
		for i, m := range members {
			responding[i] = m.AgentID
		}
		return ArbiterDecision{RespondingAgents: responding}
	}
	return ArbiterDecision{}
}

func backoff(attempts int64) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := time.Duration(attempts) * time.Second
	if d > time.Minute {
		return time.Minute
	}
	return d
}

func nullTime(t time.Time) sql.NullString {
	return sql.NullString{String: t.UTC().Format("2006-01-02 15:04:05"), Valid: true}
}

func nullStringValue(v sql.NullString) string {
	if !v.Valid {
		return ""
	}
	return v.String
}
