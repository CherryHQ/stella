package qq

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tencent-connect/botgo/dto"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/channel"
)

func newTestHTTPServer(t *testing.T, handler http.Handler) (srv *httptest.Server) {
	t.Helper()

	defer func() {
		if r := recover(); r != nil {
			t.Skipf("local test server unavailable: %v", r)
		}
	}()

	if port := os.Getenv("PORT"); port != "" {
		ln, err := net.Listen("tcp", "127.0.0.1:"+port)
		if err != nil {
			t.Skipf("listen on PORT=%q: %v", port, err)
		}
		srv = httptest.NewUnstartedServer(handler)
		srv.Listener = ln
		srv.Start()
		return srv
	}

	return httptest.NewServer(handler)
}

// --- SplitMessage (shared) ---

func TestSplitMessageShort(t *testing.T) {
	chunks := channel.SplitMessage("hello", qqMaxMessageLen)
	if len(chunks) != 1 || chunks[0] != "hello" {
		t.Errorf("chunks = %v, want [hello]", chunks)
	}
}

func TestSplitMessageExactLimit(t *testing.T) {
	msg := strings.Repeat("a", qqMaxMessageLen)
	chunks := channel.SplitMessage(msg, qqMaxMessageLen)
	if len(chunks) != 1 {
		t.Errorf("len(chunks) = %d, want 1", len(chunks))
	}
}

func TestSplitMessageLong(t *testing.T) {
	msg := strings.Repeat("a", qqMaxMessageLen+100)
	chunks := channel.SplitMessage(msg, qqMaxMessageLen)
	if len(chunks) != 2 {
		t.Fatalf("len(chunks) = %d, want 2", len(chunks))
	}
	if len(chunks[0]) != qqMaxMessageLen {
		t.Errorf("chunk[0] len = %d, want %d", len(chunks[0]), qqMaxMessageLen)
	}
	if len(chunks[1]) != 100 {
		t.Errorf("chunk[1] len = %d, want 100", len(chunks[1]))
	}
}

func TestSplitMessageAtNewline(t *testing.T) {
	part1 := strings.Repeat("a", 3000)
	part2 := strings.Repeat("b", 2000)
	msg := part1 + "\n" + part2

	chunks := channel.SplitMessage(msg, qqMaxMessageLen)
	if len(chunks) != 2 {
		t.Fatalf("len(chunks) = %d, want 2", len(chunks))
	}
	if chunks[0] != part1+"\n" {
		t.Errorf("chunk[0] should end at newline")
	}
}

func TestSplitMessageMultibyteUTF8(t *testing.T) {
	char := "中"
	msg := strings.Repeat(char, qqMaxMessageLen) // 3*qqMaxMessageLen bytes
	chunks := channel.SplitMessage(msg, qqMaxMessageLen)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 0 && c[0]&0xC0 == 0x80 {
			t.Errorf("chunk[%d] starts with UTF-8 continuation byte", i)
		}
	}
}

func TestSplitMessageEmpty(t *testing.T) {
	chunks := channel.SplitMessage("", qqMaxMessageLen)
	if len(chunks) != 1 || chunks[0] != "" {
		t.Errorf("empty message should return single empty chunk, got %v", chunks)
	}
}

// --- buildStreamDisplay ---

func TestBuildStreamDisplayShort(t *testing.T) {
	d := buildStreamDisplay("hello", "")
	if !strings.HasSuffix(d, typingCursor) {
		t.Errorf("display should end with cursor, got %q", d)
	}
	if !strings.HasPrefix(d, "hello") {
		t.Errorf("display should start with text, got %q", d)
	}
}

func TestBuildStreamDisplayWithTool(t *testing.T) {
	d := buildStreamDisplay("hello", "⚡ bash: ls")
	if !strings.Contains(d, "⚡ bash: ls") {
		t.Errorf("display should contain tool line, got %q", d)
	}
}

func TestBuildStreamDisplayTruncates(t *testing.T) {
	long := strings.Repeat("x", qqMaxMessageLen+500)
	d := buildStreamDisplay(long, "")
	if len(d) > qqMaxMessageLen {
		t.Errorf("display len = %d, should be <= %d", len(d), qqMaxMessageLen)
	}
	if !strings.Contains(d, "...") {
		t.Errorf("truncated display should contain ellipsis")
	}
}

func TestBuildStreamDisplayEmptyTextWithTool(t *testing.T) {
	d := buildStreamDisplay("", "⚡ bash: ls")
	if !strings.Contains(d, "⚡ bash: ls") {
		t.Errorf("should show tool line: %q", d)
	}
}

func TestBuildStreamDisplayEmptyAll(t *testing.T) {
	d := buildStreamDisplay("", "")
	if !strings.HasSuffix(d, typingCursor) {
		t.Errorf("should end with cursor: %q", d)
	}
}

func TestBuildStreamDisplayLongSuffix(t *testing.T) {
	// Suffix (tool + cursor) exceeds qqMaxMessageLen → suffix resets to just cursor.
	longTool := strings.Repeat("x", qqMaxMessageLen+100)
	d := buildStreamDisplay("text", longTool)
	if !strings.HasSuffix(d, typingCursor) {
		t.Errorf("expected typingCursor suffix, got %q", d)
	}
}

// --- toolLine ---

func TestToolLineRunning(t *testing.T) {
	line := channel.ToolLine(&channel.ToolUseEvent{Tool: "bash", Status: "running", Input: "ls -la"})
	if !strings.Contains(line, "bash") || !strings.Contains(line, "ls -la") {
		t.Errorf("unexpected line: %q", line)
	}
}

func TestToolLineRunningTruncatesMultibyte(t *testing.T) {
	input := strings.Repeat("中", 61)
	line := channel.ToolLine(&channel.ToolUseEvent{Tool: "bash", Status: "running", Input: input})
	if !strings.HasSuffix(line, "...") {
		t.Errorf("expected truncation ellipsis, got %q", line)
	}
}

func TestToolLineRunningNoInput(t *testing.T) {
	line := channel.ToolLine(&channel.ToolUseEvent{Tool: "search", Status: "running"})
	if !strings.Contains(line, "search") {
		t.Errorf("unexpected line: %q", line)
	}
	if strings.Contains(line, ":") {
		t.Errorf("no input should not have colon separator: %q", line)
	}
}

func TestToolLineError(t *testing.T) {
	line := channel.ToolLine(&channel.ToolUseEvent{Tool: "read", Status: "error"})
	if !strings.Contains(line, "failed") {
		t.Errorf("unexpected line: %q", line)
	}
}

func TestToolLineDefault(t *testing.T) {
	line := channel.ToolLine(&channel.ToolUseEvent{Tool: "bash", Status: "done"})
	if line != "" {
		t.Errorf("expected empty, got %q", line)
	}
}

func TestToolLineUnknownTool(t *testing.T) {
	line := channel.ToolLine(&channel.ToolUseEvent{Tool: "custom_tool", Status: "running", Input: "x"})
	if !strings.Contains(line, "🔧") {
		t.Errorf("unknown tool should use default emoji: %q", line)
	}
}

// --- FilterModels (shared) ---

func TestFilterModels(t *testing.T) {
	models := []channel.ModelOption{
		{Provider: "openai", Model: "gpt-4"},
		{Provider: "anthropic", Model: "claude-3"},
		{Provider: "openai", Model: "gpt-3.5"},
	}

	result := channel.FilterModels(models, "openai")
	if len(result) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(result))
	}
	if result[0].GlobalIdx != 1 || result[1].GlobalIdx != 3 {
		t.Errorf("global indices should be 1 and 3, got %d and %d", result[0].GlobalIdx, result[1].GlobalIdx)
	}
}

func TestFilterModelsNoMatch(t *testing.T) {
	models := []channel.ModelOption{{Provider: "openai", Model: "gpt-4"}}
	result := channel.FilterModels(models, "gemini")
	if len(result) != 0 {
		t.Errorf("expected 0 matches, got %d", len(result))
	}
}

func TestFilterModelsCaseInsensitive(t *testing.T) {
	models := []channel.ModelOption{{Provider: "Anthropic", Model: "Claude-3"}}
	result := channel.FilterModels(models, "CLAUDE")
	if len(result) != 1 {
		t.Fatalf("expected 1 match, got %d", len(result))
	}
}

// --- New ---

func TestNewValidConfig(t *testing.T) {
	cfg := Config{AppID: "123", AppSecret: "secret"}
	bot, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bot == nil {
		t.Fatal("expected bot, got nil")
		return
	}
}

func TestNewMissingAppID(t *testing.T) {
	_, err := New(Config{AppSecret: "secret"}, nil)
	if err == nil {
		t.Fatal("expected error for missing app_id")
	}
}

func TestNewMissingAppSecret(t *testing.T) {
	_, err := New(Config{AppID: "123"}, nil)
	if err == nil {
		t.Fatal("expected error for missing app_secret")
	}
}

// --- channelForC2C / channelForGroup ---

func TestChannelForC2C(t *testing.T) {
	if got := channelForC2C("user123"); got != "qq:c2c:user123" {
		t.Errorf("channelForC2C = %q", got)
	}
}

func TestChannelForGroup(t *testing.T) {
	if got := channelForGroup("group456"); got != "qq:group:group456" {
		t.Errorf("channelForGroup = %q", got)
	}
}

// --- Name ---

func TestBotName(t *testing.T) {
	bot := &Bot{}
	if bot.Name() != "qq" {
		t.Errorf("Name() = %q, want %q", bot.Name(), "qq")
	}
}

// --- formatModelList ---

func TestFormatModelListNoQuery(t *testing.T) {
	models := channel.IndexModels([]channel.ModelOption{
		{Provider: "openai", Model: "gpt-4"},
		{Provider: "anthropic", Model: "claude-3"},
	})
	out := channel.FormatModelList(models, "")
	if !strings.Contains(out, "• openai/gpt-4") {
		t.Errorf("missing model entry in output: %s", out)
	}
	if !strings.Contains(out, "• anthropic/claude-3") {
		t.Errorf("missing model entry in output: %s", out)
	}
	if strings.Contains(out, "filter") {
		t.Error("should not show filter when query is empty")
	}
}

func TestFormatModelListWithQuery(t *testing.T) {
	models := channel.IndexModels([]channel.ModelOption{
		{Provider: "openai", Model: "gpt-4"},
	})
	out := channel.FormatModelList(models, "openai")
	if !strings.Contains(out, `filter: "openai"`) {
		t.Errorf("should show filter query: %s", out)
	}
}

// --- extractImageAttachments ---

func TestExtractImageAttachmentsEmpty(t *testing.T) {
	msg := &dto.Message{}
	images := extractImageAttachments(msg)
	if len(images) != 0 {
		t.Errorf("expected 0 images, got %d", len(images))
	}
}

func TestExtractImageAttachmentsFiltersImages(t *testing.T) {
	msg := &dto.Message{
		Attachments: []*dto.MessageAttachment{
			{URL: "https://example.com/a.png", ContentType: "image/png"},
			{URL: "https://example.com/b.txt", ContentType: "text/plain"},
			{URL: "", ContentType: "image/jpeg"},
			{URL: "https://example.com/c.jpg", ContentType: "image/jpeg"},
		},
	}
	images := extractImageAttachments(msg)
	if len(images) != 2 {
		t.Errorf("expected 2 images, got %d", len(images))
	}
}

// --- downloadImage ---

func TestDownloadImageSuccess(t *testing.T) {
	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("fakepng"))
	}))
	defer srv.Close()

	data, mime, err := downloadImage(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "fakepng" {
		t.Errorf("data = %q", data)
	}
	if mime != "image/png" {
		t.Errorf("mime = %q", mime)
	}
}

func TestDownloadImageAddsScheme(t *testing.T) {
	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	data, _, err := downloadImage(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "ok" {
		t.Errorf("data = %q", data)
	}
}

func TestDownloadImageBadStatus(t *testing.T) {
	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, _, err := downloadImage(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error for 404")
	}
}

func TestDownloadImageTooLarge(t *testing.T) {
	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(make([]byte, maxImageSize+10))
	}))
	defer srv.Close()

	_, _, err := downloadImage(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error for oversized image")
	}
}

func TestDownloadImageDetectsMIME(t *testing.T) {
	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	_, mime, err := downloadImage(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mime == "" {
		t.Error("expected auto-detected MIME type")
	}
}

// --- handleCommand ---

// mockHandler implements channel.Handler for tests.
type mockHandler struct {
	handleIncomingFn func(ctx context.Context, msg channel.IncomingMessage, cmd, args string) (string, bool, *channel.ChatStream, error)
	listModelsFn     func() []channel.ModelOption
	switchModelFn    func(provider, model string) error
	listAgentsFn     func(ctx context.Context, msg channel.IncomingMessage) ([]channel.AgentInfo, string, error)
	switchAgentFn    func(ctx context.Context, msg channel.IncomingMessage, slug string) error
}

func (m *mockHandler) HandleIncoming(ctx context.Context, msg channel.IncomingMessage, cmd, args string) (string, bool, *channel.ChatStream, error) {
	if m.handleIncomingFn != nil {
		return m.handleIncomingFn(ctx, msg, cmd, args)
	}
	// Default: handle shared commands.
	switch cmd {
	case "/start":
		return channel.WelcomeMessage, true, nil, nil
	case "/new":
		return "Session reset.", true, nil, nil
	case "/whoami":
		return fmt.Sprintf("Your user ID: %s", msg.SenderID), true, nil, nil
	}
	return "", false, nil, nil
}

func (m *mockHandler) ListAgents(ctx context.Context, msg channel.IncomingMessage) ([]channel.AgentInfo, string, error) {
	if m.listAgentsFn != nil {
		return m.listAgentsFn(ctx, msg)
	}
	return nil, "", nil
}

func (m *mockHandler) SwitchAgent(ctx context.Context, msg channel.IncomingMessage, slug string) error {
	if m.switchAgentFn != nil {
		return m.switchAgentFn(ctx, msg, slug)
	}
	return nil
}

func (m *mockHandler) ListModels() []channel.ModelOption {
	if m.listModelsFn != nil {
		return m.listModelsFn()
	}
	return nil
}

func (m *mockHandler) SwitchModel(provider, model string) error {
	if m.switchModelFn != nil {
		return m.switchModelFn(provider, model)
	}
	return nil
}

func TestHandleLocalCommandHelp(t *testing.T) {
	bot := &Bot{handler: &mockHandler{}}
	var reply string
	incoming := incomingMsg("user123", "", nil)
	handled := bot.handleLocalCommand(incoming, "/help", func(s string) { reply = s })
	if !handled {
		t.Fatal("expected /help to be handled")
	}
	if !strings.Contains(reply, "Stella") {
		t.Errorf("unexpected reply: %s", reply)
	}
}

func TestHandleLocalCommandWhoami(t *testing.T) {
	bot := &Bot{handler: &mockHandler{}}
	var reply string
	incoming := incomingMsg("OPEN_ID_ABC", "", nil)
	handled := bot.handleLocalCommand(incoming, "/whoami", func(s string) { reply = s })
	if !handled {
		t.Fatal("expected /whoami to be handled")
	}
	if !strings.Contains(reply, "OPEN_ID_ABC") {
		t.Errorf("expected reply to contain sender ID, got: %s", reply)
	}
}

func TestHandleLocalCommandUnknown(t *testing.T) {
	bot := &Bot{handler: &mockHandler{}}
	incoming := incomingMsg("user123", "", nil)
	handled := bot.handleLocalCommand(incoming, "hello world", func(s string) {})
	if handled {
		t.Error("regular text should not be handled as command")
	}
}

func TestHandleLocalCommandAbortDelegatesToCoordinator(t *testing.T) {
	bot := &Bot{handler: &mockHandler{}}
	incoming := incomingMsg("user123", "", nil)
	called := false
	handled := bot.handleLocalCommand(incoming, "/abort", func(s string) { called = true })
	if handled {
		t.Fatal("expected /abort to be delegated to coordinator")
	}
	if called {
		t.Fatal("expected no local reply for /abort")
	}
}

func TestHandleLocalCommandEmpty(t *testing.T) {
	bot := &Bot{handler: &mockHandler{}}
	incoming := incomingMsg("user123", "", nil)
	handled := bot.handleLocalCommand(incoming, "", func(s string) {})
	if handled {
		t.Error("empty text should not be handled")
	}
}

func TestHandleLocalCommandModel(t *testing.T) {
	models := []channel.ModelOption{{Provider: "openai", Model: "gpt-4"}}
	bot := &Bot{
		handler:    &mockHandler{listModelsFn: func() []channel.ModelOption { return models }},
		chatModels: make(map[string]channel.ModelOption),
	}
	var reply string
	incoming := incomingMsg("user123", "", nil)
	handled := bot.handleLocalCommand(incoming, "/model", func(s string) { reply = s })
	if !handled {
		t.Fatal("expected /model to be handled")
	}
	if !strings.Contains(reply, "gpt-4") {
		t.Errorf("expected model list, got: %s", reply)
	}
}

// --- handleModelCommand ---

func TestHandleModelCommandListModels(t *testing.T) {
	models := []channel.ModelOption{
		{Provider: "openai", Model: "gpt-4"},
	}
	bot := &Bot{
		handler:    &mockHandler{listModelsFn: func() []channel.ModelOption { return models }},
		chatModels: make(map[string]channel.ModelOption),
	}

	var reply string
	bot.handleModelCommand("", func(s string) { reply = s })
	if !strings.Contains(reply, "openai/gpt-4") {
		t.Errorf("expected model list, got: %s", reply)
	}
}

func TestHandleModelCommandFilter(t *testing.T) {
	models := []channel.ModelOption{
		{Provider: "openai", Model: "gpt-4"},
		{Provider: "anthropic", Model: "claude-3"},
	}
	bot := &Bot{
		handler:    &mockHandler{listModelsFn: func() []channel.ModelOption { return models }},
		chatModels: make(map[string]channel.ModelOption),
	}

	var reply string
	bot.handleModelCommand("claude", func(s string) { reply = s })
	if !strings.Contains(reply, "claude-3") {
		t.Errorf("expected filtered results with claude, got: %s", reply)
	}
	if strings.Contains(reply, "gpt-4") {
		t.Errorf("should not contain non-matching model: %s", reply)
	}
}

func TestHandleModelCommandFilterNoMatch(t *testing.T) {
	models := []channel.ModelOption{
		{Provider: "openai", Model: "gpt-4"},
	}
	bot := &Bot{
		handler:    &mockHandler{listModelsFn: func() []channel.ModelOption { return models }},
		chatModels: make(map[string]channel.ModelOption),
	}

	var reply string
	bot.handleModelCommand("gemini", func(s string) { reply = s })
	if !strings.Contains(reply, "No models matching") {
		t.Errorf("expected no match message, got: %s", reply)
	}
}

func TestHandleModelCommandSwitchByName(t *testing.T) {
	models := []channel.ModelOption{
		{Provider: "openai", Model: "gpt-4"},
	}
	var switched string
	bot := &Bot{
		handler: &mockHandler{
			listModelsFn:  func() []channel.ModelOption { return models },
			switchModelFn: func(p, m string) error { switched = p + "/" + m; return nil },
		},
		chatModels: make(map[string]channel.ModelOption),
	}

	var reply string
	bot.handleModelCommand("openai/gpt-4", func(s string) { reply = s })
	if !strings.Contains(reply, "Switched to openai/gpt-4") {
		t.Errorf("expected switch confirmation, got: %s", reply)
	}
	if switched != "openai/gpt-4" {
		t.Errorf("expected switch to openai/gpt-4, got: %s", switched)
	}
}

func TestHandleModelCommandSwitchByNameUnknown(t *testing.T) {
	models := []channel.ModelOption{
		{Provider: "openai", Model: "gpt-4"},
	}
	bot := &Bot{
		handler:    &mockHandler{listModelsFn: func() []channel.ModelOption { return models }},
		chatModels: make(map[string]channel.ModelOption),
	}

	var reply string
	bot.handleModelCommand("fake/model", func(s string) { reply = s })
	if !strings.Contains(reply, "Unknown model") {
		t.Errorf("expected unknown model error, got: %s", reply)
	}
}

func TestHandleModelCommandSwitchError(t *testing.T) {
	models := []channel.ModelOption{
		{Provider: "openai", Model: "gpt-4"},
	}
	bot := &Bot{
		handler: &mockHandler{
			listModelsFn:  func() []channel.ModelOption { return models },
			switchModelFn: func(string, string) error { return fmt.Errorf("switch failed") },
		},
		chatModels: make(map[string]channel.ModelOption),
	}

	var reply string
	bot.handleModelCommand("openai/gpt-4", func(s string) { reply = s })
	if !strings.Contains(reply, "Error switching model") {
		t.Errorf("expected switch error, got: %s", reply)
	}
}

func TestHandleModelCommandNumericTreatedAsFilter(t *testing.T) {
	models := []channel.ModelOption{
		{Provider: "openai", Model: "gpt-4"},
	}
	bot := &Bot{
		handler:    &mockHandler{listModelsFn: func() []channel.ModelOption { return models }},
		chatModels: make(map[string]channel.ModelOption),
	}

	var reply string
	bot.handleModelCommand("5", func(s string) { reply = s })
	// "5" is now treated as a filter query, not an index
	if !strings.Contains(reply, "No models matching") {
		t.Errorf("expected no match for numeric filter, got: %s", reply)
	}
}

// --- Notify ---

func TestNotifyEmptyChatID(t *testing.T) {
	bot := &Bot{}
	err := bot.Notify(context.Background(), channel.Notification{})
	if err == nil {
		t.Fatal("expected error for empty chat ID")
	}
}

// --- Stop ---

func TestStopWithCancel(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	bot := &Bot{cancel: cancel}
	bot.Stop() // should not panic
}

func TestStopWithoutCancel(t *testing.T) {
	bot := &Bot{}
	bot.Stop() // should not panic when cancel is nil
}

// --- buildMessageContent ---

func TestBuildMessageContentTextOnly(t *testing.T) {
	bot := &Bot{}
	msg := &dto.Message{Content: "hello world"}
	content := bot.buildMessageContent(msg, "", false)
	if len(content) != 1 {
		t.Fatalf("expected 1 block, got %d", len(content))
	}
	tc, ok := content[0].(ai.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", content[0])
	}
	if tc.Text != "hello world" {
		t.Errorf("text = %q, want %q", tc.Text, "hello world")
	}
}

func TestBuildMessageContentEmpty(t *testing.T) {
	bot := &Bot{}
	msg := &dto.Message{Content: "  "}
	content := bot.buildMessageContent(msg, "", false)
	if content != nil {
		t.Errorf("expected nil for blank message, got %v", content)
	}
}

func TestBuildMessageContentEmptyNoAttachments(t *testing.T) {
	bot := &Bot{}
	msg := &dto.Message{}
	content := bot.buildMessageContent(msg, "", false)
	if content != nil {
		t.Errorf("expected nil for empty message, got %v", content)
	}
}

// --- sendImage (no-op) ---

func TestSendImageNoOp(t *testing.T) {
	bot := &Bot{}
	bot.sendImage("target", "msg", channel.ImageEvent{}, scopeC2C)
}

// --- incomingMsg ---

func TestIncomingMsgC2C(t *testing.T) {
	msg := incomingMsg("user1", "", channel.TextContent("hello"))
	if msg.Platform != channel.PlatformQQ {
		t.Errorf("Platform = %q, want %q", msg.Platform, channel.PlatformQQ)
	}
	if msg.SenderID != "user1" {
		t.Errorf("SenderID = %q, want %q", msg.SenderID, "user1")
	}
	if msg.ChatID != "qq:c2c:user1" {
		t.Errorf("ChatID = %q, want %q", msg.ChatID, "qq:c2c:user1")
	}
	if msg.IsGroup {
		t.Error("IsGroup should be false for C2C")
	}
}

func TestIncomingMsgGroup(t *testing.T) {
	msg := incomingMsg("user1", "group1", channel.TextContent("hello"))
	if msg.ChatID != "qq:group:group1" {
		t.Errorf("ChatID = %q, want %q", msg.ChatID, "qq:group:group1")
	}
	if !msg.IsGroup {
		t.Error("IsGroup should be true for group")
	}
}

// --- pure-image persistence (regression: image-only messages must persist) ---

// assetMockHandler extends mockHandler with the UserRootResolver and AssetSaver
// capabilities so buildMessageContent can resolve a storage dir and persist.
type assetMockHandler struct {
	mockHandler
	userRoot  string
	saveErr   error
	saveCalls []savedAsset
}

type savedAsset struct {
	assetsDir string
	fileName  string
	data      []byte
}

func (m *assetMockHandler) ResolveUserRoot(_ context.Context, _ channel.IncomingMessage) (string, error) {
	return m.userRoot, nil
}

func (m *assetMockHandler) SaveAsset(_ context.Context, assetsDir, fileName string, data []byte) (string, error) {
	if m.saveErr != nil {
		return "", m.saveErr
	}
	m.saveCalls = append(m.saveCalls, savedAsset{assetsDir: assetsDir, fileName: fileName, data: append([]byte(nil), data...)})
	return filepath.Join(assetsDir, fileName), nil
}

func TestResolveAssetsDirImageOnlyMessage(t *testing.T) {
	h := &assetMockHandler{userRoot: "/home/stella"}
	bot := &Bot{handler: h, ctx: context.Background()}
	msg := &dto.Message{
		Attachments: []*dto.MessageAttachment{
			{URL: "https://example.com/a.png", ContentType: "image/png"},
		},
	}
	// Regression: an image-only message (no file attachments) must still resolve a
	// storage dir so the image is persisted rather than inline-only.
	if got := bot.resolveAssetsDir(bot.incomingMsg("user1", "", nil), msg); got == "" {
		t.Fatal("expected a resolved assets dir for an image-only message, got \"\"")
	}
}

func TestBuildMessageContentPersistsPureImage(t *testing.T) {
	pngBytes := []byte("\x89PNG\r\n\x1a\nfake png body")
	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer srv.Close()

	h := &assetMockHandler{userRoot: "/home/stella"}
	bot := &Bot{handler: h, ctx: context.Background()}
	msg := &dto.Message{
		Attachments: []*dto.MessageAttachment{
			{URL: srv.URL, ContentType: "image/png", FileName: "pic.png"},
		},
	}

	assetsDir := bot.resolveAssetsDir(bot.incomingMsg("user1", "", nil), msg)
	if assetsDir == "" {
		t.Fatal("expected a resolved assets dir for an image-only message")
	}

	content := bot.buildMessageContent(msg, assetsDir, false)
	if len(h.saveCalls) != 1 {
		t.Fatalf("expected the image to be persisted once, got %d save calls", len(h.saveCalls))
	}
	if string(h.saveCalls[0].data) != string(pngBytes) {
		t.Fatalf("persisted bytes do not match the downloaded image")
	}
	got := ai.FlattenText(content)
	if !strings.Contains(got, "saved to") {
		t.Fatalf("content = %q, want a saved-path note", got)
	}
}

func TestBuildMessageContentImageSaveFailureInlines(t *testing.T) {
	pngBytes := []byte("\x89PNG\r\n\x1a\nfake png body")
	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer srv.Close()

	h := &assetMockHandler{userRoot: "/home/stella", saveErr: fmt.Errorf("storage down")}
	bot := &Bot{handler: h, ctx: context.Background()}
	msg := &dto.Message{
		Attachments: []*dto.MessageAttachment{
			{URL: srv.URL, ContentType: "image/png", FileName: "pic.png"},
		},
	}

	assetsDir := bot.resolveAssetsDir(bot.incomingMsg("user1", "", nil), msg)
	content := bot.buildMessageContent(msg, assetsDir, false)

	// Save failed, but the turn must not be dropped: a small image degrades to an
	// inline image block via the shared fallback.
	hasInlineImage := false
	for _, block := range content {
		if _, ok := block.(ai.ImageContent); ok {
			hasInlineImage = true
		}
	}
	if !hasInlineImage {
		t.Fatalf("expected an inline image fallback on save failure, got %#v", content)
	}
}
