package server_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/library"
	"github.com/CherryHQ/stella/internal/server"
)

type libraryFileAPIItem struct {
	ID           string  `json:"id"`
	Scope        string  `json:"scope"`
	AgentID      *string `json:"agent_id"`
	FileName     string  `json:"file_name"`
	MediaType    string  `json:"media_type"`
	SizeBytes    int64   `json:"size_bytes"`
	Status       string  `json:"status"`
	ErrorMessage *string `json:"error_message"`
}

type libraryFileAPIList struct {
	LibraryFiles  []libraryFileAPIItem `json:"library_files"`
	NextPageToken *string              `json:"next_page_token"`
	Quota         struct {
		UsedFiles int64 `json:"used_files"`
		MaxFiles  int64 `json:"max_files"`
		UsedBytes int64 `json:"used_bytes"`
		MaxBytes  int64 `json:"max_bytes"`
	} `json:"quota"`
}

type serverLibraryParser struct{}

func (serverLibraryParser) Parse(context.Context, string, string) ([]library.ParsedChunk, error) {
	return []library.ParsedChunk{{Content: "server test library"}}, nil
}

func attachLibraryService(t *testing.T, env *testEnv) *library.Service {
	t.Helper()
	rawStore, err := library.NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("library.NewFSRawStore: %v", err)
	}
	return attachLibraryServiceWithRawStore(t, env, rawStore)
}

func attachLibraryServiceWithRawStore(
	t *testing.T,
	env *testEnv,
	rawStore library.RawStore,
) *library.Service {
	t.Helper()
	riverClient, err := river.NewClient(riverpgxv5.New(env.db), &river.Config{})
	if err != nil {
		t.Fatalf("river.NewClient: %v", err)
	}
	service, err := library.NewService(library.ServiceConfig{
		DB: env.db, RawStore: rawStore, Parser: serverLibraryParser{},
		ParserProfile: library.TextParserProfile, River: riverClient,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), TempDir: t.TempDir(),
		MaxConcurrentUploads: 4, MaxSpoolBytes: 4 * library.MaxFileBytes,
		AgentAccess: env.deps.AgentAccess,
	})
	if err != nil {
		t.Fatalf("library.NewService: %v", err)
	}
	env.rebuild(t, func(deps *server.Deps) { deps.Library = service })
	return service
}

func uploadLibraryFile(t *testing.T, env *testEnv, token, scope, agentID, name, content string) libraryFileAPIItem {
	t.Helper()
	path := "/api/library-files?scope=" + url.QueryEscape(scope)
	if agentID != "" {
		path += "&agent_id=" + url.QueryEscape(agentID)
	}
	rr := doMultipartRequestWithSession(
		t, env.srv.Handler(), token, http.MethodPost, path, "file", name, []byte(content),
	)
	if rr.Code != http.StatusCreated {
		t.Fatalf("upload %s/%s status = %d, want 201 (body: %s)", scope, name, rr.Code, rr.Body.String())
	}
	var item libraryFileAPIItem
	if err := json.Unmarshal(parseResponse(t, rr).Data, &item); err != nil {
		t.Fatalf("decode library upload: %v", err)
	}
	return item
}

func listLibraryFiles(t *testing.T, env *testEnv, token, query string) libraryFileAPIList {
	t.Helper()
	rr := doRequestWithSession(t, env.srv, token, http.MethodGet, "/api/library-files?"+query, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list library files status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	var list libraryFileAPIList
	if err := json.Unmarshal(parseResponse(t, rr).Data, &list); err != nil {
		t.Fatalf("decode library list: %v", err)
	}
	return list
}

func setLibraryTestAuth(req *http.Request, token string) {
	if strings.HasPrefix(token, "stella_") {
		req.Header.Set("Authorization", "Bearer "+token)
		return
	}
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
}

func TestLibraryFileManagementScopesQuotaSearchAndLifecycle(t *testing.T) {
	env := setupAdmin(t)
	_, userToken := newNonAdmin(t, env, "library-files-user")
	agentID := createAgentAsUser(t, env, userToken, "Library Files Agent")
	attachLibraryService(t, env)

	userFile := uploadLibraryFile(t, env, userToken, "user", "", "company-notes.md", "all agents")
	agentFile := uploadLibraryFile(t, env, userToken, "user_agent", agentID, "agent-notes.txt", "one agent")
	if userFile.Status != "processing" || agentFile.Status != "processing" {
		t.Fatalf("new upload statuses = %q/%q, want processing", userFile.Status, agentFile.Status)
	}

	userList := listLibraryFiles(t, env, userToken, "scope=user&q=COMPANY")
	if len(userList.LibraryFiles) != 1 || userList.LibraryFiles[0].ID != userFile.ID {
		t.Fatalf("user filename search = %+v, want %s", userList.LibraryFiles, userFile.ID)
	}
	if userList.Quota.UsedFiles != 2 || userList.Quota.MaxFiles != library.PersonalMaxFiles || userList.Quota.UsedBytes != int64(len("all agents")+len("one agent")) {
		t.Fatalf("personal shared quota = %+v, want 2 files across user + user_agent", userList.Quota)
	}
	agentList := listLibraryFiles(t, env, userToken, "scope=user_agent&agent_id="+url.QueryEscape(agentID))
	if len(agentList.LibraryFiles) != 1 || agentList.LibraryFiles[0].ID != agentFile.ID || agentList.Quota.UsedFiles != 2 {
		t.Fatalf("user_agent list/quota = %+v, want exact Agent item and shared quota", agentList)
	}

	// A browser refresh observes the durable worker state without a polling API.
	if _, err := env.db.Exec(t.Context(), `UPDATE library_file SET status = 'failed', error_message = 'parse failed', updated_at = now() WHERE id = $1`, agentFile.ID); err != nil {
		t.Fatalf("mark test file failed: %v", err)
	}
	rr := doRequestWithSession(t, env.srv, userToken, http.MethodGet, "/api/library-files/"+agentFile.ID, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get failed library file status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	var refreshed libraryFileAPIItem
	if err := json.Unmarshal(parseResponse(t, rr).Data, &refreshed); err != nil {
		t.Fatal(err)
	}
	if refreshed.Status != "failed" || refreshed.ErrorMessage == nil || *refreshed.ErrorMessage != "parse failed" {
		t.Fatalf("refreshed file = %+v, want failed state and message", refreshed)
	}

	rr = doRequestWithSession(t, env.srv, userToken, http.MethodDelete, "/api/library-files/"+userFile.ID, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete user library status = %d, want 204 (body: %s)", rr.Code, rr.Body.String())
	}
	userList = listLibraryFiles(t, env, userToken, "scope=user")
	if len(userList.LibraryFiles) != 0 || userList.Quota.UsedFiles != 1 {
		t.Fatalf("post-delete list/quota = %+v, want immediate visibility and quota release", userList)
	}
	rr = doRequestWithSession(t, env.srv, userToken, http.MethodGet, "/api/library-files/"+userFile.ID, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("get tombstoned file status = %d, want 404", rr.Code)
	}
}

func TestLibraryFileManagementAdminScopesAndOpaqueOwnership(t *testing.T) {
	env := setupAdmin(t)
	_, userToken := newNonAdmin(t, env, "library-files-authz")
	agentID := createAgentAsUser(t, env, userToken, "Library Admin Scope Agent")
	attachLibraryService(t, env)

	systemFile := uploadLibraryFile(t, env, env.bearerToken, "system", "", "system.md", "system")
	systemAgentFile := uploadLibraryFile(t, env, env.bearerToken, "system_agent", agentID, "system-agent.md", "agent")
	if got := listLibraryFiles(t, env, env.bearerToken, "scope=system"); len(got.LibraryFiles) != 1 || got.LibraryFiles[0].ID != systemFile.ID || got.Quota.MaxFiles != library.SystemMaxFiles {
		t.Fatalf("system list = %+v", got)
	}
	if got := listLibraryFiles(t, env, env.bearerToken, "scope=system_agent&agent_id="+url.QueryEscape(agentID)); len(got.LibraryFiles) != 1 || got.LibraryFiles[0].ID != systemAgentFile.ID || got.Quota.MaxFiles != library.SystemAgentMaxFiles {
		t.Fatalf("system_agent list = %+v", got)
	}

	for _, path := range []string{
		"/api/library-files?scope=system",
		"/api/library-files?scope=system_agent&agent_id=" + url.QueryEscape(agentID),
	} {
		rr := doRequestWithSession(t, env.srv, userToken, http.MethodGet, path, nil)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("ordinary user GET %s status = %d, want 403", path, rr.Code)
		}
	}
	for _, id := range []string{systemFile.ID, systemAgentFile.ID} {
		rr := doRequestWithSession(t, env.srv, userToken, http.MethodGet, "/api/library-files/"+id, nil)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("foreign file %s status = %d, want opaque 404", id, rr.Code)
		}
	}
}

func TestLibraryFilePageTokenIsBoundToScopeAgentAndQuery(t *testing.T) {
	env := setupAdmin(t)
	_, userToken := newNonAdmin(t, env, "library-files-page")
	_, otherUserToken := newNonAdmin(t, env, "library-files-page-other")
	agentID := createAgentAsUser(t, env, userToken, "Library Page Agent")
	attachLibraryService(t, env)
	uploadLibraryFile(t, env, userToken, "user", "", "first.md", "first")
	uploadLibraryFile(t, env, userToken, "user", "", "second.md", "second")

	first := listLibraryFiles(t, env, userToken, "scope=user&page_size=1")
	if len(first.LibraryFiles) != 1 || first.NextPageToken == nil || *first.NextPageToken == "" {
		t.Fatalf("first page = %+v, want one item and next token", first)
	}
	secondPath := "/api/library-files?scope=user&page_size=1&page_token=" + url.QueryEscape(*first.NextPageToken)
	secondResponse := doRequestWithSession(t, env.srv, userToken, http.MethodGet, secondPath, nil)
	if secondResponse.Code != http.StatusOK {
		t.Fatalf("second page status = %d, want 200 (body: %s)", secondResponse.Code, secondResponse.Body.String())
	}
	secondData := parseResponse(t, secondResponse).Data
	var secondEnvelope map[string]json.RawMessage
	if err := json.Unmarshal(secondData, &secondEnvelope); err != nil {
		t.Fatalf("decode second page envelope: %v", err)
	}
	nextPageTokenJSON, ok := secondEnvelope["next_page_token"]
	if !ok || string(nextPageTokenJSON) != "null" {
		t.Fatalf("terminal next_page_token = %s (present: %t), want explicit null", nextPageTokenJSON, ok)
	}
	var second libraryFileAPIList
	if err := json.Unmarshal(secondData, &second); err != nil {
		t.Fatalf("decode second page: %v", err)
	}
	if len(second.LibraryFiles) != 1 || second.NextPageToken != nil || second.LibraryFiles[0].ID == first.LibraryFiles[0].ID {
		t.Fatalf("second page = %+v, want distinct final item", second)
	}

	for _, query := range []string{
		"scope=user&q=other&page_token=" + url.QueryEscape(*first.NextPageToken),
		"scope=user_agent&agent_id=" + url.QueryEscape(agentID) + "&page_token=" + url.QueryEscape(*first.NextPageToken),
		"scope=user&page_token=" + url.QueryEscape(mutateOpaqueToken(t, *first.NextPageToken, "id", "not-a-uuid")),
		"scope=user&page_token=malformed",
	} {
		rr := doRequestWithSession(t, env.srv, userToken, http.MethodGet, "/api/library-files?"+query, nil)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("token query %q status = %d, want 400 (body: %s)", query, rr.Code, rr.Body.String())
		}
	}
	rr := doRequestWithSession(
		t, env.srv, otherUserToken, http.MethodGet,
		"/api/library-files?scope=user&page_token="+url.QueryEscape(*first.NextPageToken), nil,
	)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("cross-user page token status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
}

type countedBody struct {
	remaining int64
	reads     int
}

func (b *countedBody) Read(p []byte) (int, error) {
	b.reads++
	if b.remaining == 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), b.remaining)
	for i := range p[:n] {
		p[i] = 'x'
	}
	b.remaining -= n
	return int(n), nil
}

func TestLibraryFileUnauthorizedUploadIsRejectedBeforeBodyRead(t *testing.T) {
	env := setupAdmin(t)
	_, userToken := newNonAdmin(t, env, "library-files-stream-guard")
	attachLibraryService(t, env)
	body := &countedBody{remaining: 100 << 20}
	req := httptest.NewRequest(http.MethodPost, "/api/library-files?scope=system", body)
	setLibraryTestAuth(req, userToken)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=library-test")
	rr := httptest.NewRecorder()
	env.srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("unauthorized upload status = %d, want 403 (body: %s)", rr.Code, rr.Body.String())
	}
	if body.reads != 0 {
		t.Fatalf("unauthorized upload consumed request body %d times", body.reads)
	}
}

func TestLibraryFileUploadQuotaErrorIsStructured(t *testing.T) {
	env := setupAdmin(t)
	user, userToken := newNonAdmin(t, env, "library-files-quota")
	attachLibraryService(t, env)
	if _, err := env.db.Exec(t.Context(), `
		INSERT INTO library_file (
			id, scope, user_id, file_name, media_type, size_bytes, raw_sha256, status
		) VALUES ($1, 'user', $2, 'quota-fixture.txt', 'text/plain', $3, $4, 'ready')
	`, uuid.NewString(), user.ID, library.PersonalMaxBytes, bytes.Repeat([]byte{1}, sha256.Size)); err != nil {
		t.Fatalf("seed full personal quota: %v", err)
	}

	rr := doMultipartRequestWithSession(
		t, env.srv.Handler(), userToken, http.MethodPost,
		"/api/library-files?scope=user", "file", "over-quota.txt", []byte("x"),
	)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("quota upload status = %d, want 429 (body: %s)", rr.Code, rr.Body.String())
	}
	var body struct {
		Error struct {
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode quota error: %v", err)
	}
	if body.Error.Details["code"] != "quota_exceeded" || body.Error.Details["max_bytes"] != float64(library.PersonalMaxBytes) {
		t.Fatalf("quota error details = %v", body.Error.Details)
	}
}

func TestLibraryFileUploadValidationAndUnavailableCapability(t *testing.T) {
	env := setupAdmin(t)
	rr := doRequest(t, env, http.MethodGet, "/api/library-files?scope=user", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired library status = %d, want 503", rr.Code)
	}
	attachLibraryService(t, env)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		var response *httptest.ResponseRecorder
		if method == http.MethodGet {
			response = doRequestWithSession(
				t, env.srv, env.bearerToken, method,
				"/api/library-files?scope=user&agent_id=", nil,
			)
		} else {
			response = doMultipartRequestWithSession(
				t, env.srv.Handler(), env.bearerToken, method,
				"/api/library-files?scope=user&agent_id=", "file", "empty-agent.txt", []byte("value"),
			)
		}
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s with empty agent_id status = %d, want 400 (body: %s)", method, response.Code, response.Body.String())
		}
	}

	for name, wantStatus := range map[string]int{
		"unsupported.csv": http.StatusBadRequest,
		"empty.txt":       http.StatusBadRequest,
	} {
		content := "value"
		if strings.HasPrefix(name, "empty") {
			content = ""
		}
		rr = doMultipartRequestWithSession(
			t, env.srv.Handler(), env.bearerToken, http.MethodPost,
			"/api/library-files?scope=user", "file", name, []byte(content),
		)
		if rr.Code != wantStatus {
			t.Fatalf("upload %s status = %d, want %d (body: %s)", name, rr.Code, wantStatus, rr.Body.String())
		}
	}

	// Exercise the complete HTTP multipart path so the transport limit and the
	// domain file limit cannot accidentally turn an oversized upload into 500.
	rr = doMultipartRequestWithSession(
		t, env.srv.Handler(), env.bearerToken, http.MethodPost,
		"/api/library-files?scope=user", "file", "too-large.txt",
		bytes.Repeat([]byte{'x'}, library.MaxFileBytes+1),
	)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload status = %d, want 413 (body: %s)", rr.Code, rr.Body.String())
	}

	// A multipart preamble is transport framing rather than file content. It can
	// therefore exceed the request limit before the domain file limit is reached.
	var expandedBody bytes.Buffer
	expandedBody.Write(bytes.Repeat(
		[]byte("preamble\r\n"),
		int((library.MaxFileBytes+(1<<20))/int64(len("preamble\r\n")))+1,
	))
	expandedWriter := multipart.NewWriter(&expandedBody)
	expandedPart, err := expandedWriter.CreateFormFile("file", "expanded.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := expandedPart.Write([]byte("value")); err != nil {
		t.Fatal(err)
	}
	if err := expandedWriter.Close(); err != nil {
		t.Fatal(err)
	}
	expandedReq := httptest.NewRequest(http.MethodPost, "/api/library-files?scope=user", &expandedBody)
	setLibraryTestAuth(expandedReq, env.bearerToken)
	expandedReq.Header.Set("Content-Type", expandedWriter.FormDataContentType())
	rr = httptest.NewRecorder()
	env.srv.Handler().ServeHTTP(rr, expandedReq)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("multipart transport limit status = %d, want 413 (body: %s)", rr.Code, rr.Body.String())
	}

	// An interrupted multipart file must be reported as invalid input rather
	// than as a server failure.
	var truncatedBody bytes.Buffer
	truncatedWriter := multipart.NewWriter(&truncatedBody)
	truncatedPart, err := truncatedWriter.CreateFormFile("file", "truncated.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := truncatedPart.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	truncatedReq := httptest.NewRequest(http.MethodPost, "/api/library-files?scope=user", &truncatedBody)
	setLibraryTestAuth(truncatedReq, env.bearerToken)
	truncatedReq.Header.Set("Content-Type", truncatedWriter.FormDataContentType())
	rr = httptest.NewRecorder()
	env.srv.Handler().ServeHTTP(rr, truncatedReq)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("truncated upload status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}

	// The API accepts exactly the documented file part; scope and Agent stay in
	// trusted query parameters so authorization never depends on multipart order.
	var body strings.Builder
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("scope", "system"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/library-files?scope=user", strings.NewReader(body.String()))
	setLibraryTestAuth(req, env.bearerToken)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rr = httptest.NewRecorder()
	env.srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing file part status = %d, want 400", rr.Code)
	}
}

func TestLibraryFileUploadRawStorageDegradedIsUnavailable(t *testing.T) {
	env := setupAdmin(t)
	rawStore, err := library.NewFSRawStore(t.TempDir(), math.MaxInt64)
	if err != nil {
		t.Fatalf("library.NewFSRawStore: %v", err)
	}
	attachLibraryServiceWithRawStore(t, env, rawStore)

	rr := doMultipartRequestWithSession(
		t, env.srv.Handler(), env.bearerToken, http.MethodPost,
		"/api/library-files?scope=user", "file", "degraded.txt", []byte("value"),
	)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("degraded RawStore status = %d, want 503 (body: %s)", rr.Code, rr.Body.String())
	}
}
