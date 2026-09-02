package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
)

// fakeStore is an in-memory store for tests.
type fakeStore struct {
	sessions map[string]Info
	// saveErrOnce injects one save failure after the resolver has read its
	// candidate. Tests use it to model an archive winning that exact race.
	saveErrOnce error
	// afterSaveErr mutates durable state after saveErrOnce is observed, before the
	// resolver performs its bounded re-read.
	afterSaveErr func()
	// saveCalls lets retry tests prove the inactive-session path is bounded.
	saveCalls int
	// rotateErr, when set, fails rotate so callers can prove a failed rotation
	// leaves the predecessor active (the store's transaction rolled back).
	rotateErr error
	// beforeRotate runs inside rotate, standing in for a concurrent writer that
	// commits between the caller's resolve and the compare-and-rotate.
	beforeRotate func()
}

func newFakeStore() *fakeStore {
	return &fakeStore{sessions: make(map[string]Info)}
}

func (f *fakeStore) save(_ context.Context, info Info) error {
	f.saveCalls++
	if f.saveErrOnce != nil {
		err := f.saveErrOnce
		f.saveErrOnce = nil
		if f.afterSaveErr != nil {
			f.afterSaveErr()
		}
		return err
	}
	f.sessions[info.ID] = info
	return nil
}

func (f *fakeStore) archive(_ context.Context, info Info) (bool, error) {
	existing, ok := f.sessions[info.ID]
	if !ok || existing.Archived {
		return false, nil
	}
	existing.Archived = true
	f.sessions[info.ID] = existing
	return true, nil
}

func TestEnsureRejectsOversizedTitleBeforeStorage(t *testing.T) {
	store := newFakeStore()
	r := NewRegistryWithStore(store, "agent")
	_, err := r.Ensure(context.Background(), Request{
		UserID: "user", Title: strings.Repeat("界", MaxTitleBytes/len("界")+1), CreateIfMissing: true,
	})
	if !errors.Is(err, ErrTitleTooLong) {
		t.Fatalf("Ensure error = %v, want ErrTitleTooLong", err)
	}
	if len(store.sessions) != 0 {
		t.Fatalf("stored sessions = %d, want 0", len(store.sessions))
	}
}

func (f *fakeStore) rotate(_ context.Context, expectedSessionID string, successor Info) error {
	if f.beforeRotate != nil {
		f.beforeRotate()
	}
	if f.rotateErr != nil {
		return f.rotateErr
	}
	expected, ok := f.sessions[expectedSessionID]
	if !ok || expected.Archived {
		return fmt.Errorf("%w: %s", ErrStaleRotation, expectedSessionID)
	}
	expected.Archived = true
	f.sessions[expectedSessionID] = expected
	f.sessions[successor.ID] = successor
	return nil
}

func (f *fakeStore) load(_ context.Context, sessionID, userID, _ string) (Info, error) {
	info, ok := f.sessions[sessionID]
	if !ok {
		return Info{}, errors.New("not found")
	}
	if userID != "" && info.UserID != userID {
		return Info{}, errors.New("not found")
	}
	return info, nil
}

func (f *fakeStore) list(_ context.Context, userID, agentID string, opts memory.ListOptions) ([]Info, error) {
	var out []Info
	for _, info := range f.sessions {
		if userID != "" && info.UserID != userID {
			continue
		}
		if agentID != "" && info.AgentID != agentID {
			continue
		}
		if opts.Kind != "" && info.Kind != opts.Kind {
			continue
		}
		if opts.Channel != "" && info.Channel != opts.Channel {
			continue
		}
		if opts.ProjectIDIsNull && info.ProjectID != "" {
			continue
		}
		if opts.ProjectID != "" && info.ProjectID != opts.ProjectID {
			continue
		}
		if !opts.IncludeArchived && info.Archived {
			continue
		}
		out = append(out, info)
	}
	return out, nil
}

func (f *fakeStore) listForReview(ctx context.Context, agentID string, opts memory.ListOptions) ([]Info, error) {
	return f.list(ctx, "", agentID, opts)
}

func (f *fakeStore) listForAdmin(ctx context.Context, userID, agentID string, opts memory.ListOptions) ([]Info, error) {
	return f.list(ctx, userID, agentID, opts)
}

func newTestRegistry(t *testing.T) (*Registry, *fakeStore) {
	t.Helper()
	s := newFakeStore()
	r := NewRegistryWithStore(s, "agent1")
	return r, s
}

// TestEnsure_GeneratedCreate creates a new session with a generated ID.
func TestEnsure_GeneratedCreate(t *testing.T) {
	r, _ := newTestRegistry(t)
	info, err := r.Ensure(context.Background(), Request{
		UserID:          "u1",
		Kind:            KindChat,
		Channel:         ChannelWeb,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if info.ID == "" {
		t.Error("expected generated ID")
	}
	if info.Kind != string(KindChat) {
		t.Errorf("kind = %q, want %q", info.Kind, KindChat)
	}
	if info.UserID != "u1" {
		t.Errorf("userID = %q, want u1", info.UserID)
	}
}

// TestEnsure_ExactIDCreate creates a session with an explicit ID.
func TestEnsure_ExactIDCreate(t *testing.T) {
	r, _ := newTestRegistry(t)
	info, err := r.Ensure(context.Background(), Request{
		ID:                 "explicit-id-1",
		UserID:             "u1",
		Kind:               KindDelegate,
		Channel:            ChannelDelegate,
		CreateIfMissing:    true,
		AllowExactIDCreate: true,
	})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if info.ID != "explicit-id-1" {
		t.Errorf("ID = %q, want explicit-id-1", info.ID)
	}
}

// TestEnsure_Resume resumes an existing session.
func TestEnsure_Resume(t *testing.T) {
	r, s := newTestRegistry(t)
	now := time.Now().UTC()
	existing := NewInfo("sess-1", "agent1", "u1", "web", KindChat, "", now)
	s.sessions["sess-1"] = existing

	info, err := r.Ensure(context.Background(), Request{
		ID:     "sess-1",
		UserID: "u1",
	})
	if err != nil {
		t.Fatalf("Ensure resume: %v", err)
	}
	if info.ID != "sess-1" {
		t.Errorf("ID = %q, want sess-1", info.ID)
	}
}

// TestEnsure_WrongKind rejects resuming with the wrong kind.
func TestEnsure_WrongKind(t *testing.T) {
	r, s := newTestRegistry(t)
	now := time.Now().UTC()
	existing := NewInfo("sess-1", "agent1", "u1", "web", KindChat, "", now)
	s.sessions["sess-1"] = existing

	_, err := r.Ensure(context.Background(), Request{
		ID:          "sess-1",
		UserID:      "u1",
		RequireKind: KindDelegate,
	})
	if !errors.Is(err, ErrWrongKind) {
		t.Errorf("expected ErrWrongKind, got %v", err)
	}
}

// TestEnsure_WrongUser returns not-found for cross-user access.
func TestEnsure_WrongUser(t *testing.T) {
	r, s := newTestRegistry(t)
	now := time.Now().UTC()
	existing := NewInfo("sess-1", "agent1", "u1", "web", KindChat, "", now)
	s.sessions["sess-1"] = existing

	_, err := r.Ensure(context.Background(), Request{
		ID:     "sess-1",
		UserID: "other-user",
	})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for wrong user, got %v", err)
	}
}

// TestEnsure_ArchivedRejected rejects writes to archived sessions.
func TestEnsure_ArchivedRejected(t *testing.T) {
	r, s := newTestRegistry(t)
	now := time.Now().UTC()
	archived := NewInfo("sess-archived", "agent1", "u1", "web", KindChat, "", now)
	archived.Archived = true
	s.sessions["sess-archived"] = archived

	_, err := r.Ensure(context.Background(), Request{
		ID:     "sess-archived",
		UserID: "u1",
	})
	if !errors.Is(err, ErrArchived) {
		t.Errorf("expected ErrArchived, got %v", err)
	}
}

// TestEnsure_MissingUserID returns error when UserID is absent.
func TestEnsure_MissingUserID(t *testing.T) {
	r, _ := newTestRegistry(t)
	_, err := r.Ensure(context.Background(), Request{
		Kind:            KindChat,
		CreateIfMissing: true,
	})
	if err == nil {
		t.Error("expected error for missing UserID")
	}
}

// TestGet_Success retrieves an existing session.
func TestGet_Success(t *testing.T) {
	r, s := newTestRegistry(t)
	now := time.Now().UTC()
	s.sessions["s1"] = NewInfo("s1", "agent1", "u1", "web", KindChat, "", now)

	info, err := r.Get(context.Background(), Scope{UserID: "u1", AgentID: "agent1"}, "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if info.ID != "s1" {
		t.Errorf("ID = %q, want s1", info.ID)
	}
}

// TestGet_CrossUserDenied denies cross-user access (returns not found, not forbidden, to hide existence).
func TestGet_CrossUserDenied(t *testing.T) {
	r, s := newTestRegistry(t)
	now := time.Now().UTC()
	s.sessions["s1"] = NewInfo("s1", "agent1", "u1", "web", KindChat, "", now)

	_, err := r.Get(context.Background(), Scope{UserID: "u2", AgentID: "agent1"}, "s1")
	if err == nil {
		t.Error("expected error for cross-user access")
	}
}

// TestList_KindFilter returns only sessions matching the requested kind.
func TestList_KindFilter(t *testing.T) {
	r, s := newTestRegistry(t)
	now := time.Now().UTC()
	s.sessions["chat-1"] = NewInfo("chat-1", "agent1", "u1", "web", KindChat, "", now)
	s.sessions["del-1"] = NewInfo("del-1", "agent1", "u1", "delegate", KindDelegate, "", now)

	scope := Scope{UserID: "u1", AgentID: "agent1"}
	infos, err := r.List(context.Background(), scope, ListOptions{Kinds: []Kind{KindChat}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 1 || infos[0].ID != "chat-1" {
		t.Errorf("expected [chat-1], got %v", infos)
	}
}

// TestArchive archives a session.
func TestArchive(t *testing.T) {
	r, s := newTestRegistry(t)
	now := time.Now().UTC()
	s.sessions["s1"] = NewInfo("s1", "agent1", "u1", "web", KindChat, "", now)

	if err := r.Archive(context.Background(), Scope{UserID: "u1", AgentID: "agent1"}, "s1"); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if !s.sessions["s1"].Archived {
		t.Error("expected session to be archived")
	}
}

// TestReviewPolicy_ExcludesDelegate verifies delegate sessions are excluded by default.
func TestReviewPolicy_ExcludesDelegate(t *testing.T) {
	policy := DefaultReviewPolicy()
	if policy.Includes(KindDelegate) {
		t.Error("default policy should exclude delegate sessions")
	}
	if !policy.Includes(KindChat) {
		t.Error("default policy should include chat sessions")
	}
	if !policy.Includes(KindMain) {
		t.Error("default policy should include main sessions")
	}
}

// TestListForReview excludes delegate sessions but keeps archived ones: a
// rotated-away session is archived immediately and its final messages still have
// to be distilled. Watermark comparison, not archival, ends review candidacy.
func TestListForReview(t *testing.T) {
	r, s := newTestRegistry(t)
	now := time.Now().UTC()
	s.sessions["chat-1"] = NewInfo("chat-1", "agent1", "u1", "web", KindChat, "", now)
	s.sessions["del-1"] = NewInfo("del-1", "agent1", "u2", "delegate", KindDelegate, "", now)
	archived := NewInfo("arch-1", "agent1", "u1", "web", KindChat, "", now)
	archived.Archived = true
	s.sessions["arch-1"] = archived

	infos, err := r.ListForReview(context.Background(), ReviewRequest{AgentID: "agent1"})
	if err != nil {
		t.Fatalf("ListForReview: %v", err)
	}
	got := make(map[string]bool, len(infos))
	for _, i := range infos {
		if Kind(i.Kind) == KindDelegate {
			t.Errorf("delegate session %q should be excluded", i.ID)
		}
		got[i.ID] = true
	}
	if !got["chat-1"] || !got["arch-1"] || len(infos) != 2 {
		t.Errorf("expected [chat-1 arch-1], got %v", infos)
	}
}

// TestMemoryScope converts session Info to memory.Session without hand-building.
func TestMemoryScope(t *testing.T) {
	r, _ := newTestRegistry(t)
	now := time.Now().UTC()
	info := NewInfo("s1", "agent1", "u1", "web", KindChat, "", now)

	scope, err := r.MemoryScope(info)
	if err != nil {
		t.Fatalf("MemoryScope: %v", err)
	}
	if scope.ID != "s1" || scope.AgentID != "agent1" || scope.UserID != "u1" {
		t.Errorf("unexpected scope: %+v", scope)
	}
}

// TestResolveMain_CreatesFresh creates a main session when none exists.
func TestResolveMain_CreatesFresh(t *testing.T) {
	r, _ := newTestRegistry(t)

	info, err := r.ResolveMain(context.Background(), MainRequest{
		UserID:  "u1",
		AgentID: "agent1",
	})
	if err != nil {
		t.Fatalf("ResolveMain: %v", err)
	}
	if info.Kind != string(KindMain) {
		t.Errorf("kind = %q, want %q", info.Kind, KindMain)
	}
}

// TestResolveMain_ReturnsExisting returns an existing main session.
func TestResolveMain_ReturnsExisting(t *testing.T) {
	r, s := newTestRegistry(t)
	now := time.Now().UTC()
	main := NewInfo("main-1", "agent1", "u1", "agent1:user:u1:private", KindMain, "", now)
	s.sessions["main-1"] = main

	info, err := r.ResolveMain(context.Background(), MainRequest{
		UserID:  "u1",
		AgentID: "agent1",
	})
	if err != nil {
		t.Fatalf("ResolveMain: %v", err)
	}
	if info.ID != "main-1" {
		t.Errorf("ID = %q, want main-1", info.ID)
	}
}

func TestResolveMain_IgnoresProjectMainWhenAgentMainExists(t *testing.T) {
	r, s := newTestRegistry(t)
	now := time.Now().UTC()
	projectMain := NewInfo("project-main-1", "agent1", "u1", "web", KindMain, "project-1", now.Add(time.Minute))
	agentMain := NewInfo("agent-main-1", "agent1", "u1", "agent1:user:u1:private", KindMain, "", now)
	s.sessions["project-main-1"] = projectMain
	s.sessions["agent-main-1"] = agentMain

	info, err := r.ResolveMain(context.Background(), MainRequest{
		UserID:  "u1",
		AgentID: "agent1",
	})
	if err != nil {
		t.Fatalf("ResolveMain: %v", err)
	}
	if info.ID != "agent-main-1" {
		t.Errorf("ID = %q, want agent-main-1", info.ID)
	}
	if info.ProjectID != "" {
		t.Errorf("ProjectID = %q, want empty", info.ProjectID)
	}
}

func TestResolveMain_CreatesAgentMainWhenOnlyProjectMainExists(t *testing.T) {
	r, s := newTestRegistry(t)
	now := time.Now().UTC()
	projectMain := NewInfo("project-main-1", "agent1", "u1", "web", KindMain, "project-1", now)
	s.sessions["project-main-1"] = projectMain

	info, err := r.ResolveMain(context.Background(), MainRequest{
		UserID:  "u1",
		AgentID: "agent1",
	})
	if err != nil {
		t.Fatalf("ResolveMain: %v", err)
	}
	if info.ID == "project-main-1" {
		t.Fatal("ResolveMain reused project main as agent main")
	}
	if info.Kind != string(KindMain) {
		t.Errorf("Kind = %q, want %q", info.Kind, KindMain)
	}
	if info.ProjectID != "" {
		t.Errorf("ProjectID = %q, want empty", info.ProjectID)
	}
	if !strings.Contains(info.Channel, ":user:u1:") {
		t.Errorf("Channel = %q, want private user channel", info.Channel)
	}
}

func TestResolveMain_ReResolvesAfterPromotionLosesArchiveRace(t *testing.T) {
	r, s := newTestRegistry(t)
	now := time.Now().UTC()
	s.sessions["chat-raced"] = NewInfo("chat-raced", "agent1", "u1", "agent1:user:u1:private", KindChat, "", now)
	s.saveErrOnce = memory.ErrInactiveSession
	s.afterSaveErr = func() {
		archived := s.sessions["chat-raced"]
		archived.Archived = true
		s.sessions[archived.ID] = archived
	}

	resolved, err := r.ResolveMain(context.Background(), MainRequest{UserID: "u1", AgentID: "agent1"})
	if err != nil {
		t.Fatalf("ResolveMain after archive race: %v", err)
	}
	if resolved.ID == "chat-raced" || resolved.Kind != string(KindMain) || resolved.Archived {
		t.Fatalf("resolved = %+v, want a fresh active main", resolved)
	}
	if !s.sessions["chat-raced"].Archived {
		t.Fatal("raced candidate must remain archived")
	}
}

func TestResolveChatChannel_ReResolvesAfterLegacyAdoptionLosesArchiveRace(t *testing.T) {
	r, s := newTestRegistry(t)
	now := time.Now().UTC()
	s.sessions["legacy-chat"] = NewInfo("legacy-chat", "agent1", "u1", "", KindChat, "", now)
	s.saveErrOnce = memory.ErrInactiveSession
	s.afterSaveErr = func() {
		archived := s.sessions["legacy-chat"]
		archived.Archived = true
		s.sessions[archived.ID] = archived
	}
	req := ChannelRequest{UserID: "u1", AgentID: "agent1", Channel: ChannelWeb, LegacyID: "legacy-chat"}

	resolved, err := r.ResolveChatChannel(context.Background(), req)
	if err != nil {
		t.Fatalf("ResolveChatChannel after archive race: %v", err)
	}
	if resolved.ID == "legacy-chat" || resolved.Channel != string(ChannelWeb) || resolved.Archived {
		t.Fatalf("resolved = %+v, want a fresh active channel session", resolved)
	}
	if !s.sessions["legacy-chat"].Archived {
		t.Fatal("raced legacy session must remain archived")
	}
}

func TestResolveMain_InactiveRetryIsBounded(t *testing.T) {
	r, s := newTestRegistry(t)
	now := time.Now().UTC()
	s.sessions["chat-still-active"] = NewInfo("chat-still-active", "agent1", "u1", "agent1:user:u1:private", KindChat, "", now)
	s.saveErrOnce = memory.ErrInactiveSession
	s.afterSaveErr = func() {
		// Inject the second failure without changing the candidate so a broken
		// unbounded retry would spin forever.
		s.saveErrOnce = memory.ErrInactiveSession
	}

	_, err := r.ResolveMain(context.Background(), MainRequest{UserID: "u1", AgentID: "agent1"})
	if !errors.Is(err, memory.ErrInactiveSession) {
		t.Fatalf("ResolveMain = %v, want ErrInactiveSession after bounded retry", err)
	}
	if s.saveCalls != 2 {
		t.Fatalf("save calls = %d, want exactly 2", s.saveCalls)
	}
}

// TestEnsure_ResumeGroupReconciliation covers validateResume's handling of a
// requested GroupID against the durable one.
func TestEnsure_ResumeGroupReconciliation(t *testing.T) {
	const (
		gid1 = "11111111-1111-4111-8111-111111111111"
		gid2 = "22222222-2222-4222-8222-222222222222"
	)
	now := time.Now().UTC()

	seedGroup := func(s *fakeStore, id, userID, groupID string) {
		s.sessions[id] = Info{
			ID: id, AgentID: "agent1", UserID: userID, GroupID: groupID,
			Channel: "group:" + userID, Kind: string(KindChat), CreatedAt: now, LastActive: now,
		}
	}

	t.Run("durable group matches", func(t *testing.T) {
		r, s := newTestRegistry(t)
		seedGroup(s, "g", gid1, gid1)
		info, err := r.Ensure(context.Background(), Request{ID: "g", UserID: gid1, GroupID: gid1})
		if err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		if info.GroupID != gid1 {
			t.Fatalf("GroupID = %q, want %q", info.GroupID, gid1)
		}
	})

	t.Run("conflicting durable group rejected", func(t *testing.T) {
		r, s := newTestRegistry(t)
		seedGroup(s, "g", gid1, gid1)
		_, err := r.Ensure(context.Background(), Request{ID: "g", UserID: gid1, GroupID: gid2})
		if !errors.Is(err, ErrForbidden) {
			t.Fatalf("err = %v, want ErrForbidden", err)
		}
	})

	t.Run("legacy null row reattaches for matching owner", func(t *testing.T) {
		r, s := newTestRegistry(t)
		seedGroup(s, "g", gid1, "") // durable group_id is NULL, owner is the group
		info, err := r.Ensure(context.Background(), Request{ID: "g", UserID: gid1, GroupID: gid1})
		if err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		if info.GroupID != gid1 {
			t.Fatalf("GroupID after reattach = %q, want %q", info.GroupID, gid1)
		}
	})

	t.Run("legacy null row rejects non-owner", func(t *testing.T) {
		r, s := newTestRegistry(t)
		seedGroup(s, "g", "some-user", "") // owner is not the requested group
		_, err := r.Ensure(context.Background(), Request{ID: "g", UserID: "some-user", GroupID: gid1})
		if !errors.Is(err, ErrForbidden) {
			t.Fatalf("err = %v, want ErrForbidden", err)
		}
	})
}

// --- RotateMain -------------------------------------------------------------

// TestRotateMain_ArchivesOldMain proves /new hands back a fresh, empty main and
// leaves the previous one archived but intact.
func TestRotateMain_ArchivesOldMain(t *testing.T) {
	r, s := newTestRegistry(t)
	now := time.Now().UTC()
	s.sessions["main-1"] = NewInfo("main-1", "agent1", "u1", "agent1:user:u1:private", KindMain, "", now)

	successor, err := r.RotateMain(context.Background(), MainRequest{UserID: "u1", AgentID: "agent1"})
	if err != nil {
		t.Fatalf("RotateMain: %v", err)
	}
	if successor.ID == "main-1" {
		t.Fatal("RotateMain must return a new session, not the rotated one")
	}
	if successor.Kind != string(KindMain) || successor.Archived {
		t.Fatalf("successor = %+v, want an active main", successor)
	}
	if !strings.Contains(successor.Channel, ":user:u1:") {
		t.Errorf("successor channel = %q, want a private user channel", successor.Channel)
	}
	if !s.sessions["main-1"].Archived {
		t.Error("the rotated main must be archived, not deleted")
	}

	resolved, err := r.ResolveMain(context.Background(), MainRequest{UserID: "u1", AgentID: "agent1"})
	if err != nil {
		t.Fatalf("ResolveMain after rotation: %v", err)
	}
	if resolved.ID != successor.ID {
		t.Errorf("ResolveMain = %q, want the successor %q", resolved.ID, successor.ID)
	}
}

// TestRotateMain_CreatesWhenNoMainExists treats rotation from nothing as a
// plain create rather than an error.
func TestRotateMain_CreatesWhenNoMainExists(t *testing.T) {
	r, _ := newTestRegistry(t)

	info, err := r.RotateMain(context.Background(), MainRequest{UserID: "u1", AgentID: "agent1"})
	if err != nil {
		t.Fatalf("RotateMain: %v", err)
	}
	if info.Kind != string(KindMain) {
		t.Errorf("kind = %q, want %q", info.Kind, KindMain)
	}
}

// TestRotateMain_StaleExpectedSession is the duplicate-/new case: the second
// rotation names a session that is no longer the main, so it must not rotate
// the successor the first one just created.
func TestRotateMain_StaleExpectedSession(t *testing.T) {
	r, s := newTestRegistry(t)
	now := time.Now().UTC()
	s.sessions["main-1"] = NewInfo("main-1", "agent1", "u1", "agent1:user:u1:private", KindMain, "", now)

	successor, err := r.RotateMain(context.Background(), MainRequest{UserID: "u1", AgentID: "agent1", ExpectedSessionID: "main-1"})
	if err != nil {
		t.Fatalf("RotateMain: %v", err)
	}

	_, err = r.RotateMain(context.Background(), MainRequest{UserID: "u1", AgentID: "agent1", ExpectedSessionID: "main-1"})
	if !errors.Is(err, ErrStaleRotation) {
		t.Fatalf("second RotateMain = %v, want ErrStaleRotation", err)
	}
	if s.sessions[successor.ID].Archived {
		t.Error("a stale rotation must leave the current main active")
	}
}

// TestRotateMain_StoreFailureKeepsOldMain proves a failed rotation is a no-op:
// the store transaction rolls back, so the old main is still resolvable.
func TestRotateMain_StoreFailureKeepsOldMain(t *testing.T) {
	r, s := newTestRegistry(t)
	now := time.Now().UTC()
	s.sessions["main-1"] = NewInfo("main-1", "agent1", "u1", "agent1:user:u1:private", KindMain, "", now)
	s.rotateErr = errors.New("create successor failed")

	if _, err := r.RotateMain(context.Background(), MainRequest{UserID: "u1", AgentID: "agent1"}); err == nil {
		t.Fatal("RotateMain must report the store failure")
	}
	if s.sessions["main-1"].Archived {
		t.Fatal("a failed rotation must not archive the old main")
	}
	resolved, err := r.ResolveMain(context.Background(), MainRequest{UserID: "u1", AgentID: "agent1"})
	if err != nil {
		t.Fatalf("ResolveMain after failed rotation: %v", err)
	}
	if resolved.ID != "main-1" {
		t.Errorf("ResolveMain = %q, want main-1", resolved.ID)
	}
}

// TestRotateMain_ConcurrentResolveNeverPromotesStale proves the keyed main lock
// covers the whole rotation: a ResolveMain racing it sees either the old main or
// the successor, never a promoted third session.
func TestRotateMain_ConcurrentResolveNeverPromotesStale(t *testing.T) {
	r, s := newTestRegistry(t)
	now := time.Now().UTC()
	s.sessions["main-1"] = NewInfo("main-1", "agent1", "u1", "agent1:user:u1:private", KindMain, "", now)
	// A promotable chat candidate on the same private channel. If ResolveMain ran
	// while the rotation had archived main-1 but not yet created its successor,
	// this is the session it would wrongly promote.
	s.sessions["chat-1"] = NewInfo("chat-1", "agent1", "u1", "agent1:user:u1:private", KindChat, "", now.Add(-time.Hour))

	resolved := make(chan Info, 1)
	s.beforeRotate = func() {
		go func() {
			info, err := r.ResolveMain(context.Background(), MainRequest{UserID: "u1", AgentID: "agent1"})
			if err != nil {
				t.Errorf("concurrent ResolveMain: %v", err)
			}
			resolved <- info
		}()
		// Give the racing resolve a chance to reach the lock before the rotation
		// finishes; it must block there rather than observe the gap.
		time.Sleep(10 * time.Millisecond)
	}

	successor, err := r.RotateMain(context.Background(), MainRequest{UserID: "u1", AgentID: "agent1"})
	if err != nil {
		t.Fatalf("RotateMain: %v", err)
	}
	select {
	case info := <-resolved:
		if info.ID != successor.ID && info.ID != "main-1" {
			t.Fatalf("concurrent ResolveMain promoted %q; want the old main or the successor", info.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent ResolveMain did not complete")
	}
	if s.sessions["chat-1"].Kind == string(KindMain) {
		t.Error("a stale chat session must never be promoted during rotation")
	}
}
