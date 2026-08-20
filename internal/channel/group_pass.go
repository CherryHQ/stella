package channel

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// modelPassToken is the reply an agent writes when it has read the group and
// has nothing to add. Triage decides whether a turn is worth thinking about;
// only the agent that did the thinking can tell whether it is worth saying.
// Without this, an agent woken by mistake had one way to stay out of the way:
// posting that it was staying out of the way.
const modelPassToken = "PASS"

// isModelPass recognises the pass reply through the wrapping models add to it:
// surrounding whitespace, a code fence, inline backticks, bold markers, a final
// period. An empty reply is a pass too -- the agent produced nothing to post.
//
// Only a bare pass counts. "PASS, but check the logs" is a reply that happens
// to start with the word, and it gets posted.
func isModelPass(text string) bool {
	trimmed := strings.TrimSpace(text)
	if fenced, ok := strings.CutPrefix(trimmed, "```"); ok {
		if body, ok := strings.CutSuffix(strings.TrimSpace(fenced), "```"); ok {
			// Drop an info string like ```text on the opening fence.
			if _, rest, found := strings.Cut(body, "\n"); found {
				body = rest
			}
			trimmed = strings.TrimSpace(body)
		}
	}
	trimmed = strings.Trim(trimmed, "`*_ \t\n.")
	return trimmed == "" || strings.EqualFold(trimmed, modelPassToken)
}

// stripTrailingPass drops the pass reply itself and keeps everything the turn
// actually did. An agent may call tools and then decide it has nothing to say;
// dropping the whole turn would make it forget the claim it just took or the
// file it just wrote, while the side effect stays real for every peer.
//
// Only trailing text-only assistant messages go. A message carrying a tool call
// stops the walk, so no tool_use is ever separated from its tool_result.
func stripTrailingPass(rows []ai.Message) []ai.Message {
	for len(rows) > 0 {
		msg, ok := rows[len(rows)-1].(ai.AssistantMessage)
		if !ok {
			break
		}
		text := strings.Builder{}
		for _, block := range msg.Content {
			switch b := block.(type) {
			case ai.TextContent:
				text.WriteString(b.Text)
			case ai.ThinkingContent:
			default:
				return rows
			}
		}
		if !isModelPass(text.String()) {
			break
		}
		rows = rows[:len(rows)-1]
	}
	return rows
}

// stripTrailingTextOnlyAssistant removes the unpublished final response from a
// stopped turn without touching a tool transcript. Text and private reasoning
// are both draft response material; a ToolCall or ToolResult is an execution
// record and must stay paired.
func stripTrailingTextOnlyAssistant(rows []ai.Message) []ai.Message {
	if len(rows) == 0 {
		return rows
	}
	msg, ok := rows[len(rows)-1].(ai.AssistantMessage)
	if !ok {
		return rows
	}
	for _, block := range msg.Content {
		switch block.(type) {
		case ai.TextContent, ai.ThinkingContent:
		default:
			return rows
		}
	}
	return rows[:len(rows)-1]
}

// retireModelPass records the silent turn and commits what the agent read.
//
// The peer rows it was shown and its ingest cursor still commit: the agent did
// read them, and leaving the cursor behind would replay the same messages on
// every later turn. The pass reply itself does not: an empty assistant message
// would make the session history look like the agent ignored the group, and
// some providers reject empty turns outright.
func (d *GroupDispatcher) retireModelPass(ctx context.Context, row sqlc.CtxGroupDispatch, turn memory.DeferredGroupTurn) error {
	if d.committer == nil {
		return errors.New("group dispatcher requires memory.TxGroupCommitter")
	}
	turn.OwnRows = stripTrailingPass(turn.OwnRows)
	tx, err := d.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("model pass: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := d.q.WithTx(tx)
	if err := d.committer.CommitGroupTurn(ctx, q, turn); err != nil {
		return fmt.Errorf("model pass: commit read context: %w", err)
	}
	if _, err := q.MarkGroupDispatchSilent(ctx, sqlc.MarkGroupDispatchSilentParams{ID: row.ID, AttemptCount: row.AttemptCount, Reason: groupSilentModelPass}); err != nil {
		return fmt.Errorf("model pass: mark silent: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("model pass: commit: %w", err)
	}
	if d.events != nil {
		d.events.AnnounceTurn(row.GroupID, row.AgentID, string(groupTurnSilent), groupSilentModelPass)
	}
	return nil
}

// groupSilentModelPass is the reason a pass is retired under. It is UI: an
// operator watching /events must be able to tell "the agent chose not to speak"
// from "a server gate stopped it".
const groupSilentModelPass = "model_pass"
