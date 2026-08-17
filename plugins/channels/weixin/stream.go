package weixin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/CherryHQ/stella/pkg/channel"
)

const streamChunkSize = 500

// streamPieceContent is the JSON payload encoded inside SyncStreamPiece.PieceData.
type streamPieceContent struct {
	Type       string `json:"type"`
	Text       string `json:"text"`
	StreamType string `json:"stream_type,omitempty"`
}

// WeixinStreamSender manages a single iLink uplink stream session.
// It replicates the pendingPieces retry pattern from the official stream.js.
type WeixinStreamSender struct {
	bot              *Bot
	deviceID         string
	clientStreamID   string
	streamTicket     string
	pieceSeq         int
	pendingPieces    []SyncStreamPiece
	seqBeforePending int
}

func newWeixinStreamSender(bot *Bot, deviceID, clientStreamID, streamTicket string) *WeixinStreamSender {
	return &WeixinStreamSender{
		bot:            bot,
		deviceID:       deviceID,
		clientStreamID: clientStreamID,
		streamTicket:   streamTicket,
	}
}

// makePiece encodes text as a stream piece and increments pieceSeq.
func (s *WeixinStreamSender) makePiece(text string) SyncStreamPiece {
	s.pieceSeq++
	content, _ := json.Marshal(streamPieceContent{Type: "text", Text: text, StreamType: "text"})
	return SyncStreamPiece{
		PieceSeq:  s.pieceSeq,
		PieceData: base64.StdEncoding.EncodeToString(content),
	}
}

// sendPieces sends newPieces via SyncStream, prepending any retained pendingPieces.
// isEnd = true sets end_up_piece_seq to the last piece's seq.
// On failure: all pieces are saved to pendingPieces and pieceSeq is rolled back.
func (s *WeixinStreamSender) sendPieces(newPieces []SyncStreamPiece, isEnd bool) error {
	var seqBefore int
	if len(s.pendingPieces) > 0 {
		seqBefore = s.seqBeforePending
	} else {
		seqBefore = s.pieceSeq - len(newPieces)
	}

	toSend := append(s.pendingPieces, newPieces...) //nolint:gocritic
	s.pendingPieces = nil

	if len(toSend) == 0 {
		return nil
	}

	endSeq := 0
	if isEnd {
		endSeq = toSend[len(toSend)-1].PieceSeq
	}

	err := s.bot.client.SyncStream(SyncStreamRequest{
		DeviceID:       s.deviceID,
		ClientStreamID: s.clientStreamID,
		UpPieceList:    toSend,
		EndUpPieceSeq:  endSeq,
		StreamTicket:   s.streamTicket,
	})
	if err != nil {
		s.pendingPieces = toSend
		s.seqBeforePending = seqBefore
		s.pieceSeq = seqBefore
		return err
	}
	return nil
}

// sendViaStream delivers response text using one-attempt iLink stream mutations.
// Any failure has an unknown outcome and is returned without retry or fallback.
func (b *Bot) sendViaStream(ctx context.Context, stream *channel.ChatStream, msg WeixinMessage, text string) error {
	defer stream.Discard()
	if b.guard.IsPaused() {
		return b.guard.AssertActive()
	}

	deviceID := b.cfg.BotID
	if deviceID == "" {
		deviceID = "stella"
	}
	clientStreamID := RandomClientID("stream")

	if err := stream.CheckOperation(ctx); err != nil {
		return err
	}
	initResp, err := b.client.InitStream(deviceID, clientStreamID)
	if err != nil {
		return err
	}

	sender := newWeixinStreamSender(b, deviceID, clientStreamID, initResp.StreamTicket)

	chunks := channel.SplitMessage(text, streamChunkSize)
	pieces := make([]SyncStreamPiece, 0, len(chunks))
	for _, chunk := range chunks {
		pieces = append(pieces, sender.makePiece(chunk))
	}

	if err := stream.CheckOperation(ctx); err != nil {
		return err
	}
	return sender.sendPieces(pieces, true)
}

const (
	// weixinMaxMessageLen is the maximum text message length for WeChat iLink.
	weixinMaxMessageLen = 2000
)

const minToolDisplayDuration = 2 * time.Second

// newToolTracker creates a ToolTracker configured for WeChat display.
func newToolTracker() channel.ToolTracker {
	return channel.ToolTracker{MinDisplayDuration: minToolDisplayDuration}
}

// streamEvents consumes the agent event stream, accumulates text, and tracks tools.
// Returns the final response text, tool tracker, collected images, and any stream error.
func (b *Bot) streamEvents(msg WeixinMessage, events <-chan channel.Event) (string, *channel.ToolTracker, []channel.ImageEvent, error) {
	var sb strings.Builder
	var streamErr error
	tt := newToolTracker()
	var images []channel.ImageEvent

	for evt := range events {
		if evt.Err != nil {
			streamErr = evt.Err
			break
		}

		if evt.Image != nil {
			images = append(images, *evt.Image)
			continue
		}

		if evt.ToolUse != nil {
			tt.Handle(evt.ToolUse)
		}

		sb.WriteString(evt.Text)
	}

	return sb.String(), &tt, images, streamErr
}
