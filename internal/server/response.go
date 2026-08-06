package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// isNotFound reports whether err means the requested resource does not exist:
// either an empty result (pgx.ErrNoRows) or a lookup id that is not even a valid
// uuid, which PostgreSQL rejects when casting the text parameter (SQLSTATE
// 22P02, invalid_text_representation). Mapping both to one predicate lets a
// by-id handler answer 404 for a malformed id instead of leaking a 500.
func isNotFound(err error) bool {
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, fs.ErrNotExist) {
		return true
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "22P02"
}

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation (SQLSTATE 23505), which callers map to 409 Conflict. The store layer
// wraps the driver error with %w, so errors.As reaches the *pgconn.PgError. This
// replaces the pre-migration strings.Contains(err, "UNIQUE constraint") check,
// which matched SQLite's message text and is dead under PostgreSQL (whose message
// is the lowercase "duplicate key value violates unique constraint ...").
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// Pagination tokens are deliberately offset-based, not keyset-based. The token
// is base64(offset) and carries no filter fingerprint or expiry. This is a
// pragmatic choice for an internal API served only to our own frontend + CLI:
// it keeps every list handler a one-liner and is correct for the read-mostly
// collections here. The known tradeoffs versus the keyset scheme in
// web/content/docs/development/rules/api-design.md: (1) under concurrent inserts ahead of the cursor a
// row can be skipped or duplicated at a page boundary — the per-query
// `ORDER BY <ts>, id` tiebreaker makes the order stable but does not eliminate
// offset drift; (2) a client may change filter params between pages without a
// 400. Revisit (keyset + filter-bound, expiring tokens) if this API is ever
// exposed to third parties.

// encodeOffsetToken encodes an offset into an opaque page token (AIP-158).
// Callers treat the result as opaque; clients must not parse it.
func encodeOffsetToken(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

// decodeOffsetToken decodes an opaque page token back into an offset. An empty
// token yields offset 0; a malformed token yields an error.
func decodeOffsetToken(token string) (int, error) {
	if token == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, err
	}
	offset, err := strconv.Atoi(string(raw))
	if err != nil {
		return 0, err
	}
	if offset < 0 || offset > math.MaxInt32 {
		return 0, fmt.Errorf("offset in page token is outside the supported range: %d", offset)
	}
	return offset, nil
}

// writeData writes a success JSON response with the given data.
func writeData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// writeError writes a structured error response per AIP-193:
// {"error": {"code", "message", "status"}}.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeErrorDetails(w, status, msg, nil)
}

func writeErrorDetails(w http.ResponseWriter, status int, msg string, details map[string]any) {
	errorBody := map[string]any{
		"code":    status,
		"message": msg,
		"status":  statusName(status),
	}
	if len(details) > 0 {
		errorBody["details"] = details
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": errorBody})
}

func writeLoggedError(w http.ResponseWriter, log *slog.Logger, status int, clientMessage string, err error) {
	log.Error("http handler error", "status", status, "error", err)
	writeError(w, status, clientMessage)
}

func (s *Server) writeInternalError(w http.ResponseWriter, err error) {
	writeLoggedError(w, s.log, http.StatusInternalServerError, "internal error", err)
}

func (s *Server) writeBadGatewayError(w http.ResponseWriter, err error) {
	writeLoggedError(w, s.log, http.StatusBadGateway, "upstream service error", err)
}

// statusName maps an HTTP status code to its canonical AIP status name.
func statusName(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusConflict:
		return "ABORTED"
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return "DEADLINE_EXCEEDED"
	case http.StatusRequestEntityTooLarge, http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case http.StatusServiceUnavailable:
		return "UNAVAILABLE"
	case http.StatusBadGateway:
		return "UNAVAILABLE"
	case http.StatusInternalServerError:
		return "INTERNAL"
	default:
		return "UNKNOWN"
	}
}

// writeNoContent writes a 204 No Content response with no body.
func writeNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// decodeJSON reads a JSON body into dst.
func decodeJSON(r *http.Request, dst any) error {
	return json.NewDecoder(r.Body).Decode(dst)
}
