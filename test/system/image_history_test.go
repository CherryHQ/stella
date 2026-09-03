//go:build system

package system

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// testImageHistory proves the complete cross-request image contract through the
// real stellad process and a scripted model endpoint: the same pixels reach the
// configured baseline VLM and the active answer turn, canonical history stores
// no Base64, the next answer turn receives only the immutable baseline, and
// authorized history can load the byte-identical original.
func (h *harness) testImageHistory(t *testing.T) {
	fake := newFakeAnthropic(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	baselineReply := "## Text\nsystem test pixels\n\n## Scene\nAn eight-by-eight synthetic PNG contains a color grid."
	firstReply := "active image received, run " + h.runID
	secondReply := "historical baseline received, run " + h.runID
	fake.enqueueText(baselineReply)
	fake.enqueueText(firstReply)
	fake.enqueueText(secondReply)

	const modelID = "claude-sonnet-4-6"
	providerID := h.createFakeProviderNamed(t, ctx, fake.baseURL(), "anthropic-image-"+h.runID)
	h.setVisionModel(t, ctx, providerID+"/"+modelID)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		h.setVisionModel(t, cleanupCtx, "")
	})
	agentID := h.createAgent(t, ctx, providerID+"/"+modelID)
	sessionID := h.createSession(t, ctx, agentID)

	original := systemPNG(t, 90)
	encoded := base64.StdEncoding.EncodeToString(original)
	firstEvents, gotFirstReply := h.streamChatParts(t, ctx, agentID, sessionID, []map[string]any{
		{"type": "image", "image": encoded, "mimeType": "image/png"},
		{"type": "text", "text": "describe this active image " + h.runID},
	})
	assertTurnEventOrder(t, firstEvents)
	if gotFirstReply != firstReply {
		t.Fatalf("first assistant text = %q, want %q", gotFirstReply, firstReply)
	}

	baseline, mediaID := h.assertCanonicalImageStored(t, ctx, sessionID, "user", encoded, int64(len(original)))
	if baseline != baselineReply {
		t.Fatalf("stored baseline = %q, want scripted VLM baseline %q", baseline, baselineReply)
	}

	secondEvents, gotSecondReply := h.streamChatTurn(t, ctx, agentID, sessionID, "what was in the prior image? "+h.runID)
	assertTurnEventOrder(t, secondEvents)
	if gotSecondReply != secondReply {
		t.Fatalf("second assistant text = %q, want %q", gotSecondReply, secondReply)
	}

	reqs := fake.requests()
	if len(reqs) != 3 {
		t.Fatalf("fake received %d model requests, want baseline render + 2 answer turns", len(reqs))
	}
	for i := range 2 {
		if reqs[i].Model != modelID || len(reqs[i].Images) != 1 {
			t.Fatalf("image request %d model/images = %q/%#v, want %q and one image", i, reqs[i].Model, reqs[i].Images, modelID)
		}
		if got := reqs[i].Images[0]; got.MediaType != "image/png" || got.Data != encoded {
			t.Fatalf("image request %d changed pixels: mime=%q bytes_equal=%t", i, got.MediaType, got.Data == encoded)
		}
	}
	if len(reqs[2].Images) != 0 {
		t.Fatalf("historical request leaked %d image blocks", len(reqs[2].Images))
	}
	if !messagesContain(reqs[2].Messages, baseline) {
		t.Fatalf("historical request omitted stored baseline %q: %#v", baseline, reqs[2].Messages)
	}

	h.assertHistoryLoadsOriginal(t, ctx, agentID, sessionID, "user", mediaID, original)
}

// testViewImageToolHistory proves the production tool loop, not just direct
// upload: the answer model requests view_image, the tool's image is canonically
// persisted through the configured VLM, pixels remain active for the follow-up
// answer call, and the next user turn safely receives baseline-only history.
func (h *harness) testViewImageToolHistory(t *testing.T) {
	fake := newFakeAnthropic(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	const modelID = "claude-sonnet-4-6"
	providerID := h.createFakeProviderNamed(t, ctx, fake.baseURL(), "anthropic-view-image-"+h.runID)
	h.setVisionModel(t, ctx, providerID+"/"+modelID)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		h.setVisionModel(t, cleanupCtx, "")
	})
	agentID := h.createAgent(t, ctx, providerID+"/"+modelID)
	sessionID := h.createSession(t, ctx, agentID)

	// Distinct pixels from image_history's: same owner, so identical bytes would
	// resolve to that journey's already-described media object and this journey
	// would never reach the VLM. See systemPNG.
	original := systemPNG(t, 200)
	encoded := base64.StdEncoding.EncodeToString(original)
	imagePath := h.uploadWorkspaceImage(t, ctx, agentID, sessionID, original)
	toolArgs, err := json.Marshal(map[string]string{"path": imagePath})
	if err != nil {
		t.Fatal(err)
	}
	baselineReply := "## Text\nview_image tool pixels\n\n## Scene\nAn eight-by-eight synthetic PNG contains a color grid."
	activeReply := "view_image tool image received, run " + h.runID
	historyReply := "view_image tool baseline received, run " + h.runID
	fake.enqueueTool("toolu_view_image", "view_image", string(toolArgs))
	fake.enqueueText(baselineReply)
	fake.enqueueText(activeReply)
	fake.enqueueText(historyReply)

	firstEvents, gotActiveReply := h.streamChatTurn(t, ctx, agentID, sessionID, "view the uploaded image at "+imagePath)
	assertTurnEventOrder(t, firstEvents)
	if gotActiveReply != activeReply {
		t.Fatalf("tool-loop assistant text = %q, want %q", gotActiveReply, activeReply)
	}
	baseline, mediaID := h.assertCanonicalImageStored(t, ctx, sessionID, "tool", encoded, int64(len(original)))
	if baseline != baselineReply {
		t.Fatalf("stored view_image baseline = %q, want %q", baseline, baselineReply)
	}

	historyEvents, gotHistoryReply := h.streamChatTurn(t, ctx, agentID, sessionID, "what did the viewed image contain? "+h.runID)
	assertTurnEventOrder(t, historyEvents)
	if gotHistoryReply != historyReply {
		t.Fatalf("history assistant text = %q, want %q", gotHistoryReply, historyReply)
	}

	reqs := fake.requests()
	if len(reqs) != 4 {
		t.Fatalf("fake received %d requests, want tool call + VLM + active follow-up + history", len(reqs))
	}
	if len(reqs[0].Images) != 0 || !containsString(reqs[0].ToolNames, "view_image") {
		t.Fatalf("initial LLM request = images:%d tools:%v, want no images and view_image tool", len(reqs[0].Images), reqs[0].ToolNames)
	}
	for _, i := range []int{1, 2} {
		if reqs[i].Model != modelID || len(reqs[i].Images) != 1 {
			t.Fatalf("view_image request %d model/images = %q/%#v", i, reqs[i].Model, reqs[i].Images)
		}
		if got := reqs[i].Images[0]; got.MediaType != "image/png" || got.Data != encoded {
			t.Fatalf("view_image request %d changed pixels: mime=%q bytes_equal=%t", i, got.MediaType, got.Data == encoded)
		}
	}
	if len(reqs[3].Images) != 0 || !messagesContain(reqs[3].Messages, baseline) {
		t.Fatalf("view_image history projection = images:%d messages:%#v, want zero images plus baseline %q", len(reqs[3].Images), reqs[3].Messages, baseline)
	}

	h.assertHistoryLoadsOriginal(t, ctx, agentID, sessionID, "tool", mediaID, original)
}

func (h *harness) uploadWorkspaceImage(t *testing.T, ctx context.Context, agentID, sessionID string, data []byte) string {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "view-image-tool.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/agents/%s/sessions/%s/workspace/upload", agentID, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+path, &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("upload view_image fixture: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload view_image fixture = %d, want 201", resp.StatusCode)
	}
	var uploaded struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&uploaded); err != nil {
		t.Fatal(err)
	}
	if uploaded.Path == "" {
		t.Fatal("workspace upload returned empty path")
	}
	return uploaded.Path
}

func containsString(values []string, want string) bool {
	return slices.Contains(values, want)
}

func (h *harness) setVisionModel(t *testing.T, ctx context.Context, model string) {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"model_vision": model})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, h.baseURL+"/api/default-models", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("PUT default models: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT default models = %d, want 200", resp.StatusCode)
	}
}

// assertCanonicalImageStored proves the durable shape of one stored image: the
// baseline lives on the media object (never copied onto the part), the parent
// message's text projection carries that same baseline, and no provider Base64
// reached the canonical row.
func (h *harness) assertCanonicalImageStored(t *testing.T, ctx context.Context, sessionID, role, encoded string, size int64) (baseline, mediaID string) {
	t.Helper()
	var (
		parentContent string
		partText      *string // image parts carry no text of their own; NULL is the contract
		mimeType      string
		storedSize    int64
	)
	// The baseline is a column of ctx_media, keyed by (owner, sha256), not of the
	// part that happens to reference it (migration 90000000000030): one image
	// forwarded into two messages is described once. Reading it from the part
	// would only ever see NULL.
	err := h.db.QueryRow(ctx, `
		SELECT m.content, p.text_content, media.baseline, p.media_id::text, media.mime_type, media.size_bytes
		  FROM ctx_conversation c
		  JOIN ctx_message m ON m.conversation_id = c.id
		  JOIN ctx_message_part p ON p.message_id = m.id AND p.part_type = 'image'
		  JOIN ctx_media media ON media.id = p.media_id
		 WHERE c.session_id = $1 AND m.role = $2
		 ORDER BY m.seq, p.ordinal
		 LIMIT 1`, sessionID, role).Scan(&parentContent, &partText, &baseline, &mediaID, &mimeType, &storedSize)
	if err != nil {
		t.Fatalf("query canonical image history: %v\n%s", err, h.proc.LogTail(40))
	}
	if partText != nil {
		t.Fatalf("canonical image part froze a text copy %q; the baseline belongs to ctx_media alone", *partText)
	}
	if baseline == "" {
		t.Fatal("canonical image media has empty baseline")
	}
	projectedParent := parentContent
	if role == "tool" {
		var envelope struct {
			Result string `json:"result"`
		}
		if err := json.Unmarshal([]byte(parentContent), &envelope); err != nil {
			t.Fatalf("decode canonical tool parent: %v", err)
		}
		projectedParent = envelope.Result
	}
	if !strings.Contains(projectedParent, baseline) {
		t.Fatalf("canonical parent/baseline = %q/%q", parentContent, baseline)
	}
	if strings.Contains(parentContent, encoded) {
		t.Fatal("canonical ctx_message.content contains provider Base64")
	}
	if mediaID == "" || mimeType != "image/png" || storedSize != size {
		t.Fatalf("ctx_media = id:%q mime:%q size:%d, want image/png size %d", mediaID, mimeType, storedSize, size)
	}
	return baseline, mediaID
}

func (h *harness) assertHistoryLoadsOriginal(t *testing.T, ctx context.Context, agentID, sessionID, role, mediaID string, original []byte) {
	t.Helper()
	path := fmt.Sprintf("/api/agents/%s/sessions/%s/messages", agentID, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET session history: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET session history = %d, want 200\n%s", resp.StatusCode, h.proc.LogTail(40))
	}
	var history struct {
		Messages []struct {
			Role   string `json:"role"`
			Blocks []struct {
				Type     string `json:"type"`
				MediaID  string `json:"media_id"`
				MimeType string `json:"mime_type"`
				URL      string `json:"url"`
			} `json:"blocks"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&history); err != nil {
		t.Fatalf("decode session history: %v", err)
	}
	var mediaURL string
	for _, message := range history.Messages {
		for _, block := range message.Blocks {
			if message.Role == role && block.Type == "image" && block.MediaID == mediaID {
				if block.MimeType != "image/png" {
					t.Fatalf("history image MIME = %q", block.MimeType)
				}
				mediaURL = block.URL
			}
		}
	}
	if mediaURL == "" {
		t.Fatalf("history omitted image media %s: %#v", mediaID, history.Messages)
	}

	mediaReq, err := http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+mediaURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	mediaResp, err := h.client.Do(mediaReq)
	if err != nil {
		t.Fatalf("GET history media: %v", err)
	}
	defer func() { _ = mediaResp.Body.Close() }()
	if mediaResp.StatusCode != http.StatusOK {
		t.Fatalf("GET history media = %d, want 200", mediaResp.StatusCode)
	}
	got, err := io.ReadAll(mediaResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if mediaResp.Header.Get("Content-Type") != "image/png" || !bytes.Equal(got, original) {
		t.Fatalf("history original changed: content-type=%q bytes_equal=%t", mediaResp.Header.Get("Content-Type"), bytes.Equal(got, original))
	}
}

// systemPNG builds an 8x8 synthetic PNG whose blue channel is the caller's, so
// two journeys can hold provably different bytes.
//
// That parameter is load-bearing, not decoration. Session media is deduplicated
// by (owner, sha256) and the baseline is a property of that media row (#1183),
// and every journey here runs as the same bootstrap user. Two journeys sharing
// one byte stream would therefore share one media object: the second would adopt
// the first's stored description, skip the VLM entirely, and assert nothing at
// all about the render path it exists to cover. A journey that measures baseline
// rendering owns its own pixels.
func systemPNG(t *testing.T, blue uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := range 8 {
		for x := range 8 {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x * 20), G: uint8(y * 20), B: blue, A: 255})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func messagesContain(messages []string, want string) bool {
	for _, message := range messages {
		if strings.Contains(message, want) {
			return true
		}
	}
	return false
}
