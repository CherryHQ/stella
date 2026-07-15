package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
	"github.com/CherryHQ/stella/internal/skills"
)

const (
	defaultPageSize = 20
	maxPageSize     = 500
)

// parsePageParams resolves AIP-158 page_size / page_token query parameters into
// a (limit, offset) pair. limit defaults to 20 and is bounded to [1, 500];
// page_size outside that range or a malformed token yields an error suitable
// for a 400 response. A nil page_size uses the default; a nil token starts at 0.
func parsePageParams(pageSize *int, pageToken *string) (limit, offset int, err error) {
	limit = defaultPageSize
	if pageSize != nil {
		if *pageSize < 1 || *pageSize > maxPageSize {
			return 0, 0, fmt.Errorf("page_size must be between 1 and %d", maxPageSize)
		}
		limit = *pageSize
	}
	if pageToken != nil {
		offset, err = decodeOffsetToken(*pageToken)
		if err != nil {
			return 0, 0, err
		}
	}
	return limit, offset, nil
}

// nextPageTokenForRows computes the next_page_token for a DB-backed list that
// fetched limit+1 rows to detect a further page. It returns the token (empty
// when no more rows) and the rows trimmed back to limit.
func nextPageTokenForRows[T any](rows []T, limit, offset int) (page []T, nextToken string) {
	if len(rows) > limit {
		return rows[:limit], encodeOffsetToken(offset + limit)
	}
	return rows, ""
}

const knowledgePageTokenKind = "knowledge"

const changelogPageTokenKind = "changelog"

const skillPageTokenKind = "skill"

type knowledgePageToken struct {
	Kind   string    `json:"kind"`
	State  string    `json:"state"`
	SortAt time.Time `json:"sort_at"`
	ID     string    `json:"id"`
}

type changelogPageToken struct {
	Kind   string    `json:"kind"`
	Scope  string    `json:"scope"`
	SortAt time.Time `json:"sort_at"`
	ID     string    `json:"id"`
}

type skillPageToken struct {
	Kind   string     `json:"kind"`
	State  string     `json:"state"`
	SortAt *time.Time `json:"sort_at"`
	ID     string     `json:"id"`
}

// encodeKnowledgePageToken keeps lifecycle positions opaque to clients and
// separate from the legacy offset-token format.
func encodeKnowledgePageToken(state memorywrite.KnowledgeState, cursor memorywrite.KnowledgeCursor) (string, error) {
	payload, err := json.Marshal(knowledgePageToken{
		Kind: knowledgePageTokenKind, State: string(state), SortAt: cursor.Timestamp.UTC(), ID: cursor.ID,
	})
	if err != nil {
		return "", fmt.Errorf("encode knowledge page token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeKnowledgePageToken(token string, state memorywrite.KnowledgeState) (*memorywrite.KnowledgeCursor, error) {
	if token == "" {
		return nil, fmt.Errorf("page_token is malformed")
	}
	payload, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("page_token is malformed")
	}
	var decoded knowledgePageToken
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, fmt.Errorf("page_token is malformed")
	}
	if decoded.Kind != knowledgePageTokenKind || decoded.State != string(state) || decoded.SortAt.IsZero() || decoded.ID == "" {
		return nil, fmt.Errorf("page_token does not match the knowledge query")
	}
	return &memorywrite.KnowledgeCursor{Timestamp: decoded.SortAt.UTC(), ID: decoded.ID}, nil
}

// encodeSkillPageToken binds a merged Skill cursor to its lifecycle state.
func encodeSkillPageToken(state skills.ManagedSkillState, cursor skills.ManagedSkillCursor) (string, error) {
	sortAt := cursor.Timestamp.UTC()
	payload, err := json.Marshal(skillPageToken{
		Kind: skillPageTokenKind, State: string(state), SortAt: &sortAt, ID: cursor.ID,
	})
	if err != nil {
		return "", fmt.Errorf("encode skill page token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeSkillPageToken(token string, state skills.ManagedSkillState) (*skills.ManagedSkillCursor, error) {
	if token == "" {
		return nil, fmt.Errorf("page_token is malformed")
	}
	payload, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("page_token is malformed")
	}
	var decoded skillPageToken
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, fmt.Errorf("page_token is malformed")
	}
	// Filesystem skills intentionally have a zero timestamp, so their stable ID
	// is sufficient to continue the final timestamp bucket.
	if decoded.Kind != skillPageTokenKind || decoded.State != string(state) || decoded.SortAt == nil || decoded.ID == "" {
		return nil, fmt.Errorf("page_token does not match the skill query")
	}
	return &skills.ManagedSkillCursor{Timestamp: decoded.SortAt.UTC(), ID: decoded.ID}, nil
}

func encodeChangelogPageToken(scope string, cursor memory.ChangelogCursor) (string, error) {
	payload, err := json.Marshal(changelogPageToken{
		Kind: changelogPageTokenKind, Scope: scope, SortAt: cursor.CreatedAt.UTC(), ID: cursor.ID,
	})
	if err != nil {
		return "", fmt.Errorf("encode changelog page token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeChangelogPageToken(token string, scope string) (*memory.ChangelogCursor, error) {
	if token == "" {
		return nil, fmt.Errorf("page_token is malformed")
	}
	payload, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("page_token is malformed")
	}
	var decoded changelogPageToken
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, fmt.Errorf("page_token is malformed")
	}
	if decoded.Kind != changelogPageTokenKind || decoded.Scope != scope || decoded.SortAt.IsZero() || decoded.ID == "" {
		return nil, fmt.Errorf("page_token does not match the changelog query")
	}
	return &memory.ChangelogCursor{CreatedAt: decoded.SortAt.UTC(), ID: decoded.ID}, nil
}
