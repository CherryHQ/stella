package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"

	"github.com/CherryHQ/stella/pkg/channel"
)

const (
	discordGatewayVersion = "10"
	gatewayReconnectMax   = 30 * time.Second
	gatewayReconnectBase  = time.Second
	gatewayDispatchQueue  = 64
	gatewayHandshakeQueue = 64
	gatewayStateTimeout   = 5 * time.Second
)

var (
	errGatewayReconnect     = errors.New("discord gateway requested reconnect")
	errGatewayInvalid       = errors.New("discord gateway session is invalid")
	errGatewayNoResumeState = errors.New("discord gateway resume state is incomplete")
)

// gatewayPacket is intentionally independent of discordgo's private websocket
// state. The library's Session.Open owns private sequence/session fields and
// automatically IDENTIFYs after INVALID_SESSION, which is unsafe once the
// cursor is durable. This packet is the small public wire envelope needed by
// the controlled transport below.
type gatewayPacket struct {
	Operation int             `json:"op"`
	Sequence  int64           `json:"s"`
	Type      string          `json:"t"`
	Data      json.RawMessage `json:"d"`
}

type gatewayHello struct {
	HeartbeatInterval int `json:"heartbeat_interval"`
}

type gatewayHeartbeat struct {
	Operation int   `json:"op"`
	Data      int64 `json:"d"`
}

type gatewayIdentify struct {
	Operation int                `json:"op"`
	Data      discordgo.Identify `json:"d"`
}

type gatewayResume struct {
	Operation int `json:"op"`
	Data      struct {
		Token     string `json:"token"`
		SessionID string `json:"session_id"`
		Sequence  int64  `json:"seq"`
	} `json:"d"`
}

type discordGateway struct {
	bot               *Bot
	state             channel.IngressSessionState
	stateStore        channel.IngressSessionStateStore
	conn              *websocket.Conn
	heartbeatInterval time.Duration
	lifecycleCtx      context.Context
	bufferMu          sync.RWMutex
	buffering         bool
	pending           []gatewayPacket
	activated         bool
	receiver          *gatewayReceiver

	writeMu           sync.Mutex
	connMu            sync.Mutex
	heartbeatMu       sync.Mutex
	lastHeartbeatSent time.Time
	lastHeartbeatAck  time.Time
	stopOnce          sync.Once
	stopCh            chan struct{}
}

type gatewayReceiver struct {
	ready  <-chan struct{}
	done   <-chan error
	cancel context.CancelFunc
}

func (g *discordGateway) setBuffering(buffering bool) {
	g.bufferMu.Lock()
	g.buffering = buffering
	g.bufferMu.Unlock()
}

func (g *discordGateway) setBufferingFalse() {
	g.setBuffering(false)
}

func (g *discordGateway) isBuffering() bool {
	g.bufferMu.RLock()
	defer g.bufferMu.RUnlock()
	return g.buffering
}

func newDiscordGateway(bot *Bot) (*discordGateway, error) {
	store, ok := bot.handler.(channel.IngressSessionStateStore)
	if !ok {
		return nil, errors.New("discord durable gateway session store is not configured")
	}
	return &discordGateway{bot: bot, stateStore: store, stopCh: make(chan struct{})}, nil
}

func (g *discordGateway) start(ctx context.Context) error {
	g.lifecycleCtx = ctx
	g.bot.cursor.setContext(ctx)
	if err := g.bot.cursor.load(ctx); err != nil {
		return err
	}
	state, err := g.stateStore.LoadIngressSessionState(ctx, channel.PlatformDiscord, g.bot.cursor.channelID, discordCursorStream)
	if err != nil {
		return fmt.Errorf("load Discord gateway session state: %w", err)
	}
	g.state = state
	if err := g.validateState(); err != nil {
		return err
	}
	if g.state.State == "ready" && !g.activated {
		if err := g.prepareResume(ctx); err != nil {
			return err
		}
	}
	return g.handshake(ctx)
}

func (g *discordGateway) prepareResume(ctx context.Context) error {
	identityCtx, cancel := context.WithTimeout(ctx, gatewayStateTimeout)
	defer cancel()
	user, err := g.bot.session.User("@me", discordgo.WithContext(identityCtx))
	if err != nil {
		return fmt.Errorf("load Discord bot identity before RESUME: %w", err)
	}
	if g.bot.session.State != nil {
		g.bot.session.State.User = user
	}
	if err := g.bot.activate(ctx); err != nil {
		return err
	}
	g.activated = true
	return nil
}

func (g *discordGateway) validateState() error {
	switch g.state.State {
	case "new":
		if g.state.SessionID != "" || g.state.ResumeGatewayURL != "" {
			return g.invalidate(fmt.Errorf("discord gateway state is new but contains a session"))
		}
		// A nonzero cursor without a resumable session means the process lost
		// the only protocol state that can safely continue that stream.
		if g.bot.cursor.current() > 0 {
			return g.invalidate(errGatewayNoResumeState)
		}
	case "ready":
		if g.state.SessionID == "" || g.state.ResumeGatewayURL == "" {
			return g.invalidate(errGatewayNoResumeState)
		}
	case "invalid":
		if g.state.LastError == "" {
			return errGatewayInvalid
		}
		return fmt.Errorf("%w: %s", errGatewayInvalid, g.state.LastError)
	default:
		return g.invalidate(fmt.Errorf("discord gateway state %q is unknown", g.state.State))
	}
	return nil
}

func (g *discordGateway) gatewayURL(ctx context.Context) (string, error) {
	base := g.state.ResumeGatewayURL
	if base == "" {
		response, err := g.bot.session.GatewayBot(discordgo.WithContext(ctx))
		if err != nil {
			return "", fmt.Errorf("get Discord gateway URL: %w", err)
		}
		base = response.URL
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse Discord gateway URL: %w", err)
	}
	query := u.Query()
	query.Set("v", discordGatewayVersion)
	query.Set("encoding", "json")
	// Do not request zlib-stream compression. A text frame keeps this small
	// transport independent of discordgo's private persistent inflater.
	query.Del("compress")
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func (g *discordGateway) handshake(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	endpoint, err := g.gatewayURL(ctx)
	if err != nil {
		return err
	}
	conn, response, err := g.bot.session.Dialer.DialContext(ctx, endpoint, http.Header{})
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("dial Discord gateway: %w", err)
	}
	g.setConn(conn)
	// websocket.Conn.ReadMessage does not observe ctx cancellation. Keep a
	// small watcher for the handshake itself; once the caller stops startup,
	// closing the socket is the only way to unblock a server that never sends
	// READY/RESUMED. The steady-state listener already closes the socket in its
	// context branch.
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-ctx.Done():
			g.closeConn()
		case <-watchDone:
		}
	}()
	g.heartbeatMu.Lock()
	g.lastHeartbeatSent = time.Time{}
	g.lastHeartbeatAck = time.Time{}
	g.heartbeatMu.Unlock()
	g.setBuffering(!g.activated)
	g.pending = nil
	defer g.setBufferingFalse()
	closeConn := true
	defer func() {
		if closeConn {
			g.closeConn()
		}
	}()

	packet, err := g.readPacket()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("read Discord gateway HELLO: %w", err)
	}
	if packet.Operation != 10 {
		return fmt.Errorf("expected Discord gateway HELLO, got op %d", packet.Operation)
	}
	var hello gatewayHello
	if err := json.Unmarshal(packet.Data, &hello); err != nil || hello.HeartbeatInterval <= 0 {
		if err == nil {
			err = errors.New("heartbeat interval is not positive")
		}
		return fmt.Errorf("decode Discord gateway HELLO: %w", err)
	}
	g.heartbeatInterval = time.Duration(hello.HeartbeatInterval) * time.Millisecond

	if g.state.State == "ready" {
		var resume gatewayResume
		resume.Operation = 6
		resume.Data.Token = g.bot.session.Token
		resume.Data.SessionID = g.state.SessionID
		resume.Data.Sequence = g.bot.cursor.current()
		if err := g.writeJSON(resume); err != nil {
			return fmt.Errorf("send Discord gateway RESUME: %w", err)
		}
	} else {
		identify := gatewayIdentify{Operation: 2, Data: g.bot.session.Identify}
		if err := g.writeJSON(identify); err != nil {
			return fmt.Errorf("send Discord gateway IDENTIFY: %w", err)
		}
	}

	// Heartbeats must continue while Discord waits for READY/RESUMED. The
	// heartbeat carries the last contiguous, durably admitted cursor, so a
	// crash cannot acknowledge a message whose FIFO admission was incomplete.
	if g.activated && g.state.State == "ready" {
		g.receiver = g.startResumeReceiver(ctx)
		select {
		case <-g.receiver.ready:
			closeConn = false
			return nil
		case err := <-g.receiver.done:
			return err
		case <-ctx.Done():
			g.receiver.cancel()
			return ctx.Err()
		}
	}
	heartbeatCtx, heartbeatCancel := context.WithCancel(ctx)
	defer heartbeatCancel()
	go g.heartbeat(heartbeatCtx, g.heartbeatInterval)

	for {
		packet, err := g.readPacket()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("read Discord gateway handshake: %w", err)
		}
		switch packet.Operation {
		case 0:
			ready, resumed, err := g.dispatch(packet)
			if err != nil {
				return err
			}
			if ready || resumed {
				closeConn = false
				return nil
			}
		case 1:
			if err := g.sendHeartbeat(); err != nil {
				return err
			}
		case 7:
			return errGatewayReconnect
		case 9:
			return g.invalidate(fmt.Errorf("%w: invalid session during handshake", errGatewayInvalid))
		case 11:
			g.markHeartbeatAck()
		}
	}
}

func (g *discordGateway) run(ctx context.Context) error {
	g.lifecycleCtx = ctx
	g.bot.cursor.setContext(ctx)
	if err := g.flushPending(ctx); err != nil {
		return err
	}
	backoff := gatewayReconnectBase
	for {
		err := g.receive(ctx)
		if err == nil || ctx.Err() != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return nil
		}
		g.closeConn()
		if errors.Is(err, errGatewayInvalid) {
			return err
		}
		if err := waitGatewayBackoff(ctx, backoff); err != nil {
			return err
		}
		if err := g.handshake(ctx); err != nil {
			if errors.Is(err, errGatewayInvalid) {
				return err
			}
			// Keep retrying transient network failures, but cap the delay so
			// an operator can recover a gateway outage without restarting Stella.
			if backoff < gatewayReconnectMax {
				backoff *= 2
				if backoff > gatewayReconnectMax {
					backoff = gatewayReconnectMax
				}
			}
			continue
		}
		if err := g.flushPending(ctx); err != nil {
			return err
		}
		backoff = gatewayReconnectBase
	}
}

func (g *discordGateway) listen(ctx context.Context) error {
	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeatCtx, heartbeatCancel := context.WithCancel(loopCtx)
	defer heartbeatCancel()
	go g.heartbeat(heartbeatCtx, g.heartbeatInterval)
	packets := make(chan gatewayPacket, gatewayDispatchQueue)
	readerDone := make(chan error, 1)
	workerDone := make(chan error, 1)
	go g.readLoop(loopCtx, packets, readerDone)
	go g.dispatchLoop(loopCtx, packets, workerDone)

	var first error
	select {
	case first = <-readerDone:
		// Let already-read application packets finish admission before the
		// listener returns. The queue is bounded, so this drain is finite.
		select {
		case workerErr := <-workerDone:
			if workerErr != nil && first == nil {
				first = workerErr
			}
		case <-ctx.Done():
			// A handler is allowed to be slow, but shutdown must not leave an
			// old dispatch worker racing a future listener. Give it the same
			// bounded lifecycle window as state writes, then stop waiting.
			g.closeConn()
			cancel()
			select {
			case <-workerDone:
			case <-time.After(gatewayStateTimeout):
				logger().Warn("Discord gateway dispatch worker did not stop before shutdown deadline")
			}
			first = ctx.Err()
		}
	case first = <-workerDone:
		if first == nil {
			first = <-readerDone
		}
	case <-ctx.Done():
		g.closeConn()
		cancel()
		select {
		case <-workerDone:
		case <-time.After(gatewayStateTimeout):
			logger().Warn("Discord gateway dispatch worker did not stop before shutdown deadline")
		}
		first = ctx.Err()
	}
	g.closeConn()
	cancel()
	return first
}

func (g *discordGateway) startResumeReceiver(ctx context.Context) *gatewayReceiver {
	receiverCtx, cancel := context.WithCancel(ctx)
	packets := make(chan gatewayPacket, gatewayDispatchQueue)
	readerDone := make(chan error, 1)
	workerDone := make(chan error, 1)
	ready := make(chan struct{})
	var readyOnce sync.Once
	go g.readLoop(receiverCtx, packets, readerDone)
	go func() {
		var result error
		defer func() { workerDone <- result }()
		for {
			select {
			case packet, ok := <-packets:
				if !ok {
					return
				}
				_, resumed, err := g.dispatch(packet)
				if err != nil {
					result = err
					return
				}
				if resumed {
					readyOnce.Do(func() { close(ready) })
				}
			case <-receiverCtx.Done():
				result = receiverCtx.Err()
				return
			}
		}
	}()
	done := make(chan error, 1)
	go func() {
		select {
		case readerErr := <-readerDone:
			workerErr := <-workerDone
			cancel()
			if workerErr != nil && readerErr == nil {
				done <- workerErr
				return
			}
			done <- readerErr
		case workerErr := <-workerDone:
			g.closeConn()
			cancel()
			readerErr := <-readerDone
			if workerErr != nil {
				done <- workerErr
				return
			}
			done <- readerErr
		}
	}()
	heartbeatCtx, heartbeatCancel := context.WithCancel(receiverCtx)
	go func() {
		defer heartbeatCancel()
		g.heartbeat(heartbeatCtx, g.heartbeatInterval)
	}()
	return &gatewayReceiver{ready: ready, done: done, cancel: func() {
		cancel()
		g.closeConn()
	}}
}

func (g *discordGateway) receive(ctx context.Context) error {
	if g.receiver == nil {
		return g.listen(ctx)
	}
	receiver := g.receiver
	select {
	case err := <-receiver.done:
		g.receiver = nil
		return err
	case <-ctx.Done():
		receiver.cancel()
		select {
		case err := <-receiver.done:
			g.receiver = nil
			if err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
		default:
		}
		return ctx.Err()
	}
}

func (g *discordGateway) readLoop(ctx context.Context, packets chan<- gatewayPacket, done chan<- error) {
	defer close(packets)
	var result error
	defer func() { done <- result }()
	for {
		packet, err := g.readPacket()
		if err != nil {
			if ctx.Err() != nil {
				result = ctx.Err()
			} else {
				result = fmt.Errorf("read Discord gateway event: %w", err)
			}
			return
		}
		switch packet.Operation {
		case 0:
			select {
			case packets <- packet:
			case <-ctx.Done():
				result = ctx.Err()
				return
			default:
				// A full queue means admission cannot keep up with the gateway.
				// Closing forces a reconnect from the last durable cursor instead
				// of spawning unbounded handler goroutines or dropping events.
				g.closeConn()
				result = errors.New("discord gateway dispatch queue is full")
				return
			}
		case 1:
			if err := g.sendHeartbeat(); err != nil {
				result = err
				return
			}
		case 7:
			result = errGatewayReconnect
			return
		case 9:
			result = g.invalidate(fmt.Errorf("%w: invalid session", errGatewayInvalid))
			return
		case 11:
			g.markHeartbeatAck()
		case 10:
			// Discord only sends HELLO on a newly opened connection. Ignore a
			// duplicate rather than treating it as an application dispatch.
		default:
			logger().Debug("ignored Discord gateway operation", "operation", packet.Operation)
		}
	}
}

func (g *discordGateway) dispatchLoop(ctx context.Context, packets <-chan gatewayPacket, done chan<- error) {
	var result error
	defer func() { done <- result }()
	for {
		select {
		case packet, ok := <-packets:
			if !ok {
				return
			}
			if _, _, err := g.dispatch(packet); err != nil {
				result = err
				return
			}
		case <-ctx.Done():
			result = ctx.Err()
			return
		}
	}
}

func (g *discordGateway) dispatch(packet gatewayPacket) (ready, resumed bool, err error) {
	event := &discordgo.Event{Operation: packet.Operation, Sequence: packet.Sequence, Type: packet.Type, RawData: packet.Data}
	switch packet.Type {
	case "READY":
		var value discordgo.Ready
		if err := json.Unmarshal(packet.Data, &value); err != nil {
			return false, false, fmt.Errorf("decode Discord READY: %w", err)
		}
		var metadata struct {
			ResumeGatewayURL string `json:"resume_gateway_url"`
		}
		if err := json.Unmarshal(packet.Data, &metadata); err != nil {
			return false, false, fmt.Errorf("decode Discord READY metadata: %w", err)
		}
		if value.SessionID == "" {
			return false, false, errors.New("discord READY has no session_id")
		}
		if metadata.ResumeGatewayURL == "" {
			metadata.ResumeGatewayURL = g.state.ResumeGatewayURL
		}
		if metadata.ResumeGatewayURL == "" {
			return false, false, errors.New("discord READY has no resume_gateway_url")
		}
		if g.bot.session.State != nil {
			g.bot.session.State.User = value.User
			for _, guild := range value.Guilds {
				if guild != nil {
					_ = g.bot.session.State.GuildAdd(guild)
				}
			}
		}
		g.state = channel.IngressSessionState{SessionID: value.SessionID, ResumeGatewayURL: metadata.ResumeGatewayURL, State: "ready"}
		stateCtx, cancel := g.stateWriteContext()
		err := g.saveState(stateCtx)
		cancel()
		if err != nil {
			return false, false, err
		}
		if g.isBuffering() {
			if err := g.buffer(packet); err != nil {
				return false, false, err
			}
			return true, false, nil
		}
		g.bot.cursor.observe(event)
		return true, false, nil
	case "RESUMED":
		// RESUMED has no user payload. Fetching @me restores the state needed
		// by mention and identity gates after a process restart.
		parent := g.lifecycleCtx
		if parent == nil {
			parent = context.Background()
		}
		identityCtx, cancel := context.WithTimeout(parent, gatewayStateTimeout)
		user, err := g.bot.session.User("@me", discordgo.WithContext(identityCtx))
		cancel()
		if err != nil {
			return false, false, fmt.Errorf("load Discord bot identity after RESUMED: %w", err)
		}
		if g.bot.session.State != nil {
			g.bot.session.State.User = user
		}
		if g.isBuffering() {
			if err := g.buffer(packet); err != nil {
				return false, false, err
			}
			return false, true, nil
		}
		g.bot.cursor.observe(event)
		return false, true, nil
	case "MESSAGE_CREATE":
		var value discordgo.Message
		if err := json.Unmarshal(packet.Data, &value); err != nil {
			return false, false, fmt.Errorf("decode Discord MESSAGE_CREATE: %w", err)
		}
		if g.isBuffering() {
			if err := g.buffer(packet); err != nil {
				return false, false, err
			}
			return false, false, nil
		}
		g.bot.cursor.observe(event)
		g.bot.onMessageCreate(g.bot.session, &discordgo.MessageCreate{Message: &value})
	case "INTERACTION_CREATE":
		var value discordgo.Interaction
		if err := json.Unmarshal(packet.Data, &value); err != nil {
			return false, false, fmt.Errorf("decode Discord INTERACTION_CREATE: %w", err)
		}
		if g.isBuffering() {
			if err := g.buffer(packet); err != nil {
				return false, false, err
			}
			return false, false, nil
		}
		g.bot.cursor.observe(event)
		g.bot.onInteractionCreate(g.bot.session, &discordgo.InteractionCreate{Interaction: &value})
	default:
		// Every dispatch advances the platform sequence. Unknown and ignored
		// events are safe to acknowledge immediately, while ingress events are
		// held by gatewayCursor until their admission callback finishes.
		if g.isBuffering() {
			if err := g.buffer(packet); err != nil {
				return false, false, err
			}
			return false, false, nil
		}
		g.bot.cursor.observe(event)
	}
	return false, false, nil
}

func (g *discordGateway) buffer(packet gatewayPacket) error {
	g.pending = append(g.pending, packet)
	if len(g.pending) < gatewayHandshakeQueue {
		return nil
	}
	if !g.activated {
		return errors.New("discord gateway handshake replay queue is full before activation")
	}
	if err := g.flushHandshakePending(); err != nil {
		return err
	}
	return nil
}

func (g *discordGateway) flushHandshakePending() error {
	pending := g.pending
	g.pending = nil
	g.setBuffering(false)
	defer func() { g.setBuffering(true) }()
	ctx := g.lifecycleCtx
	if ctx == nil {
		ctx = context.Background()
	}
	for _, packet := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, _, err := g.dispatch(packet); err != nil {
			return err
		}
	}
	return nil
}

func (g *discordGateway) flushPending(ctx context.Context) error {
	if len(g.pending) == 0 {
		return nil
	}
	pending := g.pending
	g.pending = nil
	for _, packet := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, _, err := g.dispatch(packet); err != nil {
			return err
		}
	}
	return nil
}

func (g *discordGateway) saveState(ctx context.Context) error {
	if err := g.stateStore.SaveIngressSessionState(ctx, channel.PlatformDiscord, g.bot.cursor.channelID, discordCursorStream, g.state); err != nil {
		return fmt.Errorf("save Discord gateway session state: %w", err)
	}
	return nil
}

func (g *discordGateway) stateWriteContext() (context.Context, context.CancelFunc) {
	parent := g.lifecycleCtx
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, gatewayStateTimeout)
}

func (g *discordGateway) invalidate(cause error) error {
	if cause == nil {
		cause = errGatewayInvalid
	}
	g.state.State = "invalid"
	g.state.LastError = cause.Error()
	stateCtx, cancel := g.stateWriteContext()
	err := g.saveState(stateCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("%w: %w; persist invalid state: %w", errGatewayInvalid, cause, err)
	}
	return cause
}

func (g *discordGateway) heartbeat(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	timer := time.NewTimer(time.Duration(rand.N(int64(interval))))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return
	case <-g.stopCh:
		return
	}
	for {
		sentAt := time.Now().UTC()
		g.heartbeatMu.Lock()
		g.lastHeartbeatSent = sentAt
		g.heartbeatMu.Unlock()
		if err := g.sendHeartbeat(); err != nil {
			logger().Debug("Discord gateway heartbeat failed", "error", err)
			return
		}
		timer.Reset(interval)
		select {
		case <-timer.C:
			g.heartbeatMu.Lock()
			acknowledged := !g.lastHeartbeatAck.Before(sentAt)
			g.heartbeatMu.Unlock()
			if !acknowledged {
				logger().Warn("Discord gateway heartbeat was not acknowledged; reconnecting")
				g.closeConn()
				return
			}
		case <-ctx.Done():
			return
		case <-g.stopCh:
			return
		}
	}
}

func (g *discordGateway) sendHeartbeat() error {
	sequence := g.bot.cursor.current()
	if err := g.writeJSON(gatewayHeartbeat{Operation: 1, Data: sequence}); err != nil {
		return fmt.Errorf("send Discord gateway heartbeat: %w", err)
	}
	g.bot.session.Lock()
	g.bot.session.LastHeartbeatSent = time.Now().UTC()
	g.bot.session.Unlock()
	return nil
}

func (g *discordGateway) markHeartbeatAck() {
	now := time.Now().UTC()
	g.heartbeatMu.Lock()
	g.lastHeartbeatAck = now
	g.heartbeatMu.Unlock()
	g.bot.session.Lock()
	g.bot.session.LastHeartbeatAck = now
	g.bot.session.Unlock()
}

func (g *discordGateway) readPacket() (gatewayPacket, error) {
	g.connMu.Lock()
	conn := g.conn
	g.connMu.Unlock()
	if conn == nil {
		return gatewayPacket{}, errors.New("discord gateway is not connected")
	}
	messageType, payload, err := conn.ReadMessage()
	if err != nil {
		return gatewayPacket{}, err
	}
	if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
		return gatewayPacket{}, fmt.Errorf("unexpected Discord gateway websocket message type %d", messageType)
	}
	var packet gatewayPacket
	if err := json.Unmarshal(payload, &packet); err != nil {
		return gatewayPacket{}, err
	}
	return packet, nil
}

func (g *discordGateway) writeJSON(value any) error {
	g.connMu.Lock()
	conn := g.conn
	g.connMu.Unlock()
	if conn == nil {
		return errors.New("discord gateway is not connected")
	}
	g.writeMu.Lock()
	defer g.writeMu.Unlock()
	return conn.WriteJSON(value)
}

func (g *discordGateway) setConn(conn *websocket.Conn) {
	g.connMu.Lock()
	g.conn = conn
	g.connMu.Unlock()
}

func (g *discordGateway) closeConn() {
	g.connMu.Lock()
	conn := g.conn
	g.conn = nil
	g.connMu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (g *discordGateway) stop() {
	g.stopOnce.Do(func() {
		close(g.stopCh)
		g.closeConn()
	})
}

func waitGatewayBackoff(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
