//go:build system

package system

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// testGroupNewSession drives the Web group session reset end to end against the
// subprocess. It covers three seams no in-process test reaches:
//
//   - the typed `/new` path through real HTTP: the command is answered before the
//     event log is written, and the rotation it triggers is committed by the
//     server process, not a fake dispatch runner;
//   - the ingest watermark surviving rotation across requests, so the successor
//     session starts clean instead of replaying the group's whole event log;
//   - the `session_control` two-phase confirmation, which only exists because
//     cmd/stellad registers the builtin tool — request and confirmation live in
//     two separate HTTP turns, exactly the structure the tool's server-side gate
//     is built to enforce.
//
// Every model reply is scripted, and the group is single-member with explicit
// @mentions, so routing takes the deterministic rule path and never asks a
// model who should answer.
func (h *harness) testGroupNewSession(t *testing.T) {
	fake := newFakeAnthropic(t)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	const modelID = "claude-sonnet-4-6"
	providerID := h.createGroupProvider(t, ctx, fake.baseURL())
	agentID := h.createGroupAgent(t, ctx, providerID+"/"+modelID)
	groupID := h.createGroup(t, ctx, agentID)

	// 1. One real group turn, so the agent has a group session with content and
	//    an ingest watermark to lose.
	seedText := "@" + agentID + " seed " + h.runID
	seedReply := "seeded group, run " + h.runID
	fake.enqueueText(seedReply)
	if got := h.sendGroupMessage(t, ctx, groupID, seedText); got != seedReply {
		t.Fatalf("group turn text = %q, want scripted %q\n%s", got, seedReply, h.proc.logTail(40))
	}
	firstSession := h.activeGroupSession(t, ctx, groupID, agentID)
	watermark := h.awaitGroupWatermark(t, ctx, groupID, agentID)

	// 2. Typed `/new`: intercepted before the event-log append, rotates the one
	//    agent in the group.
	if got := h.sendGroupMessage(t, ctx, groupID, "/new"); got != pkgchannel.NewSessionStartedMessage {
		t.Fatalf("/new reply = %q, want %q\n%s", got, pkgchannel.NewSessionStartedMessage, h.proc.logTail(40))
	}
	secondSession := h.assertRotated(t, ctx, groupID, agentID, firstSession)
	h.assertGroupEventLogExcludes(t, ctx, groupID, "/new")
	if got := h.groupWatermark(t, ctx, groupID, agentID); got != watermark {
		t.Fatalf("ingest watermark = %d after /new, want it unchanged at %d (a reset watermark replays the whole event log into the new session)", got, watermark)
	}

	// 3. The next message lands in the successor, and the surviving watermark
	//    keeps the pre-rotation event log out of it.
	afterText := "@" + agentID + " after reset " + h.runID
	afterReply := "fresh context, run " + h.runID
	fake.enqueueText(afterReply)
	if got := h.sendGroupMessage(t, ctx, groupID, afterText); got != afterReply {
		t.Fatalf("post-rotation turn text = %q, want scripted %q\n%s", got, afterReply, h.proc.logTail(40))
	}
	contents := h.sessionMessageContents(t, ctx, secondSession)
	if !containsSubstring(contents, "after reset "+h.runID) {
		t.Fatalf("successor session %s has no message from the post-rotation turn; messages: %q\n%s", secondSession, contents, h.proc.logTail(40))
	}
	if containsSubstring(contents, "seed "+h.runID) {
		t.Fatalf("successor session %s replayed pre-rotation group history; messages: %q", secondSession, contents)
	}

	// 4. Agent-driven reset: request in one turn, confirm in the next. The nonce
	//    is read from the database rather than the prompt, so the fake never has
	//    to branch on prose.
	askText := "确认要开一个新会话吗？"
	fake.enqueueTool("session_control", `{"action":"request_new"}`)
	fake.enqueueText(askText)
	if got := h.sendGroupMessage(t, ctx, groupID, "@"+agentID+" 开个新会话"); got != askText {
		t.Fatalf("request_new turn text = %q, want scripted %q\n%s", got, askText, h.proc.logTail(40))
	}
	if got := h.activeGroupSession(t, ctx, groupID, agentID); got != secondSession {
		t.Fatalf("request_new rotated the session to %s; it must only record a pending confirmation", got)
	}
	nonce := h.awaitRotationNonce(t, ctx, secondSession)

	doneText := "已经开始新会话了。"
	fake.enqueueTool("session_control", fmt.Sprintf(`{"action":"confirm_new","nonce":%q}`, nonce))
	fake.enqueueText(doneText)
	if got := h.sendGroupMessage(t, ctx, groupID, "@"+agentID+" 确认"); got != doneText {
		t.Fatalf("confirm_new turn text = %q, want scripted %q\n%s", got, doneText, h.proc.logTail(40))
	}
	h.assertRotated(t, ctx, groupID, agentID, secondSession)
}

// createGroupProvider registers the run-scoped provider for this journey. Its id
// is distinct from every other journey's so the shared server holds all of them
// at once without collision.
func (h *harness) createGroupProvider(t *testing.T, ctx context.Context, baseURL string) string {
	t.Helper()
	id := "anthropic-group-" + h.runID
	resp := h.postJSON(t, ctx, "/api/providers", map[string]any{
		"id":       id,
		"type":     "anthropic",
		"name":     id,
		"enabled":  true,
		"api_key":  "system-test-not-a-secret",
		"base_url": baseURL,
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/providers = %d, want %d\n%s", resp.StatusCode, http.StatusCreated, h.proc.logTail(40))
	}
	return id
}

// createGroupAgent creates the single agent this journey's group is built
// around and returns its server-assigned id, which is also its @mention target
// in a Web group.
func (h *harness) createGroupAgent(t *testing.T, ctx context.Context, model string) string {
	t.Helper()
	resp := h.postJSON(t, ctx, "/api/agents", map[string]any{
		"name":    "sys-test-group-agent-" + h.runID,
		"model":   model,
		"enabled": true,
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/agents = %d, want %d\n%s", resp.StatusCode, http.StatusCreated, h.proc.logTail(40))
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode agent response: %v", err)
	}
	if created.ID == "" {
		t.Fatal("created group agent has empty id")
	}
	return created.ID
}

// createGroup creates a Web group with exactly one agent member — the shape
// where a bare `/new` needs no target — and returns its id.
func (h *harness) createGroup(t *testing.T, ctx context.Context, agentID string) string {
	t.Helper()
	resp := h.postJSON(t, ctx, "/api/groups", map[string]any{
		"group_name": "sys-test-group-" + h.runID,
		"agent_ids":  []string{agentID},
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/groups = %d, want %d\n%s", resp.StatusCode, http.StatusCreated, h.proc.logTail(40))
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode group response: %v", err)
	}
	if created.ID == "" {
		t.Fatal("created group has empty id")
	}
	return created.ID
}

// sendGroupMessage posts one message to the group and consumes the SSE response
// to completion, returning the text assembled from its text-delta frames. Both
// group outcomes stream the same way: an intercepted command replies with its
// text directly, an ordinary message streams the agent's turn.
func (h *harness) sendGroupMessage(t *testing.T, ctx context.Context, groupID, content string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"content": content})
	if err != nil {
		t.Fatalf("marshal group send body: %v", err)
	}
	path := fmt.Sprintf("/api/groups/%s/messages", groupID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+path, strings.NewReader(string(payload)))
	if err != nil {
		t.Fatalf("build group send request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("POST group message: %v\n%s", err, h.proc.logTail(40))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		drainBody(resp.Body)
		t.Fatalf("POST group message = %d, want %d\n%s", resp.StatusCode, http.StatusOK, h.proc.logTail(40))
	}

	var (
		text strings.Builder
		done bool
	)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			done = true
			break
		}
		var evt turnEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			t.Fatalf("group SSE frame is not valid JSON: %q: %v", data, err)
		}
		if evt.Type == "error" {
			t.Fatalf("group turn emitted an error frame: %q\n%s", data, h.proc.logTail(40))
		}
		if evt.Type == "text-delta" {
			text.WriteString(evt.Delta)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read group SSE stream: %v\n%s", err, h.proc.logTail(40))
	}
	if !done {
		t.Fatalf("group SSE stream ended without a [DONE] sentinel\n%s", h.proc.logTail(40))
	}
	return text.String()
}

// activeGroupSession returns the session id of the agent's live group
// conversation. The binding is the durable one rotation preserves: the group id
// stands in for the user, plus the agent and kind=chat. Exactly one row may be
// active — a second one would mean a rotation archived nothing and the chat now
// has two candidate sessions — so anything else fails here.
func (h *harness) activeGroupSession(t *testing.T, ctx context.Context, groupID, agentID string) string {
	t.Helper()
	rows, err := h.db.Query(ctx,
		`SELECT session_id
		   FROM ctx_conversation
		  WHERE user_id = $1 AND agent_id = $2 AND kind = 'chat' AND archived = false`,
		groupID, agentID)
	if err != nil {
		t.Fatalf("query active group session (group %s, agent %s): %v\n%s", groupID, agentID, err, h.proc.logTail(40))
	}
	defer rows.Close()
	var active []string
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			t.Fatalf("scan active group session: %v", err)
		}
		active = append(active, sessionID)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read active group sessions: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("group %s has %d active sessions for agent %s (%v), want exactly 1\n%s",
			groupID, len(active), agentID, active, h.proc.logTail(40))
	}
	return active[0]
}

// assertRotated proves a rotation replaced previousSession: the predecessor is
// archived (kept, so its history stays searchable) and exactly one new active
// conversation now holds the same binding. It returns the successor's id.
func (h *harness) assertRotated(t *testing.T, ctx context.Context, groupID, agentID, previousSession string) string {
	t.Helper()
	var archived bool
	if err := h.db.QueryRow(ctx,
		`SELECT archived FROM ctx_conversation WHERE session_id = $1`, previousSession).Scan(&archived); err != nil {
		t.Fatalf("query rotated-away session %s: %v", previousSession, err)
	}
	if !archived {
		t.Fatalf("session %s archived = false after rotation; the predecessor must be archived, not left active", previousSession)
	}
	successor := h.activeGroupSession(t, ctx, groupID, agentID)
	if successor == previousSession {
		t.Fatalf("active group session is still %s after rotation", previousSession)
	}
	return successor
}

// assertGroupEventLogExcludes proves no group event-log message carries the
// given text. Commands are answered before the append precisely so `/new` never
// becomes part of the context it exists to clear.
func (h *harness) assertGroupEventLogExcludes(t *testing.T, ctx context.Context, groupID, text string) {
	t.Helper()
	var count int
	if err := h.db.QueryRow(ctx,
		`SELECT count(*) FROM ctx_group_message WHERE group_id = $1 AND content LIKE '%' || $2 || '%'`,
		groupID, text).Scan(&count); err != nil {
		t.Fatalf("query group event log for %q: %v", text, err)
	}
	if count != 0 {
		t.Fatalf("group event log holds %d message(s) containing %q, want 0", count, text)
	}
}

// groupWatermark returns the agent's group ingest watermark, or -1 when no
// cursor row exists yet.
func (h *harness) groupWatermark(t *testing.T, ctx context.Context, groupID, agentID string) int64 {
	t.Helper()
	var lastSeq int64
	err := h.db.QueryRow(ctx,
		`SELECT last_seq FROM ctx_group_ingest_cursor WHERE group_id = $1 AND pipeline = $2`,
		groupID, "lcm:"+agentID).Scan(&lastSeq)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return -1
	case err != nil:
		t.Fatalf("query group ingest cursor (group %s, agent %s): %v", groupID, agentID, err)
	}
	return lastSeq
}

// awaitGroupWatermark waits for the agent's ingest cursor to advance past the
// start of the log. The cursor is written as the turn assembles context, which
// can trail the stream's last frame.
func (h *harness) awaitGroupWatermark(t *testing.T, ctx context.Context, groupID, agentID string) int64 {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if seq := h.groupWatermark(t, ctx, groupID, agentID); seq > 0 {
			return seq
		}
		if time.Now().After(deadline) {
			t.Fatalf("group ingest cursor for agent %s never advanced past 0\n%s", agentID, h.proc.logTail(40))
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// sessionMessageContents returns every message body persisted in one session,
// oldest first.
func (h *harness) sessionMessageContents(t *testing.T, ctx context.Context, sessionID string) []string {
	t.Helper()
	rows, err := h.db.Query(ctx,
		`SELECT m.content
		   FROM ctx_message m
		   JOIN ctx_conversation c ON c.id = m.conversation_id
		  WHERE c.session_id = $1
		  ORDER BY m.seq ASC`, sessionID)
	if err != nil {
		t.Fatalf("query messages for session %s: %v", sessionID, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			t.Fatalf("scan message for session %s: %v", sessionID, err)
		}
		out = append(out, content)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read messages for session %s: %v", sessionID, err)
	}
	return out
}

// awaitRotationNonce returns the pending, unused rotation nonce the tool
// recorded for the session. Reading it from the database keeps the fake free of
// prompt parsing: the confirmation turn is scripted with the real nonce the
// server issued.
func (h *harness) awaitRotationNonce(t *testing.T, ctx context.Context, sessionID string) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var id string
		err := h.db.QueryRow(ctx,
			`SELECT id::text FROM agent_session_rotation_nonce
			  WHERE session_id = $1 AND used_at IS NULL
			  ORDER BY created_at DESC LIMIT 1`, sessionID).Scan(&id)
		switch {
		case err == nil:
			return id
		case !errors.Is(err, pgx.ErrNoRows):
			t.Fatalf("query rotation nonce for session %s: %v", sessionID, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("no pending rotation nonce for session %s; request_new never recorded one\n%s", sessionID, h.proc.logTail(40))
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func containsSubstring(values []string, want string) bool {
	for _, v := range values {
		if strings.Contains(v, want) {
			return true
		}
	}
	return false
}
