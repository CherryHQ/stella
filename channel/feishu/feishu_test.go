package feishu

import (
	"context"
	"fmt"
	"strings"
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/vaayne/anna/agent/runner"
	"github.com/vaayne/anna/channel"
)

// --- splitMessage ---

func TestSplitMessageShort(t *testing.T) {
	chunks := splitMessage("hello")
	if len(chunks) != 1 || chunks[0] != "hello" {
		t.Errorf("chunks = %v, want [hello]", chunks)
	}
}

func TestSplitMessageExactLimit(t *testing.T) {
	msg := strings.Repeat("a", feishuMaxMessageLen)
	chunks := splitMessage(msg)
	if len(chunks) != 1 {
		t.Errorf("len(chunks) = %d, want 1", len(chunks))
	}
}

func TestSplitMessageLong(t *testing.T) {
	msg := strings.Repeat("a", feishuMaxMessageLen+100)
	chunks := splitMessage(msg)
	if len(chunks) != 2 {
		t.Fatalf("len(chunks) = %d, want 2", len(chunks))
	}
	if len(chunks[0]) != feishuMaxMessageLen {
		t.Errorf("chunk[0] len = %d, want %d", len(chunks[0]), feishuMaxMessageLen)
	}
	if len(chunks[1]) != 100 {
		t.Errorf("chunk[1] len = %d, want 100", len(chunks[1]))
	}
}

func TestSplitMessageAtNewline(t *testing.T) {
	part1 := strings.Repeat("a", 3000)
	part2 := strings.Repeat("b", 2000)
	msg := part1 + "\n" + part2

	chunks := splitMessage(msg)
	if len(chunks) != 2 {
		t.Fatalf("len(chunks) = %d, want 2", len(chunks))
	}
	if chunks[0] != part1+"\n" {
		t.Errorf("chunk[0] should end at newline")
	}
}

func TestSplitMessageMultibyteUTF8(t *testing.T) {
	char := "中"
	msg := strings.Repeat(char, feishuMaxMessageLen) // 3*feishuMaxMessageLen bytes
	chunks := splitMessage(msg)
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
	chunks := splitMessage("")
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
	long := strings.Repeat("x", feishuMaxMessageLen+500)
	d := buildStreamDisplay(long, "")
	if len(d) > feishuMaxMessageLen {
		t.Errorf("display len = %d, should be <= %d", len(d), feishuMaxMessageLen)
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

func TestToolLineRunningNoInput(t *testing.T) {
	line := toolLine(&runner.ToolUseEvent{Tool: "search", Status: "running"})
	if !strings.Contains(line, "search") {
		t.Errorf("unexpected line: %q", line)
	}
	if strings.Contains(line, ":") {
		t.Errorf("no input should not have colon separator: %q", line)
	}
}

func TestToolLineUnknownTool(t *testing.T) {
	line := toolLine(&runner.ToolUseEvent{Tool: "custom_tool", Status: "running", Input: "x"})
	if !strings.Contains(line, "🔧") {
		t.Errorf("unknown tool should use default emoji: %q", line)
	}
}

// --- filterModelsIndexed ---

func TestFilterModelsIndexed(t *testing.T) {
	models := []channel.ModelOption{
		{Provider: "openai", Model: "gpt-4"},
		{Provider: "anthropic", Model: "claude-3"},
		{Provider: "openai", Model: "gpt-3.5"},
	}

	result := filterModelsIndexed(models, "openai")
	if len(result) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(result))
	}
	if result[0].globalIdx != 1 || result[1].globalIdx != 3 {
		t.Errorf("global indices should be 1 and 3, got %d and %d", result[0].globalIdx, result[1].globalIdx)
	}
}

func TestFilterModelsIndexedNoMatch(t *testing.T) {
	models := []channel.ModelOption{
		{Provider: "openai", Model: "gpt-4"},
	}
	result := filterModelsIndexed(models, "gemini")
	if len(result) != 0 {
		t.Errorf("expected 0 matches, got %d", len(result))
	}
}

func TestFilterModelsIndexedCaseInsensitive(t *testing.T) {
	models := []channel.ModelOption{
		{Provider: "Anthropic", Model: "Claude-3"},
	}
	result := filterModelsIndexed(models, "CLAUDE")
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

// --- channelForChat ---

func TestChannelForChat(t *testing.T) {
	if got := channelForChat("oc_123"); got != "feishu:oc_123" {
		t.Errorf("channelForChat = %q", got)
	}
}

// --- shouldRespondInGroup ---

func TestShouldRespondInGroupMention(t *testing.T) {
	bot := &Bot{cfg: Config{GroupMode: "mention"}}
	key := "@_user_1"
	mentions := []*larkim.MentionEvent{{Key: &key}}
	if !bot.shouldRespondInGroup(mentions) {
		t.Error("mention mode with mention should respond")
	}
}

func TestShouldRespondInGroupMentionNoMentions(t *testing.T) {
	bot := &Bot{cfg: Config{GroupMode: "mention"}}
	if bot.shouldRespondInGroup(nil) {
		t.Error("mention mode without mentions should not respond")
	}
}

func TestShouldRespondInGroupAlways(t *testing.T) {
	bot := &Bot{cfg: Config{GroupMode: "always"}}
	if !bot.shouldRespondInGroup(nil) {
		t.Error("always mode should respond")
	}
}

func TestShouldRespondInGroupDisabled(t *testing.T) {
	bot := &Bot{cfg: Config{GroupMode: "disabled"}}
	if bot.shouldRespondInGroup(nil) {
		t.Error("disabled mode should not respond")
	}
}

// --- Name ---

func TestBotName(t *testing.T) {
	bot := &Bot{}
	if bot.Name() != "feishu" {
		t.Errorf("Name() = %q, want %q", bot.Name(), "feishu")
	}
}

// --- textContent ---

func TestTextContent(t *testing.T) {
	got := textContent("hello")
	if got != `{"text":"hello"}` {
		t.Errorf("textContent = %q", got)
	}
}

func TestTextContentEscapes(t *testing.T) {
	got := textContent(`say "hi"`)
	if !strings.Contains(got, `\"hi\"`) {
		t.Errorf("textContent should escape quotes: %q", got)
	}
}

// --- parseTextContent ---

func TestParseTextContentValid(t *testing.T) {
	text := parseTextContent(`{"text":"hello world"}`)
	if text != "hello world" {
		t.Errorf("parseTextContent = %q, want %q", text, "hello world")
	}
}

func TestParseTextContentEmpty(t *testing.T) {
	text := parseTextContent("")
	if text != "" {
		t.Errorf("parseTextContent empty = %q", text)
	}
}

func TestParseTextContentInvalidJSON(t *testing.T) {
	text := parseTextContent("not json")
	if text != "not json" {
		t.Errorf("parseTextContent invalid = %q", text)
	}
}

// --- stripMentions ---

func TestStripMentions(t *testing.T) {
	key := "@_user_1"
	mentions := []*larkim.MentionEvent{{Key: &key}}
	result := stripMentions("hello @_user_1 world", mentions)
	if result != "hello  world" {
		t.Errorf("stripMentions = %q", result)
	}
}

func TestStripMentionsNoMentions(t *testing.T) {
	result := stripMentions("hello world", nil)
	if result != "hello world" {
		t.Errorf("stripMentions = %q", result)
	}
}

// --- derefStr ---

func TestDerefStrNil(t *testing.T) {
	if derefStr(nil) != "" {
		t.Error("derefStr nil should return empty")
	}
}

func TestDerefStrValue(t *testing.T) {
	s := "hello"
	if derefStr(&s) != "hello" {
		t.Error("derefStr should return value")
	}
}

// --- formatModelList ---

func TestFormatModelListNoQuery(t *testing.T) {
	models := []channel.ModelOption{
		{Provider: "openai", Model: "gpt-4"},
		{Provider: "anthropic", Model: "claude-3"},
	}
	out := formatModelList(models, "")
	if !strings.Contains(out, "1. openai/gpt-4") {
		t.Errorf("missing model entry in output: %s", out)
	}
	if !strings.Contains(out, "2. anthropic/claude-3") {
		t.Errorf("missing model entry in output: %s", out)
	}
}

func TestFormatModelListWithQuery(t *testing.T) {
	models := []channel.ModelOption{{Provider: "openai", Model: "gpt-4"}}
	out := formatModelList(models, "openai")
	if !strings.Contains(out, `filter: "openai"`) {
		t.Errorf("should show filter query: %s", out)
	}
}

// --- handleCommand ---

func TestHandleCommandHelp(t *testing.T) {
	bot := &Bot{}
	var reply string
	handled := bot.handleCommand("/help", "ch", func(s string) { reply = s })
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
	handled := bot.handleCommand("/start", "ch", func(s string) { reply = s })
	if !handled {
		t.Fatal("expected /start to be handled")
	}
	if reply != welcomeMessage {
		t.Errorf("unexpected reply: %s", reply)
	}
}

func TestHandleCommandUnknown(t *testing.T) {
	bot := &Bot{}
	handled := bot.handleCommand("hello world", "ch", func(s string) {})
	if handled {
		t.Error("regular text should not be handled as command")
	}
}

func TestHandleCommandEmpty(t *testing.T) {
	bot := &Bot{}
	handled := bot.handleCommand("", "ch", func(s string) {})
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
		listFn:     func() []channel.ModelOption { return models },
		chatModels: make(map[string]ModelOption),
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
		listFn:     func() []channel.ModelOption { return models },
		chatModels: make(map[string]ModelOption),
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
		listFn:     func() []channel.ModelOption { return models },
		chatModels: make(map[string]ModelOption),
	}

	var reply string
	bot.handleModelCommand("gemini", "ch", func(s string) { reply = s })
	if !strings.Contains(reply, "No models matching") {
		t.Errorf("expected no match message, got: %s", reply)
	}
}

func TestHandleModelCommandInvalidIndex(t *testing.T) {
	models := []channel.ModelOption{
		{Provider: "openai", Model: "gpt-4"},
	}
	bot := &Bot{
		listFn:     func() []channel.ModelOption { return models },
		chatModels: make(map[string]ModelOption),
	}

	var reply string
	bot.handleModelCommand("5", "ch", func(s string) { reply = s })
	if !strings.Contains(reply, "Invalid selection") {
		t.Errorf("expected invalid selection, got: %s", reply)
	}
}

func TestHandleModelCommandSwitchError(t *testing.T) {
	models := []channel.ModelOption{
		{Provider: "openai", Model: "gpt-4"},
	}
	bot := &Bot{
		listFn:     func() []channel.ModelOption { return models },
		switchFn:   func(string, string) error { return fmt.Errorf("switch failed") },
		chatModels: make(map[string]ModelOption),
	}

	var reply string
	bot.handleModelCommand("1", "ch", func(s string) { reply = s })
	if !strings.Contains(reply, "Error switching model") {
		t.Errorf("expected switch error, got: %s", reply)
	}
}

// --- Notify ---

func TestNotifyEmptyChatID(t *testing.T) {
	bot := &Bot{cfg: Config{}}
	err := bot.Notify(context.Background(), channel.Notification{})
	if err == nil {
		t.Fatal("expected error for empty chat ID")
	}
}

// --- sendImage (no-op) ---

func TestSendImageNoOp(t *testing.T) {
	bot := &Bot{}
	// Should not panic.
	bot.sendImage("target", "msg", runner.ImageEvent{})
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
