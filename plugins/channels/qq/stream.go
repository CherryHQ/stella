package qq

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tencent-connect/botgo/dto"

	"github.com/CherryHQ/stella/pkg/channel"
)

const streamEditInterval = time.Second

const typingCursor = " \u258D"

// streamResponse consumes the agent event stream and progressively sends
// updates using QQ's native Stream API. Returns the final text, collected
// images, and any stream error.
func (b *Bot) streamResponse(ctx context.Context, stream *channel.ChatStream, authorID, groupID, msgID string, scope messageScope) (string, []channel.ImageEvent, error) {
	defer stream.Discard()
	targetID := authorID
	if groupID != "" {
		targetID = groupID
	}

	var sb strings.Builder
	var streamErr error
	var currentTool string
	var streamMsgID string
	var images []channel.ImageEvent
	var seq uint32 = 1
	lastSend := time.Time{}

	for evt := range stream.Events {
		if evt.Err != nil {
			streamErr = evt.Err
			break
		}

		if evt.Image != nil {
			images = append(images, *evt.Image)
			continue
		}

		if evt.ToolUse != nil {
			line := channel.ToolLine(evt.ToolUse)
			if line != "" {
				currentTool = line
			} else {
				currentTool = ""
			}
			lastSend = time.Time{}
		}

		sb.WriteString(evt.Text)

		now := time.Now()
		if now.Sub(lastSend) < streamEditInterval {
			continue
		}

		current := sb.String()
		if strings.TrimSpace(current) == "" && currentTool == "" {
			continue
		}

		display := buildStreamDisplay(current, currentTool)

		if err := stream.CheckOperation(ctx); err != nil {
			return sb.String(), images, err
		}
		newMsgID, err := b.sendStreamChunk(ctx, targetID, msgID, display, streamMsgID, seq, false, scope)
		if err != nil {
			return sb.String(), images, err
		}
		if streamMsgID == "" {
			streamMsgID = newMsgID
		}
		seq++
		lastSend = now
	}

	// Send a terminal stream chunk (State=10) to finalize the stream,
	// so QQ clients exit the "generating" state.
	if streamMsgID != "" {
		final := sb.String()
		if strings.TrimSpace(final) == "" {
			final = "(empty response)"
		}
		if err := stream.CheckOperation(ctx); err != nil {
			return sb.String(), images, err
		}
		if _, err := b.sendStreamChunk(ctx, targetID, msgID, final, streamMsgID, seq, true, scope); err != nil {
			return sb.String(), images, err
		}
	}

	return sb.String(), images, streamErr
}

// sendStreamChunk sends a streaming message chunk using QQ's Stream API.
// Returns the message ID from the first chunk (used as stream ID for subsequent chunks).
func (b *Bot) sendStreamChunk(ctx context.Context, targetID, replyMsgID, text, streamID string, seq uint32, done bool, scope messageScope) (string, error) {
	state := int32(1) // generating
	if done {
		state = 10 // body done
	}

	msg := dto.MessageToCreate{
		Content: text,
		MsgType: dto.TextMsg,
		MsgID:   replyMsgID,
		MsgSeq:  seq,
		Stream: &dto.Stream{
			State: state,
			ID:    streamID,
			Index: int32(seq),
		},
	}

	var (
		result *dto.Message
		err    error
	)
	switch scope {
	case scopeC2C:
		result, err = b.api.PostC2CMessage(ctx, targetID, msg)
	case scopeGroup:
		result, err = b.api.PostGroupMessage(ctx, targetID, msg)
	}

	if err != nil {
		return "", err
	}
	if result != nil {
		return result.ID, nil
	}
	return "", nil
}

// buildStreamDisplay constructs the streaming display text with tool indicator
// and cursor, truncated to QQ's message length limit (UTF-8 safe).
func buildStreamDisplay(text, currentTool string) string {
	display := text
	suffix := typingCursor
	if currentTool != "" {
		suffix = "\n\n" + currentTool + typingCursor
	}

	if len(suffix) >= qqMaxMessageLen {
		suffix = typingCursor
	}

	if len(display)+len(suffix) > qqMaxMessageLen {
		cutAt := max(qqMaxMessageLen-len(suffix)-3, 0)
		for cutAt > 0 && !utf8.RuneStart(display[cutAt]) {
			cutAt--
		}
		display = display[:cutAt] + "..."
	}

	return display + suffix
}
