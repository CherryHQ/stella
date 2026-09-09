package discord

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"

	"github.com/CherryHQ/stella/pkg/channel"
)

type gatewayStateStore struct {
	mu       sync.Mutex
	cursor   int64
	state    channel.IngressSessionState
	advances []int64
}

func (s *gatewayStateStore) LoadIngressCursor(context.Context, string, string, string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor, nil
}

func (s *gatewayStateStore) AdvanceIngressCursor(_ context.Context, _ string, _ string, _ string, cursor int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cursor > s.cursor {
		s.cursor = cursor
	}
	s.advances = append(s.advances, cursor)
	return nil
}

func (s *gatewayStateStore) LoadIngressSessionState(context.Context, string, string, string) (channel.IngressSessionState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, nil
}

func (s *gatewayStateStore) SaveIngressSessionState(_ context.Context, _ string, _ string, _ string, state channel.IngressSessionState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = state
	return nil
}

type gatewayTestHandler struct {
	fakeHandler
	*gatewayStateStore
}

type failingGatewayHandler struct {
	gatewayTestHandler
}

func (failingGatewayHandler) HandleIncoming(context.Context, channel.IncomingMessage, string, string) (string, bool, *channel.ChatStream, error) {
	return "", false, nil, errors.New("durable admission failed")
}

type blockingGatewayHandler struct {
	gatewayTestHandler
	started chan struct{}
	release chan struct{}
}

type replayRecordingHandler struct {
	gatewayTestHandler
	mu    sync.Mutex
	calls int
}

func (h *replayRecordingHandler) HandleIncoming(context.Context, channel.IncomingMessage, string, string) (string, bool, *channel.ChatStream, error) {
	h.mu.Lock()
	h.calls++
	h.mu.Unlock()
	return "ok", true, nil, nil
}

func (h *blockingGatewayHandler) HandleIncoming(context.Context, channel.IncomingMessage, string, string) (string, bool, *channel.ChatStream, error) {
	select {
	case <-h.started:
	default:
		close(h.started)
	}
	<-h.release
	return "ok", true, nil, nil
}

type gatewayRoundTripper struct{}

func (gatewayRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"bot","username":"stella"}`)),
	}, nil
}

type gatewayURLRoundTripper struct{ url string }

func (t gatewayURLRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"url":"` + t.url + `"}`)),
	}, nil
}

func TestGatewayRebuildUsesPersistedResumeSessionAndCursor(t *testing.T) {
	store := &gatewayStateStore{
		cursor: 42,
		state: channel.IngressSessionState{
			SessionID:        "session-from-previous-process",
			ResumeGatewayURL: "",
			State:            "ready",
		},
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	packets := make(chan gatewayPacket, 2)
	connections := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade gateway websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		if err := conn.WriteJSON(gatewayPacket{Operation: 10, Data: json.RawMessage(`{"heartbeat_interval":60000}`)}); err != nil {
			t.Errorf("write gateway HELLO: %v", err)
			return
		}
		var packet gatewayPacket
		if err := conn.ReadJSON(&packet); err != nil {
			t.Errorf("read gateway handshake packet: %v", err)
			return
		}
		packets <- packet
		if packet.Operation != 6 {
			return
		}
		if err := conn.WriteJSON(gatewayPacket{Operation: 0, Type: "RESUMED", Data: json.RawMessage(`{}`)}); err != nil {
			t.Errorf("write gateway RESUMED: %v", err)
			return
		}
		connections <- struct{}{}
		// Keep the transport open until the test stops this process instance.
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	store.state.ResumeGatewayURL = "ws" + strings.TrimPrefix(server.URL, "http")

	newProcess := func() *discordGateway {
		handler := &gatewayTestHandler{gatewayStateStore: store}
		bot, err := New(Config{InstanceID: "discord-main", Token: "token"}, handler)
		if err != nil {
			t.Fatal(err)
		}
		bot.session.Client = &http.Client{Transport: gatewayRoundTripper{}}
		gateway, err := newDiscordGateway(bot)
		if err != nil {
			t.Fatal(err)
		}
		return gateway
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first := newProcess()
	if err := first.start(ctx); err != nil {
		t.Fatal(err)
	}
	first.stop()

	second := newProcess()
	if err := second.start(ctx); err != nil {
		t.Fatal(err)
	}
	second.stop()

	for i := range 2 {
		select {
		case <-connections:
		case <-ctx.Done():
			t.Fatalf("gateway connection %d was not established: %v", i+1, ctx.Err())
		}
	}
	for i := range 2 {
		select {
		case packet := <-packets:
			if packet.Operation != 6 {
				t.Fatalf("process %d operation = %d, want OP6 RESUME", i+1, packet.Operation)
			}
			var resume struct {
				Token     string `json:"token"`
				SessionID string `json:"session_id"`
				Sequence  int64  `json:"seq"`
			}
			if err := json.Unmarshal(packet.Data, &resume); err != nil {
				t.Fatal(err)
			}
			if resume.Token != "Bot token" || resume.SessionID != "session-from-previous-process" || resume.Sequence != 42 {
				t.Fatalf("process %d resume = %#v, want persisted token/session/seq", i+1, resume)
			}
		case <-ctx.Done():
			t.Fatalf("resume packet %d was not received: %v", i+1, ctx.Err())
		}
	}
}

func TestGatewayStartCancellationClosesStalledHandshake(t *testing.T) {
	store := &gatewayStateStore{state: channel.IngressSessionState{State: "new"}}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	identified := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade gateway websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		if err := conn.WriteJSON(gatewayPacket{Operation: 10, Data: json.RawMessage(`{"heartbeat_interval":60000}`)}); err != nil {
			t.Errorf("write gateway HELLO: %v", err)
			return
		}
		var identify gatewayPacket
		if err := conn.ReadJSON(&identify); err != nil {
			return
		}
		identified <- struct{}{}
		// Keep the socket open without sending READY. Start must still honor
		// cancellation instead of waiting forever in ReadMessage.
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	bot, err := New(Config{InstanceID: "discord-main", Token: "token"}, &gatewayTestHandler{gatewayStateStore: store})
	if err != nil {
		t.Fatal(err)
	}
	bot.session.Client = &http.Client{Transport: gatewayURLRoundTripper{url: "ws" + strings.TrimPrefix(server.URL, "http")}}
	gateway, err := newDiscordGateway(bot)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- gateway.start(ctx) }()
	select {
	case <-identified:
	case <-time.After(2 * time.Second):
		gateway.stop()
		t.Fatal("gateway did not send IDENTIFY")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("gateway.start() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		gateway.stop()
		t.Fatal("gateway.start() did not stop after context cancellation")
	}
	gateway.stop()
}

func TestGatewayInvalidSessionPersistsBlockAndNeverIdentifies(t *testing.T) {
	store := &gatewayStateStore{
		cursor: 42,
		state: channel.IngressSessionState{
			SessionID:        "session-that-discord-rejected",
			ResumeGatewayURL: "",
			State:            "ready",
		},
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	var connections int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connections++
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade gateway websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteJSON(gatewayPacket{Operation: 10, Data: json.RawMessage(`{"heartbeat_interval":60000}`)})
		var packet gatewayPacket
		if err := conn.ReadJSON(&packet); err != nil {
			t.Errorf("read gateway resume: %v", err)
			return
		}
		_ = conn.WriteJSON(gatewayPacket{Operation: 9, Data: json.RawMessage(`false`)})
	}))
	defer server.Close()
	store.state.ResumeGatewayURL = "ws" + strings.TrimPrefix(server.URL, "http")

	newGateway := func() *discordGateway {
		handler := &gatewayTestHandler{gatewayStateStore: store}
		bot, err := New(Config{InstanceID: "discord-main", Token: "token"}, handler)
		if err != nil {
			t.Fatal(err)
		}
		bot.session.Client = &http.Client{Transport: gatewayRoundTripper{}}
		gateway, err := newDiscordGateway(bot)
		if err != nil {
			t.Fatal(err)
		}
		return gateway
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first := newGateway()
	if err := first.start(ctx); err == nil || !errors.Is(err, errGatewayInvalid) {
		t.Fatalf("invalid session start error = %v, want errGatewayInvalid", err)
	}
	store.mu.Lock()
	state := store.state
	store.mu.Unlock()
	if state.State != "invalid" || state.LastError == "" {
		t.Fatalf("invalid session state = %#v, want persisted invalid state and reason", state)
	}
	if connections != 1 {
		t.Fatalf("gateway connections = %d, want one", connections)
	}

	second := newGateway()
	if err := second.start(ctx); err == nil || !errors.Is(err, errGatewayInvalid) {
		t.Fatalf("blocked restart error = %v, want errGatewayInvalid", err)
	}
	if connections != 1 {
		t.Fatalf("blocked restart opened %d gateway connections, want one", connections)
	}
}

func TestGatewayAdmissionFailureDoesNotAdvanceCursor(t *testing.T) {
	store := &gatewayStateStore{
		cursor: 42,
		state:  channel.IngressSessionState{State: "ready", SessionID: "session", ResumeGatewayURL: "ws://gateway.invalid"},
	}
	handler := &failingGatewayHandler{gatewayTestHandler: gatewayTestHandler{gatewayStateStore: store}}
	bot, err := New(Config{InstanceID: "discord-main", Token: "token", AllowDM: true}, handler)
	if err != nil {
		t.Fatal(err)
	}
	bot.rest = newFakeDiscordREST()
	bot.ctx = context.Background()
	if err := bot.cursor.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	gateway := &discordGateway{bot: bot, stateStore: store}
	message := discordgo.Message{ID: "message", ChannelID: "dm", Author: &discordgo.User{ID: "sender"}, Content: "hello"}
	payload, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := gateway.dispatch(gatewayPacket{Operation: 0, Sequence: 43, Type: "MESSAGE_CREATE", Data: payload}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.cursor != 42 || len(store.advances) != 0 {
		t.Fatalf("failed admission advanced cursor: cursor=%d advances=%v", store.cursor, store.advances)
	}
}

func TestGatewayReadsHeartbeatAcksWhileAdmissionIsBlocked(t *testing.T) {
	store := &gatewayStateStore{
		cursor: 42,
		state:  channel.IngressSessionState{State: "ready", SessionID: "session", ResumeGatewayURL: "ws://gateway.invalid"},
	}
	handler := &blockingGatewayHandler{
		gatewayTestHandler: gatewayTestHandler{gatewayStateStore: store},
		started:            make(chan struct{}),
		release:            make(chan struct{}),
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	connections := make(chan int, 4)
	heartbeats := make(chan struct{}, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection := len(connections) + 1
		connections <- connection
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade gateway websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		if err := conn.WriteJSON(gatewayPacket{Operation: 10, Data: json.RawMessage(`{"heartbeat_interval":50}`)}); err != nil {
			t.Errorf("write gateway HELLO: %v", err)
			return
		}
		var packet gatewayPacket
		if err := conn.ReadJSON(&packet); err != nil {
			t.Errorf("read gateway resume: %v", err)
			return
		}
		if err := conn.WriteJSON(gatewayPacket{Operation: 0, Type: "RESUMED", Data: json.RawMessage(`{}`)}); err != nil {
			t.Errorf("write gateway RESUMED: %v", err)
			return
		}
		if connection == 1 {
			message := gatewayPacket{Operation: 0, Sequence: 43, Type: "MESSAGE_CREATE", Data: json.RawMessage(`{"id":"message","channel_id":"dm","content":"hello","author":{"id":"sender"}}`)}
			if err := conn.WriteJSON(message); err != nil {
				t.Errorf("write gateway message: %v", err)
				return
			}
		}
		for {
			var packet gatewayPacket
			if err := conn.ReadJSON(&packet); err != nil {
				return
			}
			if packet.Operation == 1 {
				heartbeats <- struct{}{}
				_ = conn.WriteJSON(gatewayPacket{Operation: 11, Data: json.RawMessage(`null`)})
			}
		}
	}))
	defer server.Close()
	store.state.ResumeGatewayURL = "ws" + strings.TrimPrefix(server.URL, "http")
	bot, err := New(Config{InstanceID: "discord-main", Token: "token", AllowDM: true}, handler)
	if err != nil {
		t.Fatal(err)
	}
	bot.session.Client = &http.Client{Transport: gatewayRoundTripper{}}
	bot.rest = newFakeDiscordREST()
	bot.ctx = context.Background()
	gateway, err := newDiscordGateway(bot)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := gateway.start(ctx); err != nil {
		t.Fatal(err)
	}
	runErr := make(chan error, 1)
	go func() { runErr <- gateway.run(ctx) }()
	select {
	case <-handler.started:
	case <-time.After(2 * time.Second):
		t.Fatal("message admission did not block")
	}
	// Four heartbeat periods pass while the application worker is blocked. A
	// reader tied to dispatch would misclassify this as a dead connection.
	time.Sleep(250 * time.Millisecond)
	if got := len(connections); got != 1 {
		t.Fatalf("gateway connections while admission blocked = %d, want one", got)
	}
	if got := len(heartbeats); got < 3 {
		t.Fatalf("heartbeat packets while admission blocked = %d, want at least 3", got)
	}
	close(handler.release)
	cancel()
	gateway.stop()
	select {
	case <-runErr:
	case <-time.After(2 * time.Second):
		t.Fatal("gateway run did not stop")
	}
}

func TestGatewayReconnectsWhenHeartbeatAckIsMissing(t *testing.T) {
	store := &gatewayStateStore{
		cursor: 42,
		state:  channel.IngressSessionState{State: "ready", SessionID: "session", ResumeGatewayURL: "ws://gateway.invalid"},
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	connections := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade gateway websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteJSON(gatewayPacket{Operation: 10, Data: json.RawMessage(`{"heartbeat_interval":10}`)})
		var packet gatewayPacket
		if err := conn.ReadJSON(&packet); err != nil {
			return
		}
		_ = conn.WriteJSON(gatewayPacket{Operation: 0, Type: "RESUMED", Data: json.RawMessage(`{}`)})
		connections <- struct{}{}
		// Do not send OP11. The client must close and reconnect from its
		// durable cursor rather than waiting forever on this socket.
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	store.state.ResumeGatewayURL = "ws" + strings.TrimPrefix(server.URL, "http")
	bot, err := New(Config{InstanceID: "discord-main", Token: "token"}, &gatewayTestHandler{gatewayStateStore: store})
	if err != nil {
		t.Fatal(err)
	}
	bot.session.Client = &http.Client{Transport: gatewayRoundTripper{}}
	gateway, err := newDiscordGateway(bot)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := gateway.start(ctx); err != nil {
		t.Fatal(err)
	}
	runErr := make(chan error, 1)
	go func() { runErr <- gateway.run(ctx) }()
	for i := range 2 {
		select {
		case <-connections:
		case <-ctx.Done():
			t.Fatalf("gateway reconnect %d was not observed: %v", i+1, ctx.Err())
		}
	}
	gateway.stop()
	cancel()
	select {
	case <-runErr:
	case <-time.After(2 * time.Second):
		t.Fatal("gateway run did not stop after heartbeat reconnect")
	}
}

func TestGatewayStartBuffersReplayBeforeResumedUntilActivation(t *testing.T) {
	store := &gatewayStateStore{
		cursor: 42,
		state:  channel.IngressSessionState{State: "ready", SessionID: "session", ResumeGatewayURL: "ws://gateway.invalid"},
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	connected := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade gateway websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteJSON(gatewayPacket{Operation: 10, Data: json.RawMessage(`{"heartbeat_interval":60000}`)})
		var resume gatewayPacket
		if err := conn.ReadJSON(&resume); err != nil {
			t.Errorf("read gateway resume: %v", err)
			return
		}
		_ = conn.WriteJSON(gatewayPacket{Operation: 0, Sequence: 43, Type: "MESSAGE_CREATE", Data: json.RawMessage(`{"id":"message","channel_id":"dm","content":"hello","author":{"id":"sender"}}`)})
		_ = conn.WriteJSON(gatewayPacket{Operation: 0, Sequence: 44, Type: "RESUMED", Data: json.RawMessage(`{}`)})
		connected <- struct{}{}
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	store.state.ResumeGatewayURL = "ws" + strings.TrimPrefix(server.URL, "http")
	handler := &replayRecordingHandler{gatewayTestHandler: gatewayTestHandler{gatewayStateStore: store}}
	bot, err := New(Config{InstanceID: "discord-main", Token: "token", AllowDM: true}, handler)
	if err != nil {
		t.Fatal(err)
	}
	bot.session.Client = &http.Client{Transport: gatewayRoundTripper{}}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- bot.Start(ctx) }()
	select {
	case <-handlerCall(handler):
	case <-ctx.Done():
		t.Fatalf("replayed message was not admitted: %v", ctx.Err())
	}
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatalf("gateway was not connected: %v", ctx.Err())
	}
	// `connected` only proves the mock server wrote RESUMED, not that this
	// process observed it and persisted the cursor. Poll the durable cursor.
	for {
		store.mu.Lock()
		cursor := store.cursor
		store.mu.Unlock()
		if cursor == 44 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("cursor after replay admission = %d, want 44", cursor)
		case <-time.After(time.Millisecond):
		}
	}
	handler.mu.Lock()
	calls := handler.calls
	handler.mu.Unlock()
	if calls != 1 {
		t.Fatalf("replayed message admissions = %d, want one", calls)
	}
	cancel()
	select {
	case <-runErr:
	case <-time.After(2 * time.Second):
		t.Fatal("Discord Start did not stop")
	}
}

func TestGatewayStreamsLargeResumeReplayWithBoundedPending(t *testing.T) {
	store := &gatewayStateStore{
		cursor: 42,
		state:  channel.IngressSessionState{State: "ready", SessionID: "session", ResumeGatewayURL: "ws://gateway.invalid"},
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade gateway websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteJSON(gatewayPacket{Operation: 10, Data: json.RawMessage(`{"heartbeat_interval":60000}`)})
		var resume gatewayPacket
		if err := conn.ReadJSON(&resume); err != nil {
			t.Errorf("read gateway resume: %v", err)
			return
		}
		for i := range int64(80) {
			message := gatewayPacket{
				Operation: 0,
				Sequence:  43 + i,
				Type:      "MESSAGE_CREATE",
				Data:      json.RawMessage(`{"id":"message","channel_id":"dm","content":"hello","author":{"id":"sender"}}`),
			}
			if err := conn.WriteJSON(message); err != nil {
				t.Errorf("write replay message %d: %v", i, err)
				return
			}
		}
		_ = conn.WriteJSON(gatewayPacket{Operation: 0, Sequence: 123, Type: "RESUMED", Data: json.RawMessage(`{}`)})
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	store.state.ResumeGatewayURL = "ws" + strings.TrimPrefix(server.URL, "http")
	handler := &replayRecordingHandler{gatewayTestHandler: gatewayTestHandler{gatewayStateStore: store}}
	bot, err := New(Config{InstanceID: "discord-main", Token: "token", AllowDM: true}, handler)
	if err != nil {
		t.Fatal(err)
	}
	bot.session.Client = &http.Client{Transport: gatewayRoundTripper{}}
	bot.rest = newFakeDiscordREST()
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- bot.Start(ctx) }()
	deadline := time.NewTimer(6 * time.Second)
	defer deadline.Stop()
	for {
		handler.mu.Lock()
		calls := handler.calls
		handler.mu.Unlock()
		if calls == 80 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatalf("resume replay admissions = %d, want 80", calls)
		case <-time.After(time.Millisecond):
		}
	}
	// Admitting the last replay message does not prove RESUMED itself was
	// observed and persisted; poll the durable cursor for sequence 123.
	for {
		store.mu.Lock()
		cursor := store.cursor
		store.mu.Unlock()
		if cursor == 123 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatalf("cursor after 80-event replay = %d, want 123", cursor)
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-runErr:
	case <-time.After(2 * time.Second):
		t.Fatal("Discord Start did not stop after large replay")
	}
}

func handlerCall(h *replayRecordingHandler) <-chan struct{} {
	called := make(chan struct{}, 1)
	go func() {
		for {
			h.mu.Lock()
			calls := h.calls
			h.mu.Unlock()
			if calls > 0 {
				called <- struct{}{}
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	return called
}

func TestGatewayFullDispatchQueueRemainsCancellable(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade gateway websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		if err := conn.WriteJSON(gatewayPacket{Operation: 0, Sequence: 43, Type: "MESSAGE_CREATE", Data: json.RawMessage(`{}`)}); err != nil {
			t.Errorf("write queued dispatch: %v", err)
			return
		}
		<-ctx.Done()
	}))
	t.Cleanup(func() {
		cancel()
		server.Close()
	})
	conn, response, err := websocket.DefaultDialer.DialContext(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	gateway := &discordGateway{conn: conn}
	packets := make(chan gatewayPacket, 1)
	packets <- gatewayPacket{}
	done := make(chan error, 1)
	go gateway.readLoop(ctx, packets, done)
	select {
	case err := <-done:
		t.Fatalf("full queue terminated the reader before cancellation: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("reader cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("full dispatch queue prevented reader cancellation")
	}
}
