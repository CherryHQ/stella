package qq

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tencent-connect/botgo/dto"
	"github.com/vaayne/anna/agent/runner"
	"github.com/vaayne/anna/channel"
)

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

// --- toolLine ---

func TestToolLineRunning(t *testing.T) {
	line := toolLine(&runner.ToolUseEvent{Tool: "bash", Status: "running", Input: "ls -la"})
	if !strings.Contains(line, "bash") || !strings.Contains(line, "ls -la") {
		t.Errorf("unexpected line: %q", line)
	}
}

func TestToolLineRunningTruncatesMultibyte(t *testing.T) {
	input := strings.Repeat("中", 61)
	line := toolLine(&runner.ToolUseEvent{Tool: "bash", Status: "running", Input: input})
	if !strings.HasSuffix(line, "...") {
		t.Errorf("expected truncation ellipsis, got %q", line)
	}
}

func TestToolLineRunningNoInput(t *testing.T) {
	line := toolLine(&runner.ToolUseEvent{Tool: "search", Status: "running"})
	if !strings.Contains(line, "search") {
		t.Errorf("unexpected line: %q", line)
	}
	if strings.Contains(line, ":") {
		t.Errorf("no input should not have colon separator: %q", line)
	}
}

func TestToolLineError(t *testing.T) {
	line := toolLine(&runner.ToolUseEvent{Tool: "read", Status: "error"})
	if !strings.Contains(line, "failed") {
		t.Errorf("unexpected line: %q", line)
	}
}

func TestToolLineDefault(t *testing.T) {
	line := toolLine(&runner.ToolUseEvent{Tool: "bash", Status: "done"})
	if line != "" {
		t.Errorf("expected empty, got %q", line)
	}
}

func TestToolLineUnknownTool(t *testing.T) {
	line := toolLine(&runner.ToolUseEvent{Tool: "custom_tool", Status: "running", Input: "x"})
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
	bot, err := New(cfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bot == nil {
		t.Fatal("expected bot, got nil")
	}
	if bot.cfg.GroupMode != "mention" {
		t.Errorf("default group_mode = %q, want %q", bot.cfg.GroupMode, "mention")
	}
}

func TestNewMissingAppID(t *testing.T) {
	_, err := New(Config{AppSecret: "secret"}, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for missing app_id")
	}
}

func TestNewMissingAppSecret(t *testing.T) {
	_, err := New(Config{AppID: "123"}, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for missing app_secret")
	}
}

func TestNewCustomGroupMode(t *testing.T) {
	cfg := Config{AppID: "1", AppSecret: "s", GroupMode: "always"}
	bot, _ := New(cfg, nil, nil, nil)
	if bot.cfg.GroupMode != "always" {
		t.Errorf("group_mode = %q, want %q", bot.cfg.GroupMode, "always")
	}
}

func TestNewAllowedIDs(t *testing.T) {
	cfg := Config{AppID: "1", AppSecret: "s", AllowedIDs: []string{"u1", "u2"}}
	bot, _ := New(cfg, nil, nil, nil)
	if len(bot.allowed) != 2 {
		t.Errorf("allowed len = %d, want 2", len(bot.allowed))
	}
}

// --- isAllowed ---

func TestIsAllowedEmptyList(t *testing.T) {
	bot := &Bot{allowed: map[string]struct{}{}}
	if !bot.isAllowed("anyone") {
		t.Error("empty allowed list should allow everyone")
	}
}

func TestIsAllowedMatch(t *testing.T) {
	bot := &Bot{allowed: map[string]struct{}{"u1": {}}}
	if !bot.isAllowed("u1") {
		t.Error("u1 should be allowed")
	}
}

func TestIsAllowedNoMatch(t *testing.T) {
	bot := &Bot{allowed: map[string]struct{}{"u1": {}}}
	if bot.isAllowed("u2") {
		t.Error("u2 should not be allowed")
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

// --- shouldRespondInGroup ---

func TestShouldRespondInGroupMention(t *testing.T) {
	bot := &Bot{cfg: Config{GroupMode: "mention"}}
	if !bot.shouldRespondInGroup() {
		t.Error("mention mode should respond")
	}
}

func TestShouldRespondInGroupAlways(t *testing.T) {
	bot := &Bot{cfg: Config{GroupMode: "always"}}
	if !bot.shouldRespondInGroup() {
		t.Error("always mode should respond")
	}
}

func TestShouldRespondInGroupDisabled(t *testing.T) {
	bot := &Bot{cfg: Config{GroupMode: "disabled"}}
	if bot.shouldRespondInGroup() {
		t.Error("disabled mode should not respond")
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
	out := formatModelList(models, "")
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
	out := formatModelList(models, "openai")
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, _, err := downloadImage(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error for 404")
	}
}

func TestDownloadImageTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

func TestHandleCommandHelp(t *testing.T) {
	bot := &Bot{}
	var reply string
	handled := bot.handleCommand("/help", "ch", "user123", func(s string) { reply = s })
	if !handled {
		t.Fatal("expected /help to be handled")
	}
	if !strings.Contains(reply, "Anna") {
		t.Errorf("unexpected reply: %s", reply)
	}
}

func TestHandleCommandStart(t *testing.T) {
	bot := &Bot{}
	var reply string
	handled := bot.handleCommand("/start", "ch", "user123", func(s string) { reply = s })
	if !handled {
		t.Fatal("expected /start to be handled")
	}
	if reply != welcomeMessage {
		t.Errorf("unexpected reply: %s", reply)
	}
}

func TestHandleCommandUnknown(t *testing.T) {
	bot := &Bot{}
	handled := bot.handleCommand("hello world", "ch", "user123", func(s string) {})
	if handled {
		t.Error("regular text should not be handled as command")
	}
}

func TestHandleCommandEmpty(t *testing.T) {
	bot := &Bot{}
	handled := bot.handleCommand("", "ch", "user123", func(s string) {})
	if handled {
		t.Error("empty text should not be handled")
	}
}

// --- handleModelCommand ---

func TestHandleModelCommandListModels(t *testing.T) {
	models := []channel.ModelOption{
		{Provider: "openai", Model: "gpt-4"},
	}
	bot := &Bot{
		cmd:        channel.NewCommander(newTestPool(), func() []channel.ModelOption { return models }, nil),
		listFn:     func() []channel.ModelOption { return models },
		chatModels: make(map[string]channel.ModelOption),
	}

	var reply string
	bot.handleModelCommand("", "ch", func(s string) { reply = s })
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
		cmd:        channel.NewCommander(newTestPool(), func() []channel.ModelOption { return models }, nil),
		listFn:     func() []channel.ModelOption { return models },
		chatModels: make(map[string]channel.ModelOption),
	}

	var reply string
	bot.handleModelCommand("claude", "ch", func(s string) { reply = s })
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
		cmd:        channel.NewCommander(newTestPool(), func() []channel.ModelOption { return models }, nil),
		listFn:     func() []channel.ModelOption { return models },
		chatModels: make(map[string]channel.ModelOption),
	}

	var reply string
	bot.handleModelCommand("gemini", "ch", func(s string) { reply = s })
	if !strings.Contains(reply, "No models matching") {
		t.Errorf("expected no match message, got: %s", reply)
	}
}

func TestHandleModelCommandSwitchByName(t *testing.T) {
	models := []channel.ModelOption{
		{Provider: "openai", Model: "gpt-4"},
	}
	var switched string
	switchFn := func(p, m string) error { switched = p + "/" + m; return nil }
	bot := &Bot{
		cmd:        channel.NewCommander(newTestPool(), func() []channel.ModelOption { return models }, switchFn),
		listFn:     func() []channel.ModelOption { return models },
		switchFn:   switchFn,
		chatModels: make(map[string]channel.ModelOption),
	}

	var reply string
	bot.handleModelCommand("openai/gpt-4", "ch", func(s string) { reply = s })
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
		cmd:        channel.NewCommander(newTestPool(), func() []channel.ModelOption { return models }, nil),
		listFn:     func() []channel.ModelOption { return models },
		chatModels: make(map[string]channel.ModelOption),
	}

	var reply string
	bot.handleModelCommand("fake/model", "ch", func(s string) { reply = s })
	if !strings.Contains(reply, "unknown model") {
		t.Errorf("expected unknown model error, got: %s", reply)
	}
}

func TestHandleModelCommandSwitchError(t *testing.T) {
	models := []channel.ModelOption{
		{Provider: "openai", Model: "gpt-4"},
	}
	switchFn := func(string, string) error { return fmt.Errorf("switch failed") }
	bot := &Bot{
		cmd:        channel.NewCommander(newTestPool(), func() []channel.ModelOption { return models }, switchFn),
		listFn:     func() []channel.ModelOption { return models },
		switchFn:   switchFn,
		chatModels: make(map[string]channel.ModelOption),
	}

	var reply string
	bot.handleModelCommand("openai/gpt-4", "ch", func(s string) { reply = s })
	if !strings.Contains(reply, "switch model") {
		t.Errorf("expected switch error, got: %s", reply)
	}
}

func TestHandleModelCommandNumericTreatedAsFilter(t *testing.T) {
	models := []channel.ModelOption{
		{Provider: "openai", Model: "gpt-4"},
	}
	bot := &Bot{
		cmd:        channel.NewCommander(newTestPool(), func() []channel.ModelOption { return models }, nil),
		listFn:     func() []channel.ModelOption { return models },
		chatModels: make(map[string]channel.ModelOption),
	}

	var reply string
	bot.handleModelCommand("5", "ch", func(s string) { reply = s })
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
	content := bot.buildMessageContent(msg)
	text, ok := content.(string)
	if !ok {
		t.Fatalf("expected string, got %T", content)
	}
	if text != "hello world" {
		t.Errorf("text = %q, want %q", text, "hello world")
	}
}

func TestBuildMessageContentEmpty(t *testing.T) {
	bot := &Bot{}
	msg := &dto.Message{Content: "  "}
	content := bot.buildMessageContent(msg)
	if content != nil {
		t.Errorf("expected nil for blank message, got %v", content)
	}
}

func TestBuildMessageContentEmptyNoAttachments(t *testing.T) {
	bot := &Bot{}
	msg := &dto.Message{}
	content := bot.buildMessageContent(msg)
	if content != nil {
		t.Errorf("expected nil for empty message, got %v", content)
	}
}

// --- sendImage (no-op) ---

func TestSendImageNoOp(t *testing.T) {
	bot := &Bot{}
	bot.sendImage("target", "msg", runner.ImageEvent{}, scopeC2C)
}

// --- test helpers ---

type testPool struct {
	sessions map[string]channel.SessionInfo
	nextID   int
}

func newTestPool() *testPool { return &testPool{sessions: make(map[string]channel.SessionInfo)} }

func (p *testPool) ResolveSession(ch string) (channel.SessionInfo, error) {
	if info, ok := p.sessions[ch]; ok {
		return info, nil
	}
	return p.RotateSession(ch)
}

func (p *testPool) RotateSession(ch string) (channel.SessionInfo, error) {
	p.nextID++
	info := channel.SessionInfo{ID: fmt.Sprintf("s-%d", p.nextID)}
	p.sessions[ch] = info
	return info, nil
}

func (p *testPool) CompactSession(_ context.Context, _ string) (string, error) {
	return "compacted", nil
}
