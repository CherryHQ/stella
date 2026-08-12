package lcm

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	groupLCMTokenBudget = 80_000
	groupLCMFreshTail   = 6
)

// groupHistoryMessage is the lightweight Event Log projection copied into one
// Agent's LCM. Historical scans omit image blobs; the current trigger carries
// ContentBlocks because it is fetched by ID as one complete event.
type groupHistoryMessage struct {
	ID               string
	Seq              int64
	ActorType        string
	ActorID          string
	AgentSessionID   string
	ActorDisplayName pgtype.Text
	Content          string
	ContentBlocks    []byte
}

func groupCursorPipeline(agentID string) string { return memory.GroupIngestPipeline(agentID) }

// assembleGroup reads the already-synchronized per-agent LCM. The runtime calls
// SyncGroupEventsBefore before compaction so pending public events participate
// in both compaction and final assembly.
func (p *Provider) assembleGroup(ctx context.Context, session memory.Session, budget, freshTail int) ([]ai.Message, error) {
	convID, err := p.getOrCreateConversation(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("get conversation: %w", err)
	}
	return p.assembler.assemble(ctx, convID, budget, freshTail)
}

// SyncGroupEventsBefore copies the newest bounded public event window into this
// agent's LCM without advancing the group cursor. Cursor movement remains tied
// to a successful chat turn.
func (p *Provider) SyncGroupEventsBefore(ctx context.Context, session memory.Session, triggerSeq int64) error {
	if session.GroupID == "" || session.AgentID == "" || triggerSeq <= 0 {
		return nil
	}
	watermark, err := p.getGroupCursor(ctx, session.GroupID, groupCursorPipeline(session.AgentID))
	if err != nil {
		return err
	}
	if triggerSeq <= watermark+1 {
		return nil
	}

	rows, err := p.q.ListGroupMessagesForLCM(ctx, sqlc.ListGroupMessagesForLCMParams{
		GroupID:     session.GroupID,
		AfterSeq:    watermark,
		BeforeSeq:   triggerSeq,
		SelfAgentID: session.AgentID,
		TokenBudget: groupLCMTokenBudget,
	})
	if err != nil {
		return fmt.Errorf("list group messages for lcm: %w", err)
	}
	publicRows := make([]groupHistoryMessage, 0, len(rows))
	for _, row := range rows {
		publicRows = append(publicRows, groupHistoryMessage{
			ID:               row.ID,
			Seq:              row.Seq,
			ActorType:        row.ActorType,
			ActorID:          row.ActorID,
			AgentSessionID:   row.AgentSessionID,
			ActorDisplayName: row.ActorDisplayName,
			Content:          row.Content,
		})
	}
	if err := p.appendGroupHistory(ctx, session, publicRows, nil, false); err != nil {
		return err
	}
	p.log.Debug("group lcm public events synchronized",
		"group_id", session.GroupID,
		"agent_id", session.AgentID,
		"cursor_seq", watermark,
		"trigger_seq", triggerSeq,
		"selected_events", len(rows),
	)
	return nil
}

// AppendGroupTurn atomically persists the current public trigger and the
// assistant/tool continuation. If the trigger was already persisted, the whole
// turn is an idempotent no-op.
func (p *Provider) AppendGroupTurn(
	ctx context.Context,
	session memory.Session,
	groupMessageID string,
	_ ai.Message,
	continuation ...ai.Message,
) error {
	if session.GroupID == "" || groupMessageID == "" {
		return errors.New("group session and message id are required")
	}
	row, err := p.q.GetGroupMessage(ctx, groupMessageID)
	if err != nil {
		return fmt.Errorf("get triggering group message: %w", err)
	}
	if row.GroupID != session.GroupID {
		return fmt.Errorf("group message %s does not belong to group %s", groupMessageID, session.GroupID)
	}
	publicRow := groupHistoryMessage{
		ID:               row.ID,
		Seq:              row.Seq,
		ActorType:        row.ActorType,
		ActorID:          row.ActorID,
		AgentSessionID:   row.AgentSessionID,
		ActorDisplayName: row.ActorDisplayName,
		Content:          row.Content,
		ContentBlocks:    row.ContentBlocks,
	}
	if err := p.appendGroupHistory(ctx, session, []groupHistoryMessage{publicRow}, continuation, true); err != nil {
		return err
	}
	p.log.Debug("group lcm turn ensured",
		"group_id", session.GroupID,
		"agent_id", session.AgentID,
		"trigger_seq", row.Seq,
		"continuation_messages", len(continuation),
	)
	return nil
}

func (p *Provider) appendGroupHistory(
	ctx context.Context,
	session memory.Session,
	publicRows []groupHistoryMessage,
	continuation []ai.Message,
	atomicTurn bool,
) error {
	if len(publicRows) == 0 && len(continuation) == 0 {
		return nil
	}

	return p.withSessionLock(session.ID, func() error {
		convID, err := p.getOrCreateConversation(ctx, session)
		if err != nil {
			return err
		}

		tx, err := p.db.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin group history tx: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		qtx := p.q.WithTx(tx)

		// The database lock makes seq/ordinal allocation and origin idempotency
		// safe across multiple Stella nodes.
		if err = qtx.LockConversationForWrite(ctx, convID); err != nil {
			return fmt.Errorf("lock group conversation: %w", err)
		}

		if atomicTurn && len(publicRows) > 0 {
			_, lookupErr := qtx.GetMessageByGroupOrigin(ctx, sqlc.GetMessageByGroupOriginParams{
				ConversationID:       convID,
				OriginGroupMessageID: pgtype.Text{String: publicRows[0].ID, Valid: true},
			})
			switch {
			case lookupErr == nil:
				return nil
			case !errors.Is(lookupErr, pgx.ErrNoRows):
				return fmt.Errorf("check existing group turn: %w", lookupErr)
			}
		}

		seq, err := qtx.GetMaxSeq(ctx, convID)
		if err != nil {
			return fmt.Errorf("get max seq: %w", err)
		}
		ordinal, err := qtx.GetMaxContextOrdinal(ctx, convID)
		if err != nil {
			return fmt.Errorf("get max ordinal: %w", err)
		}

		appendMessage := func(msg ai.Message, originGroupMessageID string, originActor *eventlog.MessageActor) error {
			for rowIndex, row := range messageToRows(msg) {
				seq++
				actor := actorForStorageRow(ctx, session, row)
				if originActor != nil {
					actor = *originActor
				}
				var dbMsg sqlc.CtxMessage
				if originGroupMessageID != "" && rowIndex == 0 {
					dbMsg, err = qtx.CreateMessageWithGroupOrigin(ctx, sqlc.CreateMessageWithGroupOriginParams{
						ID:                   uuid.Must(uuid.NewV7()).String(),
						ConversationID:       convID,
						Seq:                  seq,
						Role:                 row.role,
						EventType:            row.eventType,
						Content:              row.content,
						TokenCount:           int64(memory.EstimateTokens(row.content)),
						ActorType:            string(actor.Type),
						ActorID:              pgtype.Text{String: actor.ID, Valid: actor.ID != ""},
						SourceSessionID:      pgtype.Text{String: actor.SourceSessionID, Valid: actor.SourceSessionID != ""},
						OriginGroupMessageID: pgtype.Text{String: originGroupMessageID, Valid: true},
					})
				} else {
					dbMsg, err = qtx.CreateMessage(ctx, sqlc.CreateMessageParams{
						ID:             uuid.Must(uuid.NewV7()).String(),
						ConversationID: convID,
						Seq:            seq,
						Role:           row.role,
						EventType:      row.eventType,
						Content:        row.content,
						TokenCount:     int64(memory.EstimateTokens(row.content)),
						ActorType:      string(actor.Type),
						ActorID:        pgtype.Text{String: actor.ID, Valid: actor.ID != ""},
						SourceSessionID: pgtype.Text{
							String: actor.SourceSessionID,
							Valid:  actor.SourceSessionID != "",
						},
					})
				}
				if err != nil {
					return fmt.Errorf("create group history message: %w", err)
				}

				ordinal++
				if err = qtx.AppendContextItem(ctx, sqlc.AppendContextItemParams{
					ConversationID: convID,
					Ordinal:        ordinal,
					ItemType:       itemTypeMessage,
					MessageID:      pgtype.Text{String: dbMsg.ID, Valid: true},
					EventType:      row.eventType,
					Role:           row.role,
				}); err != nil {
					return fmt.Errorf("append group context item: %w", err)
				}
			}
			return nil
		}

		for _, publicRow := range publicRows {
			if !atomicTurn {
				_, lookupErr := qtx.GetMessageByGroupOrigin(ctx, sqlc.GetMessageByGroupOriginParams{
					ConversationID:       convID,
					OriginGroupMessageID: pgtype.Text{String: publicRow.ID, Valid: true},
				})
				switch {
				case lookupErr == nil:
					continue
				case !errors.Is(lookupErr, pgx.ErrNoRows):
					return fmt.Errorf("check existing group event: %w", lookupErr)
				}
			}
			msg, ok := groupRowToMessage(publicRow)
			if !ok {
				continue
			}
			actor := eventlog.MessageActor{
				Type:            eventlog.ActorType(publicRow.ActorType),
				ID:              publicRow.ActorID,
				SourceSessionID: publicRow.AgentSessionID,
			}
			if err := appendMessage(msg, publicRow.ID, &actor); err != nil {
				return err
			}
		}
		for _, msg := range continuation {
			if err := appendMessage(msg, "", nil); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	})
}

func (p *Provider) CommitGroupCursor(ctx context.Context, session memory.Session, triggerSeq int64) error {
	if session.GroupID == "" || session.AgentID == "" || triggerSeq <= 0 {
		return nil
	}
	pipeline := groupCursorPipeline(session.AgentID)
	watermark, err := p.getGroupCursor(ctx, session.GroupID, pipeline)
	if err != nil {
		return err
	}
	if triggerSeq <= watermark {
		return nil
	}
	if err := p.q.UpsertIngestCursor(ctx, sqlc.UpsertIngestCursorParams{
		GroupID:  session.GroupID,
		Pipeline: pipeline,
		LastSeq:  triggerSeq,
	}); err != nil {
		return fmt.Errorf("update group cursor: %w", err)
	}
	p.log.Debug("group lcm cursor committed",
		"group_id", session.GroupID,
		"agent_id", session.AgentID,
		"previous_seq", watermark,
		"committed_seq", triggerSeq,
	)
	return nil
}

func (p *Provider) getGroupCursor(ctx context.Context, groupID, pipeline string) (int64, error) {
	cursor, err := p.q.GetIngestCursor(ctx, sqlc.GetIngestCursorParams{
		GroupID:  groupID,
		Pipeline: pipeline,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read group cursor: %w", err)
	}
	return cursor.LastSeq, nil
}

// groupRowToMessage renders public identity for model context while durable
// deduplication remains based on the event row ID, never this display text.
func groupRowToMessage(row groupHistoryMessage) (ai.Message, bool) {
	label := row.ActorID
	if row.ActorDisplayName.Valid && row.ActorDisplayName.String != "" {
		label = row.ActorDisplayName.String
	} else if row.ActorType == string(eventlog.ActorAgent) {
		label = "agent:" + row.ActorID
	}
	if blocks, err := ai.UnmarshalContentBlocks(row.ContentBlocks); err == nil && len(blocks) > 0 {
		content := make([]ai.ContentBlock, 0, len(blocks)+1)
		content = append(content, ai.TextContent{Text: fmt.Sprintf("[seq:%d %s]:", row.Seq, label)})
		content = append(content, blocks...)
		return ai.UserMessage{Content: content}, true
	}
	if row.Content == "" {
		return nil, false
	}
	return ai.UserMessage{Content: fmt.Sprintf("[seq:%d %s]: %s", row.Seq, label, row.Content)}, true
}
