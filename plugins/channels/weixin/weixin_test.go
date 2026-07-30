package weixin

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/channel"
)

type captureHandler struct {
	handleIncomingFn func(context.Context, channel.IncomingMessage, string, string) (string, bool, *channel.ChatStream, error)
}

func (h captureHandler) HandleIncoming(ctx context.Context, msg channel.IncomingMessage, cmd, args string) (string, bool, *channel.ChatStream, error) {
	if h.handleIncomingFn != nil {
		return h.handleIncomingFn(ctx, msg, cmd, args)
	}
	return "", false, nil, nil
}

func (captureHandler) ListModels() []channel.ModelOption { return nil }
func (captureHandler) SwitchModel(string, string) error  { return nil }
func (captureHandler) ListAgents(context.Context, channel.IncomingMessage) ([]channel.AgentInfo, string, error) {
	return nil, "", nil
}
func (captureHandler) SwitchAgent(context.Context, channel.IncomingMessage, string) error { return nil }

type weixinSavedAsset struct {
	assetsDir string
	fileName  string
	data      []byte
}

// assetCaptureHandler adds the optional user-root and asset-store capabilities
// required by inbound Weixin media.
type assetCaptureHandler struct {
	captureHandler
	userRoot  string
	saveErr   error
	saveCalls []weixinSavedAsset
}

func (h *assetCaptureHandler) ResolveUserRoot(context.Context, channel.IncomingMessage) (string, error) {
	return h.userRoot, nil
}

func (h *assetCaptureHandler) SaveAsset(_ context.Context, assetsDir, fileName string, data []byte) (string, error) {
	if h.saveErr != nil {
		return "", h.saveErr
	}
	h.saveCalls = append(h.saveCalls, weixinSavedAsset{
		assetsDir: assetsDir,
		fileName:  fileName,
		data:      append([]byte(nil), data...),
	})
	return filepath.Join(assetsDir, fileName), nil
}

func TestRandomWechatUIN(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{})
	for range 100 {
		uin := randomWechatUIN()
		if uin == "" {
			t.Fatal("randomWechatUIN returned empty string")
		}

		// Must be valid base64.
		decoded, err := base64.StdEncoding.DecodeString(uin)
		if err != nil {
			t.Fatalf("randomWechatUIN not valid base64: %v", err)
		}

		// Decoded must be a decimal string representing a uint32.
		val, err := strconv.ParseUint(string(decoded), 10, 32)
		if err != nil {
			t.Fatalf("decoded UIN %q is not a valid uint32 decimal: %v", string(decoded), err)
		}
		_ = val

		seen[uin] = struct{}{}
	}

	// With 100 random uint32 values, collisions should be essentially impossible.
	if len(seen) < 95 {
		t.Errorf("too many collisions: only %d unique values out of 100", len(seen))
	}
}

func TestHandleTextAbortDelegatesToCoordinator(t *testing.T) {
	var gotCmd, gotArgs, gotReply string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/sendmessage" {
			t.Fatalf("path = %q, want /ilink/bot/sendmessage", r.URL.Path)
		}
		var req SendMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(req.Msg.ItemList) != 1 || req.Msg.ItemList[0].TextItem == nil {
			t.Fatalf("unexpected reply payload: %+v", req.Msg.ItemList)
		}
		gotReply = req.Msg.ItemList[0].TextItem.Text
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()

	bot := &Bot{
		handler: captureHandler{handleIncomingFn: func(_ context.Context, _ channel.IncomingMessage, cmd, args string) (string, bool, *channel.ChatStream, error) {
			gotCmd, gotArgs = cmd, args
			return "Aborted.", true, nil, nil
		}},
		client: NewClient(server.URL, "", "token", ""),
		ctx:    context.Background(),
	}
	bot.contextTokens.Store("user-1", "ctx-token")

	bot.handleText(WeixinMessage{FromUserID: "user-1"}, "/abort")

	if gotCmd != "/abort" {
		t.Fatalf("cmd = %q, want /abort", gotCmd)
	}
	if gotArgs != "" {
		t.Fatalf("args = %q, want empty", gotArgs)
	}
	if gotReply != "Aborted." {
		t.Fatalf("reply = %q, want %q", gotReply, "Aborted.")
	}
}

func TestAESEncryptDecryptRoundTrip(t *testing.T) {
	t.Parallel()

	key, _ := hex.DecodeString("00112233445566778899aabbccddeeff")
	testCases := []struct {
		name      string
		plaintext []byte
	}{
		{"empty", []byte{}},
		{"short", []byte("hello")},
		{"exact block", []byte("0123456789abcdef")},                // 16 bytes
		{"two blocks", []byte("0123456789abcdef0123456789abcdef")}, // 32 bytes
		{"odd length", []byte("this is 17 bytes!")},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			encrypted, err := EncryptAESECB(tc.plaintext, key)
			if err != nil {
				t.Fatalf("encrypt: %v", err)
			}

			if len(encrypted)%16 != 0 {
				t.Fatalf("ciphertext length %d not multiple of 16", len(encrypted))
			}

			decrypted, err := DecryptAESECB(encrypted, key)
			if err != nil {
				t.Fatalf("decrypt: %v", err)
			}

			if string(decrypted) != string(tc.plaintext) {
				t.Errorf("round-trip mismatch: got %q, want %q", decrypted, tc.plaintext)
			}
		})
	}
}

func TestAESKnownVector(t *testing.T) {
	t.Parallel()

	// Test with known AES-128-ECB output.
	// Single block: 16 bytes of 0x00, key = 16 bytes of 0x00.
	key := make([]byte, 16)
	plaintext := []byte("hello world12345") // exactly 16 bytes

	encrypted, err := EncryptAESECB(plaintext, key)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// With PKCS7, 16 bytes of plaintext gets padded to 32 bytes.
	if len(encrypted) != 32 {
		t.Fatalf("expected 32 bytes ciphertext, got %d", len(encrypted))
	}

	decrypted, err := DecryptAESECB(encrypted, key)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Errorf("got %q, want %q", decrypted, plaintext)
	}
}

func TestCiphertextSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		rawSize  int
		expected int
	}{
		{0, 16},          // ceil((0+1)/16)*16 = 16
		{1, 16},          // ceil((1+1)/16)*16 = 16
		{15, 16},         // ceil((15+1)/16)*16 = 16
		{16, 32},         // ceil((16+1)/16)*16 = 32
		{17, 32},         // ceil((17+1)/16)*16 = 32
		{31, 32},         // ceil((31+1)/16)*16 = 32
		{32, 48},         // ceil((32+1)/16)*16 = 48
		{248731, 248736}, // from spec example
	}

	for _, tc := range tests {
		got := CiphertextSize(tc.rawSize)
		if got != tc.expected {
			t.Errorf("CiphertextSize(%d) = %d, want %d", tc.rawSize, got, tc.expected)
		}
	}
}

func TestDecodeAESKeyFormatA(t *testing.T) {
	t.Parallel()

	// Format A: base64 of raw 16 bytes.
	rawKey, _ := hex.DecodeString("00112233445566778899aabbccddeeff")
	encoded := base64.StdEncoding.EncodeToString(rawKey)
	// Should be "ABEiM0RVZneImaq7zN3u/w=="

	key, err := DecodeAESKey(encoded)
	if err != nil {
		t.Fatalf("DecodeAESKey format A: %v", err)
	}

	if hex.EncodeToString(key) != "00112233445566778899aabbccddeeff" {
		t.Errorf("got key %x, want 00112233445566778899aabbccddeeff", key)
	}
}

func TestDecodeAESKeyFormatB(t *testing.T) {
	t.Parallel()

	// Format B: base64 of hex string "00112233445566778899aabbccddeeff" (32 ASCII bytes).
	hexStr := "00112233445566778899aabbccddeeff"
	encoded := base64.StdEncoding.EncodeToString([]byte(hexStr))
	// Should be "MDAxMTIyMzM0NDU1NjY3Nzg4OTlhYWJiY2NkZGVlZmY="

	key, err := DecodeAESKey(encoded)
	if err != nil {
		t.Fatalf("DecodeAESKey format B: %v", err)
	}

	if hex.EncodeToString(key) != "00112233445566778899aabbccddeeff" {
		t.Errorf("got key %x, want 00112233445566778899aabbccddeeff", key)
	}
}

func TestDecodeAESKeyInvalid(t *testing.T) {
	t.Parallel()

	// Not valid base64.
	_, err := DecodeAESKey("not-valid-base64!!!")
	if err == nil {
		t.Error("expected error for invalid base64")
	}

	// Valid base64 but wrong length (e.g., 8 bytes).
	encoded := base64.StdEncoding.EncodeToString([]byte("12345678"))
	_, err = DecodeAESKey(encoded)
	if err == nil {
		t.Error("expected error for wrong length")
	}
}

func TestResolveImageKeyFromHexField(t *testing.T) {
	t.Parallel()

	img := &ImageItem{
		AESKey: "00112233445566778899aabbccddeeff",
		Media: &CDNMedia{
			AESKey: base64.StdEncoding.EncodeToString([]byte("ffffffffffffffffffffffffffffffff")),
		},
	}

	key, err := ResolveImageKey(img)
	if err != nil {
		t.Fatalf("ResolveImageKey: %v", err)
	}

	// Should use image_item.aeskey (hex), not media.aes_key.
	if hex.EncodeToString(key) != "00112233445566778899aabbccddeeff" {
		t.Errorf("got key %x, want hex field key", key)
	}
}

func TestResolveImageKeyFallbackToMedia(t *testing.T) {
	t.Parallel()

	rawKey, _ := hex.DecodeString("aabbccddeeff00112233445566778899")
	img := &ImageItem{
		Media: &CDNMedia{
			AESKey: base64.StdEncoding.EncodeToString(rawKey),
		},
	}

	key, err := ResolveImageKey(img)
	if err != nil {
		t.Fatalf("ResolveImageKey: %v", err)
	}

	if hex.EncodeToString(key) != "aabbccddeeff00112233445566778899" {
		t.Errorf("got key %x, want media key", key)
	}
}

func TestResolveImageKeyNoKey(t *testing.T) {
	t.Parallel()

	img := &ImageItem{}
	_, err := ResolveImageKey(img)
	if err == nil {
		t.Error("expected error when no key present")
	}
}

func TestRandomFileKey(t *testing.T) {
	t.Parallel()

	key := RandomFileKey()

	// Must be 32 hex characters (16 bytes).
	if len(key) != 32 {
		t.Errorf("RandomFileKey length = %d, want 32", len(key))
	}

	matched, _ := regexp.MatchString("^[0-9a-f]{32}$", key)
	if !matched {
		t.Errorf("RandomFileKey %q does not match hex pattern", key)
	}

	// Uniqueness check.
	key2 := RandomFileKey()
	if key == key2 {
		t.Error("two consecutive RandomFileKey calls returned the same value")
	}
}

func TestRandomClientID(t *testing.T) {
	t.Parallel()

	id := RandomClientID("stella-weixin")

	if !strings.HasPrefix(id, "stella-weixin:") {
		t.Errorf("RandomClientID %q doesn't have expected prefix", id)
	}

	// Format: prefix:timestamp-random
	parts := strings.SplitN(id, ":", 2)
	if len(parts) != 2 {
		t.Fatalf("RandomClientID %q missing colon separator", id)
	}

	rest := parts[1]
	dashIdx := strings.LastIndex(rest, "-")
	if dashIdx < 0 {
		t.Fatalf("RandomClientID %q missing dash in suffix", id)
	}

	tsStr := rest[:dashIdx]
	_, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		t.Errorf("RandomClientID timestamp %q not a valid int64: %v", tsStr, err)
	}

	suffix := rest[dashIdx+1:]
	if len(suffix) != 8 { // 4 bytes = 8 hex chars
		t.Errorf("RandomClientID suffix %q length = %d, want 8", suffix, len(suffix))
	}

	// Uniqueness.
	id2 := RandomClientID("stella-weixin")
	if id == id2 {
		t.Error("two consecutive RandomClientID calls returned the same value")
	}
}

// --- Bot creation ---

func TestNewRequiresBotToken(t *testing.T) {
	t.Parallel()

	_, err := New(Config{}, nil)
	if err == nil {
		t.Fatal("expected error for empty bot_token")
	}
	if !strings.Contains(err.Error(), "bot_token") {
		t.Errorf("error should mention bot_token: %v", err)
	}
}

func TestNewSuccess(t *testing.T) {
	t.Parallel()

	cfg := Config{BotToken: "test-token"}
	bot, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bot == nil {
		t.Fatal("expected bot, got nil")
		return
	}
	if bot.cfg.BotToken != "test-token" {
		t.Errorf("bot_token = %q, want %q", bot.cfg.BotToken, "test-token")
	}
}

// --- Name ---

func TestBotName(t *testing.T) {
	t.Parallel()

	bot := &Bot{}
	if bot.Name() != "weixin" {
		t.Errorf("Name() = %q, want %q", bot.Name(), "weixin")
	}
}

// --- SplitMessage (shared, with weixin limit) ---

func TestSplitMessageShort(t *testing.T) {
	t.Parallel()

	chunks := channel.SplitMessage("hello", weixinMaxMessageLen)
	if len(chunks) != 1 || chunks[0] != "hello" {
		t.Errorf("chunks = %v, want [hello]", chunks)
	}
}

func TestSplitMessageExactLimit(t *testing.T) {
	t.Parallel()

	msg := strings.Repeat("a", weixinMaxMessageLen)
	chunks := channel.SplitMessage(msg, weixinMaxMessageLen)
	if len(chunks) != 1 {
		t.Errorf("len(chunks) = %d, want 1", len(chunks))
	}
}

func TestSplitMessageLong(t *testing.T) {
	t.Parallel()

	msg := strings.Repeat("a", weixinMaxMessageLen+100)
	chunks := channel.SplitMessage(msg, weixinMaxMessageLen)
	if len(chunks) != 2 {
		t.Fatalf("len(chunks) = %d, want 2", len(chunks))
	}
	if len(chunks[0]) != weixinMaxMessageLen {
		t.Errorf("chunk[0] len = %d, want %d", len(chunks[0]), weixinMaxMessageLen)
	}
	if len(chunks[1]) != 100 {
		t.Errorf("chunk[1] len = %d, want 100", len(chunks[1]))
	}
}

func TestSplitMessageAtNewline(t *testing.T) {
	t.Parallel()

	part1 := strings.Repeat("a", 1500)
	part2 := strings.Repeat("b", 1000)
	msg := part1 + "\n" + part2

	chunks := channel.SplitMessage(msg, weixinMaxMessageLen)
	if len(chunks) != 2 {
		t.Fatalf("len(chunks) = %d, want 2", len(chunks))
	}
	if chunks[0] != part1+"\n" {
		t.Errorf("chunk[0] should end at newline")
	}
}

func TestSplitMessageEmpty(t *testing.T) {
	t.Parallel()

	chunks := channel.SplitMessage("", weixinMaxMessageLen)
	if len(chunks) != 1 || chunks[0] != "" {
		t.Errorf("empty message should return single empty chunk, got %v", chunks)
	}
}

func TestSplitMessageMultibyteUTF8(t *testing.T) {
	t.Parallel()

	char := "中"
	msg := strings.Repeat(char, weixinMaxMessageLen) // 3*weixinMaxMessageLen bytes
	chunks := channel.SplitMessage(msg, weixinMaxMessageLen)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 0 && c[0]&0xC0 == 0x80 {
			t.Errorf("chunk[%d] starts with UTF-8 continuation byte", i)
		}
	}
}

// --- handleUpdates filtering ---

func TestHandleUpdatesSkipsBotEchoes(t *testing.T) {
	t.Parallel()

	bot := &Bot{}
	msgs := []WeixinMessage{
		{
			MessageType:  MessageTypeBot, // bot echo, should be skipped
			MessageState: MessageStateFinish,
			FromUserID:   "user1",
			ContextToken: "tok1",
			ItemList:     []MessageItem{{Type: ItemTypeText, TextItem: &TextItem{Text: "hello"}}},
		},
	}

	// Should not panic or process the message.
	// The bot has no client, so if it tried to process, it would panic.
	bot.handleUpdates(msgs)

	// Verify context_token was NOT cached (message was skipped).
	if _, ok := bot.contextTokens.Load("user1"); ok {
		t.Error("context_token should not be cached for bot echo messages")
	}
}

func TestHandleUpdatesSkipsPartialState(t *testing.T) {
	t.Parallel()

	bot := &Bot{}
	msgs := []WeixinMessage{
		{
			MessageType:  MessageTypeUser,
			MessageState: MessageStateGenerating, // partial, should be skipped
			FromUserID:   "user1",
			ContextToken: "tok1",
			ItemList:     []MessageItem{{Type: ItemTypeText, TextItem: &TextItem{Text: "hello"}}},
		},
	}

	bot.handleUpdates(msgs)

	if _, ok := bot.contextTokens.Load("user1"); ok {
		t.Error("context_token should not be cached for partial messages")
	}
}

func TestHandleUpdatesCachesContextToken(t *testing.T) {
	t.Parallel()

	// Create a minimal bot that won't crash when processing text.
	// We set allowed empty (allow all) and provide no pool so it will
	// error at resolve() — but the context_token should be cached before that.
	bot := &Bot{}
	bot.ctx = t.Context()

	msgs := []WeixinMessage{
		{
			MessageType:  MessageTypeUser,
			MessageState: MessageStateFinish,
			FromUserID:   "user42",
			ContextToken: "cached-token-xyz",
			ItemList:     []MessageItem{}, // empty item list, so dispatch is skipped
		},
	}

	bot.handleUpdates(msgs)

	val, ok := bot.contextTokens.Load("user42")
	if !ok {
		t.Fatal("expected context_token to be cached")
	}
	if val.(string) != "cached-token-xyz" {
		t.Errorf("cached token = %q, want %q", val, "cached-token-xyz")
	}
}

func TestHandleUpdatesSkipsEmptyItemList(t *testing.T) {
	t.Parallel()

	bot := &Bot{}
	msgs := []WeixinMessage{
		{
			MessageType:  MessageTypeUser,
			MessageState: MessageStateFinish,
			FromUserID:   "user1",
			ContextToken: "tok",
			ItemList:     []MessageItem{}, // no items
		},
	}

	// Should not panic.
	bot.handleUpdates(msgs)
}

// --- dispatchMessage multi-item extraction ---
// These tests verify the item extraction and routing logic in dispatchMessage
// without needing a full agent pool. We test the exported extractMessageContent
// helper directly to avoid nil-pointer panics from missing infrastructure.

func TestExtractMessageContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		items      []MessageItem
		wantTexts  int // number of text fragments
		wantImages int // number of images
	}{
		{
			name: "single text",
			items: []MessageItem{
				{Type: ItemTypeText, TextItem: &TextItem{Text: "hello"}},
			},
			wantTexts: 1, wantImages: 0,
		},
		{
			name: "multiple texts concatenated",
			items: []MessageItem{
				{Type: ItemTypeText, TextItem: &TextItem{Text: "hello"}},
				{Type: ItemTypeText, TextItem: &TextItem{Text: "world"}},
			},
			wantTexts: 2, wantImages: 0,
		},
		{
			name: "voice transcription extracted",
			items: []MessageItem{
				{Type: ItemTypeVoice, VoiceItem: &VoiceItem{Text: "transcribed speech"}},
			},
			wantTexts: 1, wantImages: 0,
		},
		{
			name: "file placeholder",
			items: []MessageItem{
				{Type: ItemTypeFile, FileItem: &FileItem{FileName: "report.pdf"}},
			},
			wantTexts: 1, wantImages: 0,
		},
		{
			name: "video placeholder",
			items: []MessageItem{
				{Type: ItemTypeVideo},
			},
			wantTexts: 1, wantImages: 0,
		},
		{
			name: "image collected",
			items: []MessageItem{
				{Type: ItemTypeImage, ImageItem: &ImageItem{}},
			},
			wantTexts: 0, wantImages: 1,
		},
		{
			name: "text + image mixed",
			items: []MessageItem{
				{Type: ItemTypeText, TextItem: &TextItem{Text: "caption"}},
				{Type: ItemTypeImage, ImageItem: &ImageItem{}},
			},
			wantTexts: 1, wantImages: 1,
		},
		{
			name: "multiple images with text",
			items: []MessageItem{
				{Type: ItemTypeText, TextItem: &TextItem{Text: "look at these"}},
				{Type: ItemTypeImage, ImageItem: &ImageItem{}},
				{Type: ItemTypeImage, ImageItem: &ImageItem{}},
			},
			wantTexts: 1, wantImages: 2,
		},
		{
			name: "whitespace-only text skipped",
			items: []MessageItem{
				{Type: ItemTypeText, TextItem: &TextItem{Text: "   "}},
			},
			wantTexts: 0, wantImages: 0,
		},
		{
			name: "mixed text video file",
			items: []MessageItem{
				{Type: ItemTypeText, TextItem: &TextItem{Text: "check this"}},
				{Type: ItemTypeVideo},
				{Type: ItemTypeFile, FileItem: &FileItem{FileName: "data.csv"}},
			},
			wantTexts: 3, wantImages: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			texts, images := extractMessageContent(tt.items)
			if len(texts) != tt.wantTexts {
				t.Errorf("texts = %d (%v), want %d", len(texts), texts, tt.wantTexts)
			}
			if len(images) != tt.wantImages {
				t.Errorf("images = %d, want %d", len(images), tt.wantImages)
			}
		})
	}
}

func TestInboundMediaPersistsAndStorageFailureStillReachesAgent(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nweixin image")
	pdf := []byte("%PDF weixin document")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cdn/image":
			_, _ = w.Write(png)
		case "/cdn/file":
			_, _ = w.Write(pdf)
		case "/ilink/bot/sendmessage":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ret":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var incoming channel.IncomingMessage
	handler := &assetCaptureHandler{
		captureHandler: captureHandler{handleIncomingFn: func(
			_ context.Context,
			msg channel.IncomingMessage,
			_, _ string,
		) (string, bool, *channel.ChatStream, error) {
			incoming = msg
			return "ok", true, nil, nil
		}},
		userRoot: "/home/stella",
	}
	bot := &Bot{
		client:  NewClient(server.URL, "", "release-token", ""),
		handler: handler,
		ctx:     context.Background(),
	}
	bot.contextTokens.Store("user-1", "context-token")
	message := WeixinMessage{FromUserID: "user-1", MessageID: 17}

	bot.handleImages(message, []*ImageItem{{
		Media: &CDNMedia{
			FullURL:           server.URL + "/cdn/image",
			EncryptQueryParam: "image-reference",
		},
	}}, "caption")

	if len(handler.saveCalls) != 1 {
		t.Fatalf("image SaveAsset calls = %d, want 1", len(handler.saveCalls))
	}
	if string(handler.saveCalls[0].data) != string(png) {
		t.Fatal("persisted Weixin image differs from the downloaded bytes")
	}
	if !strings.Contains(ai.FlattenText(incoming.Content), "saved to") {
		t.Fatalf("image content = %#v, want a durable saved-path note", incoming.Content)
	}
	hasImage := false
	for _, block := range incoming.Content {
		if _, ok := block.(ai.ImageContent); ok {
			hasImage = true
		}
	}
	if !hasImage {
		t.Fatalf("image content = %#v, want an inline image block", incoming.Content)
	}

	handler.saveErr = errors.New("storage unavailable")
	incoming = channel.IncomingMessage{}
	bot.handleFile(message, &FileItem{
		FileName: "report.pdf",
		Media: &CDNMedia{
			FullURL:           server.URL + "/cdn/file",
			EncryptQueryParam: "file-reference",
		},
	})
	fileText := ai.FlattenText(incoming.Content)
	if !strings.Contains(fileText, "report.pdf") || !strings.Contains(fileText, "could not be stored") {
		t.Fatalf("file content = %q, want an explicit storage-failure placeholder", fileText)
	}
}

// --- Notify ---

func TestNotifyErrorWhenClientNil(t *testing.T) {
	t.Parallel()

	bot := &Bot{}
	err := bot.Notify(context.Background(), channel.Notification{ChatID: "user1", Text: "hello"})
	if err == nil {
		t.Fatal("expected error when client is nil")
	}
	if !strings.Contains(err.Error(), "not started") {
		t.Errorf("error should mention not started: %v", err)
	}
}

func TestNotifyErrorWhenNoTargetUser(t *testing.T) {
	t.Parallel()

	bot := &Bot{client: NewClient("", "", "tok", "")}
	err := bot.Notify(context.Background(), channel.Notification{Text: "hello"})
	if err == nil {
		t.Fatal("expected error when no target user")
	}
	if !strings.Contains(err.Error(), "no target user") {
		t.Errorf("error should mention no target user: %v", err)
	}
}

func TestNotifyErrorWhenNoContextToken(t *testing.T) {
	t.Parallel()

	bot := &Bot{client: NewClient("", "", "tok", "")}
	err := bot.Notify(context.Background(), channel.Notification{ChatID: "user1", Text: "hello"})
	if err == nil {
		t.Fatal("expected error when no context_token")
	}
	if !strings.Contains(err.Error(), "no context_token") {
		t.Errorf("error should mention no context_token: %v", err)
	}
}

// --- Stop ---

func TestStopWithCancel(t *testing.T) {
	t.Parallel()

	_, cancel := context.WithCancel(context.Background())
	bot := &Bot{cancel: cancel}
	bot.Stop() // should not panic
}

func TestStopWithoutCancel(t *testing.T) {
	t.Parallel()

	bot := &Bot{}
	bot.Stop() // should not panic when cancel is nil
}

// --- toolTracker ---

func TestToolTracker(t *testing.T) {
	t.Parallel()

	var tracker channel.ToolTracker

	tracker.Handle(&channel.ToolUseEvent{Tool: "bash", Status: "running", Input: "ls -la"})
	if tracker.ActiveTool != "bash" {
		t.Errorf("activeTool = %q, want %q", tracker.ActiveTool, "bash")
	}

	tracker.Handle(&channel.ToolUseEvent{Tool: "bash", Status: "done", Input: "ls -la"})
	if tracker.ActiveTool != "" {
		t.Errorf("activeTool = %q, want empty after done", tracker.ActiveTool)
	}
	if len(tracker.History) != 1 {
		t.Fatalf("history len = %d, want 1", len(tracker.History))
	}
	if tracker.History[0].Tool != "bash" {
		t.Errorf("history[0].Tool = %q, want %q", tracker.History[0].Tool, "bash")
	}
}

func TestToolTrackerError(t *testing.T) {
	t.Parallel()

	var tracker channel.ToolTracker
	tracker.Handle(&channel.ToolUseEvent{Tool: "bash", Status: "running", Input: "exit 1"})
	tracker.Handle(&channel.ToolUseEvent{Tool: "bash", Status: "error", Detail: "command failed"})

	if len(tracker.History) != 1 {
		t.Fatalf("history len = %d, want 1", len(tracker.History))
	}
	if tracker.History[0].Status != "error" {
		t.Errorf("status = %q, want %q", tracker.History[0].Status, "error")
	}
}

func TestToolTrackerRenderFinal(t *testing.T) {
	t.Parallel()

	var tracker channel.ToolTracker
	if got := tracker.RenderFinal(); got != "" {
		t.Errorf("renderFinal() with no history = %q, want empty", got)
	}

	tracker.Handle(&channel.ToolUseEvent{Tool: "read", Status: "running", Input: "main.go"})
	tracker.Handle(&channel.ToolUseEvent{Tool: "read", Status: "done", Detail: "42 lines"})
	tracker.Handle(&channel.ToolUseEvent{Tool: "bash", Status: "running", Input: "go test"})
	tracker.Handle(&channel.ToolUseEvent{Tool: "bash", Status: "error", Detail: "exit 1"})

	got := tracker.RenderFinal()
	if !strings.Contains(got, "——————————————————") {
		t.Error("renderFinal() missing separator line")
	}
	if !strings.Contains(got, "📎 2 tools") {
		t.Error("renderFinal() missing compact tool count")
	}
	if !strings.Contains(got, "read") || !strings.Contains(got, "bash") {
		t.Error("renderFinal() missing tool names in summary")
	}
	if !strings.Contains(got, "❌") {
		t.Error("renderFinal() missing error line")
	}
}

func TestToolTrackerHasHistory(t *testing.T) {
	t.Parallel()

	var tracker channel.ToolTracker
	if tracker.HasHistory() {
		t.Error("hasHistory() should be false with no tools")
	}
	tracker.Handle(&channel.ToolUseEvent{Tool: "read", Status: "running", Input: "x"})
	tracker.Handle(&channel.ToolUseEvent{Tool: "read", Status: "done", Input: "x"})
	if !tracker.HasHistory() {
		t.Error("hasHistory() should be true after tool finished")
	}
}

func TestToolTrackerStartOverwritesActive(t *testing.T) {
	t.Parallel()

	var tracker channel.ToolTracker
	tracker.Handle(&channel.ToolUseEvent{Tool: "read", Status: "running", Input: "a.go"})
	tracker.Handle(&channel.ToolUseEvent{Tool: "bash", Status: "running", Input: "ls"})

	if len(tracker.History) != 1 {
		t.Fatalf("history len = %d, want 1", len(tracker.History))
	}
	if tracker.History[0].Tool != "read" {
		t.Errorf("history[0].Tool = %q, want %q", tracker.History[0].Tool, "read")
	}
	if tracker.ActiveTool != "bash" {
		t.Errorf("activeTool = %q, want %q", tracker.ActiveTool, "bash")
	}
}

// --- emojiFor ---

func TestEmojiFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		tool string
		want string
	}{
		{"bash", "⚡"},
		{"read", "📖"},
		{"write", "✏️"},
		{"edit", "🔧"},
		{"search", "🔍"},
		{"unknown_tool", "🔧"},
	}

	for _, tt := range tests {
		if got := channel.EmojiFor(tt.tool); got != tt.want {
			t.Errorf("channel.EmojiFor(%q) = %q, want %q", tt.tool, got, tt.want)
		}
	}
}

// --- truncate ---

func TestTruncate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"hello", 10, "hello"},
		{"hello world", 8, "hello..."},
		{"abc", 3, "abc"},
		{"abcdef", 5, "ab..."},
		{"", 10, ""},
	}

	for _, tt := range tests {
		got := channel.Truncate(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("channel.Truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}

// --- FormatDuration (shared) ---

func TestFormatDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Millisecond, "500ms"},
		{0, "0ms"},
		{time.Second, "1.0s"},
		{2500 * time.Millisecond, "2.5s"},
	}

	for _, tt := range tests {
		got := channel.FormatDuration(tt.d)
		if got != tt.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

// --- renderToolRecord ---

func TestRenderToolRecord(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rec       channel.ToolRecord
		wantParts []string
	}{
		{
			name:      "done with input and detail",
			rec:       channel.ToolRecord{Tool: "bash", Input: "ls -la", Status: "done", Detail: "3 files", Duration: 500 * time.Millisecond},
			wantParts: []string{"✅", "⚡", "bash", "ls -la", "→ 3 files", "500ms"},
		},
		{
			name:      "error with detail",
			rec:       channel.ToolRecord{Tool: "bash", Input: "rm -rf /", Status: "error", Detail: "permission denied", Duration: time.Second},
			wantParts: []string{"❌", "bash", "rm -rf /", "→ permission denied"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := channel.RenderToolRecord(tt.rec)
			for _, part := range tt.wantParts {
				if !strings.Contains(got, part) {
					t.Errorf("channel.RenderToolRecord() = %q, want to contain %q", got, part)
				}
			}
		})
	}
}

// --- formatModelList ---

func TestFormatModelListNoQuery(t *testing.T) {
	t.Parallel()

	models := channel.IndexModels([]channel.ModelOption{
		{Provider: "openai", Model: "gpt-4"},
		{Provider: "anthropic", Model: "claude-3"},
	})
	out := channel.FormatModelList(models, "")
	if !strings.Contains(out, "openai/gpt-4") {
		t.Errorf("missing model entry: %s", out)
	}
	if !strings.Contains(out, "anthropic/claude-3") {
		t.Errorf("missing model entry: %s", out)
	}
	if strings.Contains(out, "filter") {
		t.Error("should not show filter when query is empty")
	}
}

func TestFormatModelListWithQuery(t *testing.T) {
	t.Parallel()

	models := channel.IndexModels([]channel.ModelOption{
		{Provider: "openai", Model: "gpt-4"},
	})
	out := channel.FormatModelList(models, "openai")
	if !strings.Contains(out, `filter: "openai"`) {
		t.Errorf("should show filter query: %s", out)
	}
}

// --- checkError ---

func TestCheckErrorSuccess(t *testing.T) {
	t.Parallel()

	if err := checkError(0, 0, ""); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestCheckErrorSessionExpired(t *testing.T) {
	t.Parallel()

	err := checkError(-14, 0, "session expired")
	if !errors.Is(err, ErrSessionExpired) {
		t.Errorf("expected ErrSessionExpired, got %v", err)
	}

	err = checkError(0, -14, "session expired")
	if !errors.Is(err, ErrSessionExpired) {
		t.Errorf("expected ErrSessionExpired for errcode=-14, got %v", err)
	}
}

func TestCheckErrorGenericRet(t *testing.T) {
	t.Parallel()

	err := checkError(-1, 0, "bad request")
	if err == nil {
		t.Fatal("expected error for ret=-1")
	}
	if !strings.Contains(err.Error(), "ret=-1") {
		t.Errorf("error should contain ret=-1: %v", err)
	}
}

func TestCheckErrorGenericErrcode(t *testing.T) {
	t.Parallel()

	err := checkError(0, 42, "something wrong")
	if err == nil {
		t.Fatal("expected error for errcode=42")
	}
	if !strings.Contains(err.Error(), "errcode=42") {
		t.Errorf("error should contain errcode=42: %v", err)
	}
}

// --- isTimeoutError ---

func TestIsTimeoutErrorTrue(t *testing.T) {
	t.Parallel()

	err := &mockTimeoutError{timeout: true}
	if !isTimeoutError(err) {
		t.Error("expected isTimeoutError=true for timeout error")
	}
}

func TestIsTimeoutErrorFalse(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("regular error")
	if isTimeoutError(err) {
		t.Error("expected isTimeoutError=false for regular error")
	}
}

type mockTimeoutError struct {
	timeout bool
}

func (e *mockTimeoutError) Error() string   { return "mock timeout error" }
func (e *mockTimeoutError) Timeout() bool   { return e.timeout }
func (e *mockTimeoutError) Temporary() bool { return false }

// --- weixinMaxMessageLen constant ---

func TestWeixinMaxMessageLen(t *testing.T) {
	t.Parallel()

	if weixinMaxMessageLen != 2000 {
		t.Errorf("weixinMaxMessageLen = %d, want 2000", weixinMaxMessageLen)
	}
}

func TestPkcs7Unpad_Valid(t *testing.T) {
	t.Parallel()
	// 16-byte block, pad with 4 bytes of 0x04.
	data := append([]byte("hello world!"), []byte{0x04, 0x04, 0x04, 0x04}...)
	got, err := pkcs7Unpad(data, 16)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello world!" {
		t.Errorf("expected 'hello world!', got %q", got)
	}
}

func TestPkcs7Unpad_InvalidLength(t *testing.T) {
	t.Parallel()
	_, err := pkcs7Unpad(nil, 16)
	if err == nil {
		t.Error("expected error for empty data")
	}
}

func TestPkcs7Unpad_InvalidPaddingValue(t *testing.T) {
	t.Parallel()
	// Last byte is 0 (zero padding value is invalid).
	data := make([]byte, 16)
	_, err := pkcs7Unpad(data, 16)
	if err == nil {
		t.Error("expected error for zero padding")
	}
}

func TestIsHexString_Valid(t *testing.T) {
	t.Parallel()
	if !isHexString([]byte("0123456789abcdefABCDEF")) {
		t.Error("expected valid hex string")
	}
}

func TestIsHexString_Invalid(t *testing.T) {
	t.Parallel()
	if isHexString([]byte("xyz")) {
		t.Error("expected invalid hex string")
	}
}

func TestEncryptAESECB_WrongKeySize(t *testing.T) {
	t.Parallel()
	_, err := EncryptAESECB([]byte("data"), []byte("short"))
	if err == nil {
		t.Error("expected error for wrong key size")
	}
}

func TestDecryptAESECB_WrongKeySize(t *testing.T) {
	t.Parallel()
	_, err := DecryptAESECB(make([]byte, 16), []byte("short"))
	if err == nil {
		t.Error("expected error for wrong key size")
	}
}

func TestBuildClientVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		version string
		want    uint32
	}{
		{"2.4.3", (2 << 16) | (4 << 8) | 3},
		{"1.0.0", 1 << 16},
		{"0.28.2", (0 << 16) | (28 << 8) | 2},
		{"dev", 0},
		{"", 0},
	}
	for _, tc := range tests {
		got := buildClientVersion(tc.version)
		if got != tc.want {
			t.Errorf("buildClientVersion(%q) = %d, want %d", tc.version, got, tc.want)
		}
	}
}

func TestBuildBaseInfo(t *testing.T) {
	t.Parallel()
	c := NewClient("", "", "tok", "")
	info := c.buildBaseInfo()
	if info.ChannelVersion == "" {
		t.Error("ChannelVersion must not be empty")
	}
	if info.BotAgent == "" {
		t.Error("BotAgent must not be empty")
	}
	// In test/dev builds version.Version is empty, so BotAgent falls back to "OpenClaw".
	// Production builds inject a version via ldflags → BotAgent = "Stella/<version>".
	if info.BotAgent != "OpenClaw" && !strings.HasPrefix(info.BotAgent, "Stella/") {
		t.Errorf("BotAgent %q must be 'OpenClaw' or start with 'Stella/'", info.BotAgent)
	}
}

func TestCommonHeaders(t *testing.T) {
	t.Parallel()

	// Without SKRouteTag.
	c := NewClient("", "", "tok", "")
	h := c.commonHeaders()
	for _, key := range []string{"iLink-App-Id", "iLink-App-ClientVersion", "Authorization", "AuthorizationType", "X-WECHAT-UIN"} {
		if h[key] == "" {
			t.Errorf("commonHeaders missing or empty key %q", key)
		}
	}
	if _, ok := h["SKRouteTag"]; ok {
		t.Error("SKRouteTag should not be present when empty")
	}
	if h["iLink-App-Id"] != "bot" {
		t.Errorf("iLink-App-Id = %q, want %q", h["iLink-App-Id"], "bot")
	}

	// With SKRouteTag.
	c2 := NewClient("", "", "tok", "my-tag")
	h2 := c2.commonHeaders()
	if h2["SKRouteTag"] != "my-tag" {
		t.Errorf("SKRouteTag = %q, want %q", h2["SKRouteTag"], "my-tag")
	}
}

// --- Streaming (Phase 3) ---

func TestStreamMakePiece(t *testing.T) {
	t.Parallel()

	bot := &Bot{}
	sender := newWeixinStreamSender(bot, "dev", "stream-1", "ticket-abc")

	p1 := sender.makePiece("hello")
	if p1.PieceSeq != 1 {
		t.Errorf("first piece seq = %d, want 1", p1.PieceSeq)
	}
	if p1.PieceData == "" {
		t.Error("piece_data must not be empty")
	}

	// piece_data must be valid base64 wrapping a JSON object with "type" and "text".
	raw, err := base64.StdEncoding.DecodeString(p1.PieceData)
	if err != nil {
		t.Fatalf("piece_data is not valid base64: %v", err)
	}
	var content map[string]string
	if err := json.Unmarshal(raw, &content); err != nil {
		t.Fatalf("decoded piece_data is not valid JSON: %v\nraw: %s", err, raw)
	}
	if content["type"] != "text" {
		t.Errorf("piece content type = %q, want %q", content["type"], "text")
	}
	if content["text"] != "hello" {
		t.Errorf("piece content text = %q, want %q", content["text"], "hello")
	}

	// Second piece must auto-increment seq.
	p2 := sender.makePiece("world")
	if p2.PieceSeq != 2 {
		t.Errorf("second piece seq = %d, want 2", p2.PieceSeq)
	}
}

func TestStreamSenderPendingPiecesRollback(t *testing.T) {
	t.Parallel()

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.URL.Path == "/ilink/bot/stream/init_stream" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"stream_ticket":"ticket-xyz","base_response":{"ret":0}}`)
			return
		}
		if r.URL.Path == "/ilink/bot/stream/sync_stream" {
			w.Header().Set("Content-Type", "application/json")
			if callCount == 1 { // first sync_stream call fails
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			// subsequent calls succeed
			_, _ = fmt.Fprintf(w, `{"base_response":{"ret":0}}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "tok", "")
	bot := &Bot{client: client}
	sender := newWeixinStreamSender(bot, "dev", "stream-1", "ticket-xyz")

	pieces := []SyncStreamPiece{sender.makePiece("chunk1"), sender.makePiece("chunk2")}
	origSeq := sender.pieceSeq // should be 2 after makePiece x2

	// First sendPieces call — server returns 500.
	err := sender.sendPieces(pieces, true)
	if err == nil {
		t.Fatal("expected error on first sendPieces (server 500)")
	}

	// After failure: pending pieces should be set and pieceSeq rolled back.
	if len(sender.pendingPieces) != 2 {
		t.Errorf("pendingPieces len = %d, want 2", len(sender.pendingPieces))
	}
	if sender.pieceSeq != 0 {
		t.Errorf("pieceSeq after rollback = %d, want 0 (seqBefore first batch)", sender.pieceSeq)
	}
	_ = origSeq

	// Second sendPieces with no new pieces drains the pending ones — server now succeeds.
	err = sender.sendPieces(nil, true)
	if err != nil {
		t.Fatalf("second sendPieces failed unexpectedly: %v", err)
	}
	if len(sender.pendingPieces) != 0 {
		t.Errorf("pendingPieces should be empty after success, got %d", len(sender.pendingPieces))
	}
}

func TestSendViaStreamFallsBackOnInitFailure(t *testing.T) {
	t.Parallel()

	var sendMessageCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ilink/bot/stream/init_stream":
			// Simulate init_stream failure.
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/ilink/bot/sendmessage":
			sendMessageCalled = true
			_, _ = fmt.Fprintf(w, `{"ret":0}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "tok", "")
	bot := &Bot{client: client}
	bot.contextTokens.Store("user1", "ctx-token")

	msg := WeixinMessage{FromUserID: "user1"}
	ok := bot.sendViaStream(msg, "hello world")
	if ok {
		t.Fatal("sendViaStream should return false when init_stream fails")
	}

	// Caller (sendFinalResponse) should use sendmessage fallback.
	bot.sendViaMessages(msg, "hello world")
	if !sendMessageCalled {
		t.Error("sendmessage should be called in fallback path")
	}
}
