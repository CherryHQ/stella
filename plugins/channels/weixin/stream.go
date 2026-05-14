package weixin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/CherryHQ/stella/pkg/channel"
)

const (
	streamChunkSize  = 500
	streamMaxRetries = 3
)

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

// sendViaStream delivers response text using the iLink streaming API.
// Returns true on success; returns false (caller should fall back to sendmessage) if
// InitStream fails or all SyncStream retries are exhausted.
func (b *Bot) sendViaStream(msg WeixinMessage, text string) bool {
	if b.guard.IsPaused() {
		return false
	}

	deviceID := b.cfg.BotID
	if deviceID == "" {
		deviceID = "stella"
	}
	clientStreamID := RandomClientID("stream")

	initResp, err := b.client.InitStream(deviceID, clientStreamID)
	if err != nil {
		logger().Debug("init_stream failed, falling back to sendmessage",
			"user_id", msg.FromUserID, "error", err)
		return false
	}

	sender := newWeixinStreamSender(b, deviceID, clientStreamID, initResp.StreamTicket)

	chunks := channel.SplitMessage(text, streamChunkSize)
	pieces := make([]SyncStreamPiece, 0, len(chunks))
	for _, chunk := range chunks {
		pieces = append(pieces, sender.makePiece(chunk))
	}

	var lastErr error
	for range streamMaxRetries {
		lastErr = sender.sendPieces(pieces, true)
		if lastErr == nil {
			return true
		}
		pieces = nil // pending pieces are already saved; drain on next iteration
	}

	logger().Warn("sync_stream failed after retries, falling back to sendmessage",
		"user_id", msg.FromUserID, "retries", streamMaxRetries, "error", lastErr)
	return false
}

const (
	// weixinMaxMessageLen is the maximum text message length for WeChat iLink.
	weixinMaxMessageLen = 2000

	// typingInterval is how often we re-send the typing indicator.
	// WeChat typing status expires after a few seconds.
	typingInterval = 5 * time.Second
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

// keepTyping sends typing indicators every 5 seconds until the context is cancelled.
// On cancel, it sends a stop-typing signal (status=2).
func (b *Bot) keepTyping(ctx context.Context, msg WeixinMessage) {
	if b.guard.IsPaused() {
		return
	}
	ticket := b.getTypingTicket(msg.FromUserID)
	if ticket == "" {
		return
	}

	// Send initial typing indicator.
	if err := b.client.SendTyping(msg.FromUserID, ticket, 1); err != nil {
		logger().Debug("typing start failed", "user_id", msg.FromUserID, "error", err)
	}

	ticker := time.NewTicker(typingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Send stop-typing.
			if err := b.client.SendTyping(msg.FromUserID, ticket, 2); err != nil {
				logger().Debug("typing stop failed", "user_id", msg.FromUserID, "error", err)
			}
			return
		case <-ticker.C:
			if err := b.client.SendTyping(msg.FromUserID, ticket, 1); err != nil {
				logger().Debug("typing refresh failed", "user_id", msg.FromUserID, "error", err)
			}
		}
	}
}

// getTypingTicket retrieves or fetches the typing_ticket for a user.
func (b *Bot) getTypingTicket(userID string) string {
	// Check cache first.
	if v, ok := b.typingTickets.Load(userID); ok {
		if ticket, ok := v.(string); ok && ticket != "" {
			return ticket
		}
	}

	// Fetch from API.
	contextToken := ""
	if v, ok := b.contextTokens.Load(userID); ok {
		contextToken, _ = v.(string)
	}

	resp, err := b.client.GetConfig(userID, contextToken)
	if err != nil {
		logger().Debug("getconfig for typing_ticket failed", "user_id", userID, "error", err)
		return ""
	}

	if resp.TypingTicket != "" {
		b.typingTickets.Store(userID, resp.TypingTicket)
	}

	return resp.TypingTicket
}
