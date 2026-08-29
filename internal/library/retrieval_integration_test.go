package library

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const testAgentB = "library-agent-b"

func TestSearchReturnsExactFourScopeUnionAndOnlyActiveReadyChunks(t *testing.T) {
	database := dbtest.New(t)
	seedLibraryPrincipals(t, database)
	seedRetrievalAgent(t, database, testAgentB)
	_, service := newLibraryService(t, database)

	visible := []struct {
		name  string
		owner Owner
	}{
		{"system.txt", Owner{Scope: ScopeSystem}},
		{"system-agent.txt", Owner{Scope: ScopeSystemAgent, AgentID: testAgentA}},
		{"user.txt", Owner{Scope: ScopeUser, UserID: testUserA}},
		{"user-agent.txt", Owner{Scope: ScopeUserAgent, UserID: testUserA, AgentID: testAgentA}},
	}
	for _, fixture := range visible {
		insertRetrievalFixture(t, database, fixture.name, fixture.owner, "ready", "ready", true, nil)
	}

	// These rows match the same lexical query but are outside the exact owner
	// union for (testUserA, testAgentA).
	for _, fixture := range []struct {
		name  string
		owner Owner
	}{
		{"foreign-system-agent.txt", Owner{Scope: ScopeSystemAgent, AgentID: testAgentB}},
		{"foreign-user.txt", Owner{Scope: ScopeUser, UserID: testUserB}},
		{"foreign-user-agent-owner.txt", Owner{Scope: ScopeUserAgent, UserID: testUserB, AgentID: testAgentA}},
		{"foreign-user-agent-executor.txt", Owner{Scope: ScopeUserAgent, UserID: testUserA, AgentID: testAgentB}},
	} {
		insertRetrievalFixture(t, database, fixture.name, fixture.owner, "ready", "ready", true, nil)
	}

	now := time.Now().UTC()
	insertRetrievalFixture(t, database, "processing.txt", Owner{Scope: ScopeSystem}, "processing", "ready", true, nil)
	insertRetrievalFixture(t, database, "failed.txt", Owner{Scope: ScopeSystem}, "failed", "ready", true, nil)
	insertRetrievalFixture(t, database, "building-set.txt", Owner{Scope: ScopeSystem}, "ready", "building", true, nil)
	insertRetrievalFixture(t, database, "inactive-set.txt", Owner{Scope: ScopeSystem}, "ready", "ready", false, nil)
	insertRetrievalFixture(t, database, "tombstoned.txt", Owner{Scope: ScopeSystem}, "ready", "ready", true, &now)

	authority, err := authz.NewAgentAuthority(authz.UserID(testUserA), authz.AgentID(testAgentA))
	if err != nil {
		t.Fatal(err)
	}
	hits, err := service.Search(t.Context(), authority, "retrievalmarker", MaxSearchLimit)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	got := make(map[string]bool, len(hits))
	for _, hit := range hits {
		got[hit.FileName] = true
		if hit.Content == "" {
			t.Fatalf("empty content for %q", hit.FileName)
		}
	}
	if len(got) != len(visible) {
		t.Fatalf("visible files = %v, want exactly %d", got, len(visible))
	}
	for _, fixture := range visible {
		if !got[fixture.name] {
			t.Errorf("missing visible file %q from %v", fixture.name, got)
		}
	}
}

func TestLibrarySearchToolReturnsOnlySafeEvidenceFields(t *testing.T) {
	database := dbtest.New(t)
	seedLibraryPrincipals(t, database)
	_, service := newLibraryService(t, database)
	insertRetrievalFixture(t, database, "travel-policy.txt", Owner{Scope: ScopeSystem}, "ready", "ready", true, nil)
	if _, err := database.Exec(t.Context(), `
		UPDATE library_chunk AS chunk
		SET locator = '{
			"first_page": 3,
			"last_page": 4,
			"heading_path": ["Travel", "Hotels"],
			"byte_start": 80,
			"byte_end": 120
		}'::jsonb
		FROM library_chunk_set AS chunk_set
		JOIN library_file AS file ON file.id = chunk_set.file_id
		WHERE chunk.chunk_set_id = chunk_set.id
		  AND file.file_name = 'travel-policy.txt'
	`); err != nil {
		t.Fatal(err)
	}

	tool := newSearchTool(service)
	ctx := authz.WithAgentID(authz.WithUserID(t.Context(), testUserA), testAgentA)
	output, err := tool.Execute(ctx, map[string]any{"query": "retrievalmarker", "limit": 1})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var response struct {
		Results []SearchHit `json:"results"`
	}
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		t.Fatalf("decode tool output: %v", err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("results = %+v, want one", response.Results)
	}
	hit := response.Results[0]
	if hit.FileName != "travel-policy.txt" || hit.Locator == nil ||
		hit.Locator.FirstPage == nil || *hit.Locator.FirstPage != 3 ||
		len(hit.Locator.HeadingPath) != 2 {
		t.Fatalf("safe evidence = %+v", hit)
	}
	for _, forbidden := range []string{"byte_start", "byte_end", "chunk_set", "scope", "score", "raw_sha256"} {
		if strings.Contains(output, forbidden) {
			t.Errorf("tool output exposed %q: %s", forbidden, output)
		}
	}

	if _, err := tool.Execute(ctx, map[string]any{"query": "retrievalmarker", "scope": "system"}); !errors.Is(err, ErrInvalidSearch) {
		t.Fatalf("spoofed scope error = %v, want ErrInvalidSearch", err)
	}
}

func TestSearchDegradesMalformedLocatorToFilenameOnlyCitation(t *testing.T) {
	database := dbtest.New(t)
	seedLibraryPrincipals(t, database)
	_, service := newLibraryService(t, database)
	insertRetrievalFixture(t, database, "bad-locator.txt", Owner{Scope: ScopeSystem}, "ready", "ready", true, nil)
	if _, err := database.Exec(t.Context(), `
		UPDATE library_chunk AS chunk
		SET locator = '{"first_page": 4, "last_page": 2}'::jsonb
		FROM library_chunk_set AS chunk_set
		JOIN library_file AS file ON file.id = chunk_set.file_id
		WHERE chunk.chunk_set_id = chunk_set.id
		  AND file.file_name = 'bad-locator.txt'
	`); err != nil {
		t.Fatal(err)
	}

	authority, err := authz.NewAgentAuthority(authz.UserID(testUserA), authz.AgentID(testAgentA))
	if err != nil {
		t.Fatal(err)
	}
	hits, err := service.Search(t.Context(), authority, "retrievalmarker", 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].FileName != "bad-locator.txt" || hits[0].Locator != nil || hits[0].Content == "" {
		t.Fatalf("hits = %+v, want content with a filename-only citation", hits)
	}
}

func seedRetrievalAgent(t *testing.T, database *pgxpool.Pool, id string) {
	t.Helper()
	if _, err := sqlc.New(database).CreateAgent(t.Context(), sqlc.CreateAgentParams{
		ID: id, Name: id, Model: "test/model", Workspace: "/tmp/" + id,
		Sandbox: json.RawMessage(`{}`), Scope: string(config.AgentScopeSystem), Enabled: true,
	}); err != nil {
		t.Fatalf("seed retrieval Agent: %v", err)
	}
}

func insertRetrievalFixture(
	t *testing.T,
	database *pgxpool.Pool,
	fileName string,
	owner Owner,
	fileStatus string,
	setStatus string,
	active bool,
	deletedAt *time.Time,
) {
	t.Helper()
	fileID := uuid.NewString()
	setID := uuid.NewString()
	chunkID := uuid.NewString()
	var userID, agentID any
	if owner.UserID != "" {
		userID = owner.UserID
	}
	if owner.AgentID != "" {
		agentID = owner.AgentID
	}
	content := "retrievalmarker " + fileName
	contentHash := sha256.Sum256([]byte(content))
	if _, err := database.Exec(t.Context(), `
		INSERT INTO library_file (
			id, scope, user_id, agent_id, file_name, media_type,
			size_bytes, raw_sha256, status, deleted_at
		) VALUES ($1, $2, $3, $4, $5, 'text/plain', $6, $7, $8, $9)
	`,
		fileID, owner.Scope, userID, agentID, fileName, int64(len(content)),
		bytes.Repeat([]byte{1}, sha256.Size), fileStatus, deletedAt,
	); err != nil {
		t.Fatalf("insert retrieval file %q: %v", fileName, err)
	}
	if _, err := database.Exec(t.Context(), `
		INSERT INTO library_chunk_set (
			id, file_id, derivation_key, processor_key, raw_sha256, status
		) VALUES ($1, $2, $3, 'test-parser:v1', $4, $5)
	`, setID, fileID, "retrieval:"+setID, bytes.Repeat([]byte{1}, sha256.Size), setStatus); err != nil {
		t.Fatalf("insert retrieval ChunkSet %q: %v", fileName, err)
	}
	if _, err := database.Exec(t.Context(), `
		INSERT INTO library_chunk (
			id, chunk_set_id, ordinal, content, locator, content_sha256
		) VALUES ($1, $2, 0, $3, '{"byte_start":0,"byte_end":1}'::jsonb, $4)
	`, chunkID, setID, content, contentHash[:]); err != nil {
		t.Fatalf("insert retrieval chunk %q: %v", fileName, err)
	}
	if active {
		if _, err := database.Exec(t.Context(), `
			UPDATE library_file SET active_chunk_set_id = $1 WHERE id = $2
		`, setID, fileID); err != nil {
			t.Fatalf("publish retrieval fixture %q: %v", fileName, err)
		}
	}
}
