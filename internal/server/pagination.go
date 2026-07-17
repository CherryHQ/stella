package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
	memprofile "github.com/CherryHQ/stella/internal/memory/profile"
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
	// Offset-backed list handlers fetch one probe row to detect a continuation.
	// Keep that complete window representable before any int32 conversion.
	if int64(offset)+int64(limit)+1 > math.MaxInt32 {
		return 0, 0, fmt.Errorf("page_token is outside the supported range")
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
	Kind             string     `json:"kind"`
	SortAt           *time.Time `json:"sort_at"`
	ID               string     `json:"id"`
	QueryFingerprint string     `json:"query_fingerprint"`
}

// skillPageQuery binds a cursor to every input that can change the merged Skill
// result set. Page size is intentionally excluded so clients may resize a page
// without changing the logical query.
type skillPageQuery struct {
	UserID     string `json:"user_id"`
	AgentID    string `json:"agent_id"`
	Scope      string `json:"scope"`
	ScopeGroup string `json:"scope_group"`
	Query      string `json:"q"`
	SessionID  string `json:"session_id"`
}

// encodeKnowledgePageToken keeps lifecycle positions opaque to clients and
// separate from the legacy offset-token format.
func encodeKnowledgePageToken(state memprofile.KnowledgeState, cursor memprofile.KnowledgeCursor) (string, error) {
	payload, err := json.Marshal(knowledgePageToken{
		Kind: knowledgePageTokenKind, State: string(state), SortAt: cursor.Timestamp.UTC(), ID: cursor.ID,
	})
	if err != nil {
		return "", fmt.Errorf("encode knowledge page token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeKnowledgePageToken(token string, state memprofile.KnowledgeState) (*memprofile.KnowledgeCursor, error) {
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
	return &memprofile.KnowledgeCursor{Timestamp: decoded.SortAt.UTC(), ID: decoded.ID}, nil
}

// encodeSkillPageToken keeps a merged Skill cursor opaque to clients.
func encodeSkillPageToken(cursor skills.ManagedSkillCursor, query skillPageQuery) (string, error) {
	sortAt := cursor.Timestamp.UTC()
	payload, err := json.Marshal(skillPageToken{
		Kind: skillPageTokenKind, SortAt: &sortAt, ID: cursor.ID, QueryFingerprint: skillPageQueryFingerprint(query),
	})
	if err != nil {
		return "", fmt.Errorf("encode skill page token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeSkillPageToken(token string, query skillPageQuery) (*skills.ManagedSkillCursor, error) {
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
	if decoded.Kind != skillPageTokenKind || decoded.SortAt == nil || decoded.ID == "" || decoded.QueryFingerprint != skillPageQueryFingerprint(query) {
		return nil, fmt.Errorf("page_token does not match the skill query")
	}
	return &skills.ManagedSkillCursor{Timestamp: decoded.SortAt.UTC(), ID: decoded.ID}, nil
}

func skillPageQueryFingerprint(query skillPageQuery) string {
	payload, _ := json.Marshal(query)
	sum := sha256.Sum256(payload)
	return base64.RawURLEncoding.EncodeToString(sum[:])
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
