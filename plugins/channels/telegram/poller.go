package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	tele "gopkg.in/telebot.v4"

	"github.com/CherryHQ/stella/pkg/channel"
)

const telegramCursorStream = "updates"

// durablePoller owns the Telegram update offset. telebot's LongPoller updates
// LastUpdateID immediately before handing an update to Bot.Updates, so a
// process crash between those two operations loses the update on Telegram's
// next getUpdates call. This poller processes the update itself and advances
// the database cursor only after ProcessUpdate has returned without a handler
// error.
type durablePoller struct {
	store     channel.IngressCursorStore
	channelID string
	streamKey string
	limit     int
	timeout   time.Duration
	allowed   []string

	mu      sync.Mutex
	cursor  int64
	loaded  bool
	lastErr error
}

func newDurablePoller(store channel.IngressCursorStore, channelID string, timeout time.Duration, limit int, allowed []string) *durablePoller {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if limit < 0 {
		limit = 0
	}
	return &durablePoller{
		store: store, channelID: channelID, streamKey: telegramCursorStream,
		limit: limit, timeout: timeout, allowed: append([]string(nil), allowed...),
	}
}

func (p *durablePoller) load(ctx context.Context) error {
	if p.store == nil {
		return errors.New("telegram durable ingress cursor store is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cursor, err := p.store.LoadIngressCursor(ctx, channel.PlatformTelegram, p.channelID, p.streamKey)
	if err != nil {
		return fmt.Errorf("load telegram ingress cursor: %w", err)
	}
	p.mu.Lock()
	p.cursor = cursor
	p.loaded = true
	p.mu.Unlock()
	return nil
}

func (p *durablePoller) onError(err error, c tele.Context) {
	if err == nil {
		return
	}
	p.mu.Lock()
	p.lastErr = err
	p.mu.Unlock()
	if c != nil {
		logger().Warn("telegram update handler failed", "update_id", c.Update().ID, "error", err)
		return
	}
	logger().Warn("telegram poller failed", "error", err)
}

func (p *durablePoller) beginUpdate() {
	p.mu.Lock()
	p.lastErr = nil
	p.mu.Unlock()
}

func (p *durablePoller) updateError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastErr
}

func (p *durablePoller) currentCursor() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cursor
}

func (p *durablePoller) advance(ctx context.Context, updateID int) error {
	if updateID < 0 {
		return fmt.Errorf("telegram update id is negative: %d", updateID)
	}
	cursor := int64(updateID)
	if cursor <= p.currentCursor() {
		return nil
	}
	if err := p.store.AdvanceIngressCursor(ctx, channel.PlatformTelegram, p.channelID, p.streamKey, cursor); err != nil {
		return fmt.Errorf("advance telegram ingress cursor to %d: %w", cursor, err)
	}
	p.mu.Lock()
	if cursor > p.cursor {
		p.cursor = cursor
	}
	p.mu.Unlock()
	return nil
}

type telegramGetUpdatesResponse struct {
	Result []tele.Update `json:"result"`
}

func (p *durablePoller) fetch(b *tele.Bot) ([]tele.Update, error) {
	params := map[string]string{
		"offset":          strconv.FormatInt(p.currentCursor()+1, 10),
		"timeout":         strconv.Itoa(int(p.timeout / time.Second)),
		"allowed_updates": mustJSON(p.allowed),
	}
	if p.limit != 0 {
		params["limit"] = strconv.Itoa(p.limit)
	}
	data, err := b.Raw("getUpdates", params)
	if err != nil {
		return nil, err
	}
	var response telegramGetUpdatesResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode Telegram getUpdates response: %w", err)
	}
	return response.Result, nil
}

func mustJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(data)
}

func (p *durablePoller) process(ctx context.Context, b *tele.Bot, update tele.Update) error {
	p.beginUpdate()
	b.ProcessUpdate(update)
	if err := p.updateError(); err != nil {
		return err
	}
	if err := p.advance(ctx, update.ID); err != nil {
		return err
	}
	return nil
}

func (p *durablePoller) Poll(b *tele.Bot, _ chan tele.Update, stop chan struct{}) {
	if !p.isLoaded() {
		if err := p.load(context.Background()); err != nil {
			p.onError(err, nil)
			return
		}
	}
	for {
		select {
		case <-stop:
			return
		default:
		}

		updates, err := p.fetch(b)
		if err != nil {
			p.onError(err, nil)
			if !waitPollRetry(stop) {
				return
			}
			continue
		}
		for _, update := range updates {
			select {
			case <-stop:
				return
			default:
			}
			if update.ID <= int(p.currentCursor()) {
				continue
			}
			if err := p.process(context.Background(), b, update); err != nil {
				p.onError(err, nil)
				if !waitPollRetry(stop) {
					return
				}
				break
			}
		}
	}
}

func (p *durablePoller) isLoaded() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.loaded
}

func waitPollRetry(stop chan struct{}) bool {
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-stop:
		return false
	case <-timer.C:
		return true
	}
}
