package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/knowledge"
	"github.com/CherryHQ/stella/internal/server"
)

type knowledgeTestParser struct{}

func (knowledgeTestParser) Parse(context.Context, string, string) ([]knowledge.ParsedChunk, error) {
	return nil, errors.New("parser is not started in HTTP management tests")
}

func setupKnowledgeAPI(t *testing.T) *testEnv {
	t.Helper()
	env := setupAdmin(t)
	service, err := knowledge.NewService(knowledge.ServiceConfig{
		DB:          env.db,
		Parser:      knowledgeTestParser{},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		AgentAccess: env.deps.AgentAccess,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	client, err := river.NewClient(riverpgxv5.New(env.db), &river.Config{})
	if err != nil {
		t.Fatalf("new insert-only River client: %v", err)
	}
	service.SetRiverClient(client)
	env.rebuild(t, func(deps *server.Deps) {
		deps.Knowledge = service
	})
	return env
}

func TestKnowledgeFileManagementLifecycleAndPagination(t *testing.T) {
	env := setupKnowledgeAPI(t)

	first := uploadKnowledgeFile(
		t,
		env,
		env.bearerToken,
		"/api/knowledge-files?scope=system",
		`C:\fakepath\Alpha.txt`,
		[]byte("alpha"),
		nil,
	)
	if first.Code != http.StatusCreated {
		t.Fatalf("first upload status=%d body=%s", first.Code, first.Body.String())
	}
	var created apitypes.KnowledgeFile
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.FileName != "Alpha.txt" || created.Status != apitypes.KnowledgeFileStatusProcessing {
		t.Fatalf("created = %+v", created)
	}
	if strings.Contains(first.Body.String(), "raw_content") || strings.Contains(first.Body.String(), "user_id") {
		t.Fatalf("create response leaked internal data: %s", first.Body.String())
	}

	for _, name := range []string{"Beta.txt", "Gamma.txt"} {
		response := uploadKnowledgeFile(
			t,
			env,
			env.bearerToken,
			"/api/knowledge-files?scope=system",
			name,
			[]byte(strings.ToLower(name)),
			nil,
		)
		if response.Code != http.StatusCreated {
			t.Fatalf("upload %s status=%d body=%s", name, response.Code, response.Body.String())
		}
	}

	pageOne := doRequest(t, env, http.MethodGet, "/api/knowledge-files?scope=system&page_size=2", nil)
	if pageOne.Code != http.StatusOK {
		t.Fatalf("page one status=%d body=%s", pageOne.Code, pageOne.Body.String())
	}
	var list apitypes.KnowledgeFileList
	if err := json.Unmarshal(pageOne.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.KnowledgeFiles) != 2 || list.NextPageToken == nil {
		t.Fatalf("page one = %+v, want 2 files and a token", list)
	}
	if list.Quota.UsedFiles != 3 || list.Quota.MaxFiles != knowledge.SystemMaxFiles {
		t.Fatalf("quota = %+v, want 3/%d files", list.Quota, knowledge.SystemMaxFiles)
	}

	pageTwo := doRequest(
		t,
		env,
		http.MethodGet,
		"/api/knowledge-files?scope=system&page_size=2&page_token="+*list.NextPageToken,
		nil,
	)
	if pageTwo.Code != http.StatusOK {
		t.Fatalf("page two status=%d body=%s", pageTwo.Code, pageTwo.Body.String())
	}
	var secondList apitypes.KnowledgeFileList
	if err := json.Unmarshal(pageTwo.Body.Bytes(), &secondList); err != nil {
		t.Fatal(err)
	}
	if len(secondList.KnowledgeFiles) != 1 || secondList.NextPageToken != nil {
		t.Fatalf("page two = %+v, want one final file", secondList)
	}

	tampered := *list.NextPageToken
	if prefix, ok := strings.CutSuffix(tampered, "A"); ok {
		tampered = prefix + "B"
	} else {
		tampered = tampered[:len(tampered)-1] + "A"
	}
	badToken := doRequest(
		t,
		env,
		http.MethodGet,
		"/api/knowledge-files?scope=system&page_token="+tampered,
		nil,
	)
	if badToken.Code != http.StatusBadRequest {
		t.Fatalf("tampered token status=%d body=%s", badToken.Code, badToken.Body.String())
	}
	crossQuery := doRequest(
		t,
		env,
		http.MethodGet,
		"/api/knowledge-files?scope=system&q=alpha&page_token="+*list.NextPageToken,
		nil,
	)
	if crossQuery.Code != http.StatusBadRequest {
		t.Fatalf("cross-query token status=%d body=%s", crossQuery.Code, crossQuery.Body.String())
	}

	get := doRequest(t, env, http.MethodGet, "/api/knowledge-files/"+created.Id.String(), nil)
	if get.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", get.Code, get.Body.String())
	}
	deleted := doRequest(t, env, http.MethodDelete, "/api/knowledge-files/"+created.Id.String(), nil)
	if deleted.Code != http.StatusNoContent || deleted.Body.Len() != 0 {
		t.Fatalf("delete status=%d body=%q", deleted.Code, deleted.Body.String())
	}
	repeated := doRequest(t, env, http.MethodDelete, "/api/knowledge-files/"+created.Id.String(), nil)
	if repeated.Code != http.StatusNotFound {
		t.Fatalf("repeat delete status=%d body=%s", repeated.Code, repeated.Body.String())
	}
}

func TestKnowledgeAuthorizationPrecedesMultipartRead(t *testing.T) {
	env := setupKnowledgeAPI(t)
	_, userToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "knowledge-user", auth.RoleUser)

	body := &readTrackingBody{}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/knowledge-files?scope=system",
		body,
	)
	request.Header.Set("Content-Type", "multipart/form-data; boundary=unused")
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: userToken})
	response := httptest.NewRecorder()
	env.srv.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if body.reads != 0 {
		t.Fatalf("unauthorized upload body was read %d times", body.reads)
	}
}

func TestKnowledgeManagementHidesSystemFileFromRegularUser(t *testing.T) {
	env := setupKnowledgeAPI(t)
	_, userToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "knowledge-reader", auth.RoleUser)

	createdResponse := uploadKnowledgeFile(
		t,
		env,
		env.bearerToken,
		"/api/knowledge-files?scope=system",
		"system.txt",
		[]byte("system"),
		nil,
	)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createdResponse.Code, createdResponse.Body.String())
	}
	var created apitypes.KnowledgeFile
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	get := doRequestWithSession(
		t,
		env.srv,
		userToken,
		http.MethodGet,
		"/api/knowledge-files/"+created.Id.String(),
		nil,
	)
	if get.Code != http.StatusNotFound {
		t.Fatalf("regular-user get status=%d body=%s", get.Code, get.Body.String())
	}
	deleteResponse := doRequestWithSession(
		t,
		env.srv,
		userToken,
		http.MethodDelete,
		"/api/knowledge-files/"+created.Id.String(),
		nil,
	)
	if deleteResponse.Code != http.StatusNotFound {
		t.Fatalf("regular-user delete status=%d body=%s", deleteResponse.Code, deleteResponse.Body.String())
	}
}

func TestKnowledgeManagementFourScopeMatrix(t *testing.T) {
	env := setupKnowledgeAPI(t)
	agentID := findStellaID(t, env)
	_, userToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "knowledge-owner", auth.RoleUser)
	_, otherUserToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "knowledge-other", auth.RoleUser)

	userFileResponse := uploadKnowledgeFile(
		t,
		env,
		userToken,
		"/api/knowledge-files?scope=user",
		"personal.txt",
		[]byte("personal"),
		nil,
	)
	if userFileResponse.Code != http.StatusCreated {
		t.Fatalf("user upload status=%d body=%s", userFileResponse.Code, userFileResponse.Body.String())
	}
	var userFile apitypes.KnowledgeFile
	if err := json.Unmarshal(userFileResponse.Body.Bytes(), &userFile); err != nil {
		t.Fatal(err)
	}

	userAgentResponse := uploadKnowledgeFile(
		t,
		env,
		userToken,
		"/api/knowledge-files?scope=user_agent&agent_id="+agentID,
		"personal-agent.txt",
		[]byte("personal agent"),
		nil,
	)
	if userAgentResponse.Code != http.StatusCreated {
		t.Fatalf("user_agent upload status=%d body=%s", userAgentResponse.Code, userAgentResponse.Body.String())
	}

	for _, path := range []string{
		"/api/knowledge-files?scope=system",
		"/api/knowledge-files?scope=system_agent&agent_id=" + agentID,
	} {
		response := uploadKnowledgeFile(t, env, userToken, path, "forbidden.txt", []byte("forbidden"), nil)
		if response.Code != http.StatusForbidden {
			t.Fatalf("regular user POST %s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}

	systemAgentResponse := uploadKnowledgeFile(
		t,
		env,
		env.bearerToken,
		"/api/knowledge-files?scope=system_agent&agent_id="+agentID,
		"shared-agent.txt",
		[]byte("shared agent"),
		nil,
	)
	if systemAgentResponse.Code != http.StatusCreated {
		t.Fatalf(
			"admin system_agent upload status=%d body=%s",
			systemAgentResponse.Code,
			systemAgentResponse.Body.String(),
		)
	}

	// A personal file remains invisible even when another authenticated user
	// knows its UUID; management authority always comes from the session.
	otherUserGet := doRequestWithSession(
		t,
		env.srv,
		otherUserToken,
		http.MethodGet,
		"/api/knowledge-files/"+userFile.Id.String(),
		nil,
	)
	if otherUserGet.Code != http.StatusNotFound {
		t.Fatalf("other-user get status=%d body=%s", otherUserGet.Code, otherUserGet.Body.String())
	}
}

func TestKnowledgeUploadRejectsExtraMultipartFields(t *testing.T) {
	env := setupKnowledgeAPI(t)
	extra := func(writer *multipart.Writer) {
		if err := writer.WriteField("scope", "user"); err != nil {
			t.Fatal(err)
		}
	}
	response := uploadKnowledgeFile(
		t,
		env,
		env.bearerToken,
		"/api/knowledge-files?scope=system",
		"one.txt",
		[]byte("one"),
		extra,
	)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"reason":"invalid_multipart"`) {
		t.Fatalf("missing invalid_multipart reason: %s", response.Body.String())
	}
}

type readTrackingBody struct {
	reads int
}

func (b *readTrackingBody) Read([]byte) (int, error) {
	b.reads++
	return 0, errors.New("body must not be read")
}

func (b *readTrackingBody) Close() error { return nil }

func uploadKnowledgeFile(
	t *testing.T,
	env *testEnv,
	sessionToken string,
	path string,
	fileName string,
	content []byte,
	extra func(*multipart.Writer),
) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if extra != nil {
		extra(writer)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, path, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionToken})
	response := httptest.NewRecorder()
	env.srv.Handler().ServeHTTP(response, request)
	return response
}

var _ io.ReadCloser = (*readTrackingBody)(nil)
