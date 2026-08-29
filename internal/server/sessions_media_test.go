package server_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestSessionMediaAuthorizationCachingAndTypedHistory(t *testing.T) {
	env := setupAdmin(t)
	agentID := createAgentAsUser(t, env, env.bearerToken, "Media history agent")
	mediaID, sessionID, bytes := seedSessionImage(t, env, agentID)
	base := "/api/agents/" + agentID + "/sessions/" + sessionID

	rr := doRequest(t, env, http.MethodGet, base+"/media/"+mediaID, nil)
	if rr.Code != http.StatusOK || rr.Body.String() != string(bytes) {
		t.Fatalf("media status=%d body=%q", rr.Code, rr.Body.String())
	}
	digest := sha256.Sum256(bytes)
	wantDigest := hex.EncodeToString(digest[:])
	wantETag := `"` + wantDigest + `"`
	if got := rr.Header().Get("ETag"); got != wantETag {
		t.Fatalf("ETag=%q, want %q", got, wantETag)
	}
	for header, want := range map[string]string{
		"Content-Type":           "image/png",
		"X-Content-Type-Options": "nosniff",
		"Content-Disposition":    "inline",
		"Cache-Control":          "private, no-cache",
	} {
		if got := rr.Header().Get(header); got != want {
			t.Fatalf("%s=%q, want %q", header, got, want)
		}
	}
	if csp := rr.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") || !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("CSP=%q", csp)
	}

	// The handler is exercised through the generated router above. A request
	// with If-None-Match verifies ServeContent's conditional response.
	request := httptest.NewRequest(http.MethodGet, base+"/media/"+mediaID, nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: env.bearerToken})
	request.Header.Set("If-None-Match", wantETag)
	response := httptest.NewRecorder()
	env.srv.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotModified {
		t.Fatalf("conditional status=%d body=%q, want 304", response.Code, response.Body.String())
	}

	for _, path := range []string{
		base + "/media/not-a-uuid",
		base + "/media/" + uuid.NewString(),
		"/api/agents/not-" + agentID + "/sessions/" + sessionID + "/media/" + mediaID,
		"/api/agents/" + agentID + "/sessions/no-such-session/media/" + mediaID,
	} {
		if got := doRequest(t, env, http.MethodGet, path, nil); got.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d body=%s, want opaque 404", path, got.Code, got.Body.String())
		}
	}
	_, foreignToken := newNonAdmin(t, env, "media-foreign")
	if got := doRequestWithSession(t, env.srv, foreignToken, http.MethodGet, base+"/media/"+mediaID, nil); got.Code != http.StatusNotFound {
		t.Fatalf("foreign user status=%d body=%s, want 404", got.Code, got.Body.String())
	}
	otherSessionID := "media-session-" + uuid.NewString()
	if err := env.mem.(memory.SessionManager).SaveInfo(context.Background(), memory.SessionInfo{
		ID: otherSessionID, AgentID: agentID, UserID: env.adminUser.ID, Channel: "web", Kind: "chat",
		CreatedAt: time.Now().UTC(), LastActive: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	otherBase := "/api/agents/" + agentID + "/sessions/" + otherSessionID
	if got := doRequest(t, env, http.MethodGet, otherBase+"/media/"+mediaID, nil); got.Code != http.StatusNotFound {
		t.Fatalf("same-user other session status=%d body=%s, want scoped 404", got.Code, got.Body.String())
	}

	history := doRequest(t, env, http.MethodGet, base+"/messages?limit=20", nil)
	if history.Code != http.StatusOK {
		t.Fatalf("history status=%d body=%s", history.Code, history.Body.String())
	}
	var messages apitypes.SessionMessageList
	if err := json.Unmarshal(history.Body.Bytes(), &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages.Messages) != 2 || messages.Messages[0].Blocks == nil {
		t.Fatalf("messages=%+v, want user and tool blocks", messages.Messages)
	}
	blocks := *messages.Messages[0].Blocks
	marker := "[file: /user/assets/photo.png]"
	if len(blocks) != 4 || blocks[0].Type != apitypes.SessionMessageBlockTypeText || blocks[1].Type != apitypes.SessionMessageBlockTypeText || blocks[2].Type != apitypes.SessionMessageBlockTypeImage || blocks[3].Type != apitypes.SessionMessageBlockTypeText {
		t.Fatalf("user blocks=%+v, want ordered text/marker/image/text", blocks)
	}
	if blocks[1].Text == nil || *blocks[1].Text != marker {
		t.Fatalf("user marker=%+v, want durable marker", blocks[1])
	}
	if blocks[2].MediaId == nil || *blocks[2].MediaId != mediaID || blocks[2].Url == nil || *blocks[2].Url != base+"/media/"+mediaID {
		t.Fatalf("image block=%+v", blocks[2])
	}
	if messages.Messages[0].Content == nil || *messages.Messages[0].Content != "before\n"+marker+"\nafter" {
		t.Fatalf("user content=%v, want visible durable text without parent baseline", messages.Messages[0].Content)
	}
	tool := messages.Messages[1]
	if tool.IsError == nil || !*tool.IsError || tool.Blocks == nil || (*tool.Blocks)[2].Type != apitypes.SessionMessageBlockTypeImage {
		t.Fatalf("tool message=%+v, want error-preserving image blocks", tool)
	}
	body := history.Body.String()
	for _, forbidden := range []string{wantDigest, "session-media", "host/path", "base64"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("history leaked %q: %s", forbidden, body)
		}
	}
}

// This exercises the shipped persistence boundary, not the row helper: a
// normal chat session is appended through memory.Provider and then exported by
// the generated sessions endpoint for the Harbor driver.
func TestSessionMessagesExportCanonicalChildAudit(t *testing.T) {
	env := setupAdmin(t)
	agentID := createAgentAsUser(t, env, env.bearerToken, "Child audit agent")
	sessionID := "child-audit-" + uuid.NewString()
	now := time.Now().UTC()
	if err := env.mem.(memory.SessionManager).SaveInfo(context.Background(), memory.SessionInfo{
		ID: sessionID, AgentID: agentID, UserID: env.adminUser.ID, Channel: "web", Kind: "chat", CreatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.mem.Append(context.Background(), memory.Session{ID: sessionID, AgentID: agentID, UserID: env.adminUser.ID, Channel: "web"}, ai.ToolResultMessage{
		ToolCallID: "outer", ToolName: "code", Content: []ai.ContentBlock{ai.TextContent{Text: "done"}},
		ChildToolCalls: []ai.ChildToolCallAudit{{ID: "outer:1", Name: "bash", IsError: true, ErrorKind: ai.ToolErrorKindTool}},
	}); err != nil {
		t.Fatal(err)
	}
	rr := doRequest(t, env, http.MethodGet, "/api/agents/"+agentID+"/sessions/"+sessionID+"/messages?limit=20", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("messages status=%d body=%s", rr.Code, rr.Body.String())
	}
	var messages apitypes.SessionMessageList
	if err := json.Unmarshal(rr.Body.Bytes(), &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages.Messages) != 1 || messages.Messages[0].ChildCalls == nil || len(*messages.Messages[0].ChildCalls) != 1 {
		t.Fatalf("API child calls = %#v", messages.Messages)
	}
	child := (*messages.Messages[0].ChildCalls)[0]
	if child.Id != "outer:1" || child.Name != "bash" || !child.IsError || child.ErrorKind == nil || *child.ErrorKind != apitypes.SessionChildToolCallAuditErrorKindToolError {
		t.Fatalf("API child call = %#v", child)
	}
}

func seedSessionImage(t *testing.T, env *testEnv, agentID string) (mediaID, sessionID string, data []byte) {
	t.Helper()
	ctx := context.Background()
	sessionID = "media-session-" + uuid.NewString()
	now := time.Now().UTC()
	if err := env.mem.(memory.SessionManager).SaveInfo(ctx, memory.SessionInfo{ID: sessionID, AgentID: agentID, UserID: env.adminUser.ID, Channel: "web", Kind: "chat", CreatedAt: now, LastActive: now}); err != nil {
		t.Fatal(err)
	}
	q := sqlc.New(env.db)
	conversation, err := q.GetConversationBySessionID(ctx, sqlc.GetConversationBySessionIDParams{SessionID: sessionID, UserID: pgtype.Text{String: env.adminUser.ID, Valid: true}, AgentID: pgtype.Text{String: agentID, Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	data = []byte("durable png bytes")
	digest := sha256.Sum256(data)
	userID, err := uuid.Parse(env.adminUser.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.deps.Assets.SessionMedia().PutSessionMedia(ctx, asset.UserMediaOwner(userID), digest, data); err != nil {
		t.Fatal(err)
	}
	media, err := q.CreateMediaIfAbsent(ctx, sqlc.CreateMediaIfAbsentParams{UserID: pgtype.Text{String: userID.String(), Valid: true}, Sha256: digest[:], MimeType: "image/png", SizeBytes: int64(len(data))})
	if err != nil {
		t.Fatal(err)
	}
	mediaID = media.ID
	user, err := q.CreateMessage(ctx, sqlc.CreateMessageParams{ID: uuid.NewString(), ConversationID: conversation.ID, Seq: 1, Role: "user", EventType: "multimodal", Content: "before\nstored baseline\nafter", TokenCount: 1, ActorType: string(eventlog.ActorHuman)})
	if err != nil {
		t.Fatal(err)
	}
	tool, err := q.CreateMessage(ctx, sqlc.CreateMessageParams{ID: uuid.NewString(), ConversationID: conversation.ID, Seq: 2, Role: "tool", EventType: "tool_result", Content: `{"id":"call-1","tool":"read","result":"stored baseline","is_error":true}`, TokenCount: 1, ActorType: string(eventlog.ActorAgent)})
	if err != nil {
		t.Fatal(err)
	}
	for _, messageID := range []string{user.ID, tool.ID} {
		for _, part := range []struct {
			ordinal int64
			text    string
		}{{ordinal: 0, text: "before"}, {ordinal: 1, text: "[file: /user/assets/photo.png]"}, {ordinal: 3, text: "after"}} {
			if _, err := q.CreateMessagePart(ctx, sqlc.CreateMessagePartParams{ID: uuid.NewString(), MessageID: messageID, PartType: "text", Ordinal: part.ordinal, TextContent: pgtype.Text{String: part.text, Valid: true}}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := q.CreateMessagePart(ctx, sqlc.CreateMessagePartParams{ID: uuid.NewString(), MessageID: messageID, PartType: "image", Ordinal: 2, MediaID: pgtype.Text{String: mediaID, Valid: true}, TextContent: pgtype.Text{String: "stored baseline", Valid: true}}); err != nil {
			t.Fatal(err)
		}
	}
	return mediaID, sessionID, data
}
