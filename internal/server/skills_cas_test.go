package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/skills"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

const managedDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type recordingSkillStore struct {
	update skills.ManagedSkillUpdate
	delete skills.ManagedSkillDelete
	file   skills.ManagedSkillFileDelete
	err    error
}

func (s *recordingSkillStore) Get(context.Context, string) (*skills.Skill, error) {
	return nil, errors.New("unused")
}

func (s *recordingSkillStore) List(context.Context, skills.ViewContext) ([]skills.Skill, error) {
	return nil, errors.New("unused")
}

func (s *recordingSkillStore) ListAll(context.Context) ([]skills.Skill, error) {
	return nil, errors.New("unused")
}

func (s *recordingSkillStore) ListActiveReflectOwnedUserAgentSkills(context.Context, string, string) ([]skills.Skill, error) {
	return nil, errors.New("unused")
}

func (s *recordingSkillStore) CreateManagedSkill(context.Context, skills.Skill, map[string]string) (skills.SkillSnapshot, error) {
	return skills.SkillSnapshot{}, s.err
}

func (s *recordingSkillStore) UpdateManagedSkill(_ context.Context, in skills.ManagedSkillUpdate) (skills.SkillSnapshot, error) {
	s.update = in
	return skills.SkillSnapshot{}, s.err
}

func (s *recordingSkillStore) DeleteManagedSkill(_ context.Context, in skills.ManagedSkillDelete) error {
	s.delete = in
	return s.err
}

func (s *recordingSkillStore) DeleteManagedSkillFile(_ context.Context, in skills.ManagedSkillFileDelete) (skills.SkillSnapshot, error) {
	s.file = in
	return skills.SkillSnapshot{}, s.err
}

func (s *recordingSkillStore) CreateReflectOwnedUserAgentSkill(context.Context, skills.ReflectSkillCreate) (skills.Skill, error) {
	return skills.Skill{}, errors.New("unused")
}

func (s *recordingSkillStore) PatchReflectOwnedUserAgentSkill(context.Context, skills.ReflectSkillPatch) (skills.Skill, error) {
	return skills.Skill{}, errors.New("unused")
}

func (s *recordingSkillStore) DeleteReflectOwnedUserAgentSkill(context.Context, skills.ReflectSkillDelete) (skills.Skill, error) {
	return skills.Skill{}, errors.New("unused")
}

func (s *recordingSkillStore) TouchReflectSkillRuntimeUse(context.Context, string, string, string, string) error {
	return errors.New("unused")
}

func (s *recordingSkillStore) ListForAgentContext(context.Context, string, string) ([]skills.Skill, error) {
	return nil, errors.New("unused")
}

func (s *recordingSkillStore) ListByScope(context.Context, string, string, string) ([]skills.Skill, error) {
	return nil, errors.New("unused")
}

func (s *recordingSkillStore) ListForAdmin(context.Context, string) ([]skills.Skill, error) {
	return nil, errors.New("unused")
}

func (s *recordingSkillStore) ListForUser(context.Context, string, []string) ([]skills.Skill, error) {
	return nil, errors.New("unused")
}

func (s *recordingSkillStore) Resolve(context.Context, string, skills.ViewContext) (*skills.Skill, error) {
	return nil, errors.New("unused")
}

func (s *recordingSkillStore) LoadFile(context.Context, string, string) (string, error) {
	return "", errors.New("unused")
}

func (s *recordingSkillStore) ListFiles(context.Context, string) ([]string, error) {
	return nil, errors.New("unused")
}

func (s *recordingSkillStore) ListFilesWithContent(context.Context, string) (map[string]string, error) {
	return nil, errors.New("unused")
}

func casTestServer(store skills.Store) *Server {
	host := pluginhost.New(nil)
	host.SetSkillStore(store)
	return &Server{pluginHost: host, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func managedTestSkill() *skills.Skill {
	return &skills.Skill{ID: "home:user:u:skill", Scope: "user", UserID: "u", Name: "skill", ContentDigest: managedDigest}
}

func TestManagedSkillHandlersPassExactClientDigest(t *testing.T) {
	store := &recordingSkillStore{}
	server := casTestServer(store)

	request := httptest.NewRequest(http.MethodPatch, "/api/skills/skill", strings.NewReader(`{"expected_digest":"`+managedDigest+`"}`))
	response := httptest.NewRecorder()
	server.applySkillUpdate(response, request, managedTestSkill())
	if response.Code != http.StatusOK || store.update.ExpectedDigest != managedDigest {
		t.Fatalf("update status/digest = %d/%q", response.Code, store.update.ExpectedDigest)
	}

	response = httptest.NewRecorder()
	server.doDeleteSkill(response, httptest.NewRequest(http.MethodDelete, "/api/skills/skill", nil), managedTestSkill(), managedDigest)
	if response.Code != http.StatusNoContent || store.delete.ExpectedDigest != managedDigest {
		t.Fatalf("delete status/digest = %d/%q", response.Code, store.delete.ExpectedDigest)
	}

	response = httptest.NewRecorder()
	server.doDeleteSkillFile(response, httptest.NewRequest(http.MethodDelete, "/api/skills/skill/file", nil), managedTestSkill(), "note.txt", managedDigest)
	if response.Code != http.StatusNoContent || store.file.ExpectedDigest != managedDigest {
		t.Fatalf("file delete status/digest = %d/%q", response.Code, store.file.ExpectedDigest)
	}
}

func TestManagedSkillDigestValidationAndConflictStatus(t *testing.T) {
	for _, digest := range []string{"", "uppercase", strings.Repeat("a", 63)} {
		store := &recordingSkillStore{}
		response := httptest.NewRecorder()
		casTestServer(store).applySkillUpdate(response, httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(`{"expected_digest":"`+digest+`"}`)), managedTestSkill())
		if response.Code != http.StatusBadRequest || store.update.ExpectedDigest != "" {
			t.Fatalf("digest %q: status/digest = %d/%q, want 400/no store call", digest, response.Code, store.update.ExpectedDigest)
		}
	}
	if validManagedSkillDigest("") || validManagedSkillDigest("A"+managedDigest[1:]) || !validManagedSkillDigest(managedDigest) {
		t.Fatal("managed digest validator accepted malformed input")
	}

	for _, err := range []error{skills.ErrHomeSkillConflict, sandbox.ErrManagedSkillConflict} {
		store := &recordingSkillStore{err: err}
		response := httptest.NewRecorder()
		casTestServer(store).applySkillUpdate(response, httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(`{"expected_digest":"`+managedDigest+`"}`)), managedTestSkill())
		if response.Code != http.StatusConflict {
			t.Fatalf("conflict %v: status = %d, want 409", err, response.Code)
		}
	}
	response := httptest.NewRecorder()
	casTestServer(&recordingSkillStore{err: sandbox.ErrOutcomeUnknown}).applySkillUpdate(response, httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(`{"expected_digest":"`+managedDigest+`"}`)), managedTestSkill())
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("outcome-unknown status = %d, want 500", response.Code)
	}
}
