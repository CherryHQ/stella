package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/credential"
)

const (
	// defaultPATLifetime is applied when a create request omits expires_at
	// (expiry is default-required).
	defaultPATLifetime = 90 * 24 * time.Hour
	// maxPATLifetime caps an explicitly requested expiry.
	maxPATLifetime = 365 * 24 * time.Hour
)

// ListPersonalAccessTokens handles GET /api/users/me/tokens.
func (s *Server) ListPersonalAccessTokens(w http.ResponseWriter, r *http.Request) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if s.credResolver == nil {
		writeCapabilityUnavailable(w, capPAT)
		return
	}
	recs, err := s.credResolver.ListPAT(r.Context(), info.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list tokens failed")
		return
	}
	out := make([]apitypes.PersonalAccessToken, 0, len(recs))
	for _, rec := range recs {
		out = append(out, patToAPI(rec))
	}
	writeData(w, http.StatusOK, apitypes.PersonalAccessTokenList{Tokens: out})
}

// GetPersonalAccessToken handles GET /api/users/me/tokens/{id}. The lookup is
// scoped to the caller, so a token owned by another user is indistinguishable
// from a missing one (404) -- ownership is checked before existence is revealed.
func (s *Server) GetPersonalAccessToken(w http.ResponseWriter, r *http.Request, id string) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if s.credResolver == nil {
		writeCapabilityUnavailable(w, capPAT)
		return
	}
	rec, found, err := s.credResolver.GetPAT(r.Context(), id, info.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get token failed")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "token not found")
		return
	}
	writeData(w, http.StatusOK, patToAPI(rec))
}

// CreatePersonalAccessToken handles POST /api/users/me/tokens. The plaintext
// token is returned exactly once here and is never retrievable again.
func (s *Server) CreatePersonalAccessToken(w http.ResponseWriter, r *http.Request) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if s.credResolver == nil {
		writeCapabilityUnavailable(w, capPAT)
		return
	}

	var body apitypes.CreatePersonalAccessTokenRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	expiresAt, err := resolvePATExpiry(body.ExpiresAt, body.NeverExpires != nil && *body.NeverExpires, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	plaintext, rec, err := s.credResolver.CreatePAT(r.Context(), info.UserID, name, expiresAt)
	if err != nil {
		s.log.Error("create personal access token", "error", err, "user_id", info.UserID)
		writeError(w, http.StatusInternalServerError, "create token failed")
		return
	}
	writeData(w, http.StatusCreated, apitypes.CreatePersonalAccessTokenResponse{
		Token:               plaintext,
		PersonalAccessToken: patToAPI(rec),
	})
}

// RevokePersonalAccessToken handles DELETE /api/users/me/tokens/{id}.
func (s *Server) RevokePersonalAccessToken(w http.ResponseWriter, r *http.Request, id string) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if s.credResolver == nil {
		writeCapabilityUnavailable(w, capPAT)
		return
	}
	revoked, err := s.credResolver.RevokePAT(r.Context(), id, info.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "revoke token failed")
		return
	}
	if !revoked {
		writeError(w, http.StatusNotFound, "token not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func patToAPI(rec credential.PATRecord) apitypes.PersonalAccessToken {
	return apitypes.PersonalAccessToken{
		Id:         rec.ID,
		Name:       rec.Name,
		Last4:      rec.Last4,
		ExpiresAt:  rec.ExpiresAt,
		LastUsedAt: rec.LastUsedAt,
		RevokedAt:  rec.RevokedAt,
		CreatedAt:  rec.CreatedAt,
	}
}

// resolvePATExpiry decides the stored expiry:
//   - neverExpires   -> nil (explicit no-expiry opt-in)
//   - expiresAt set  -> that instant, rejected if in the past or beyond the max
//   - neither        -> now + defaultPATLifetime (expiry is default-required)
//
// maxPATLifetime deliberately bounds only explicit expiries; never_expires is an
// intentional opt-in that is NOT capped. No org-level policy toggle exists today;
// gate the neverExpires branch behind one if PAT lifetime limits are ever needed.
func resolvePATExpiry(expiresAt *time.Time, neverExpires bool, now time.Time) (*time.Time, error) {
	if neverExpires {
		return nil, nil
	}
	if expiresAt == nil {
		t := now.Add(defaultPATLifetime)
		return &t, nil
	}
	ts := expiresAt.UTC()
	if !ts.After(now) {
		return nil, errors.New("expires_at must be in the future")
	}
	if ts.After(now.Add(maxPATLifetime)) {
		return nil, errors.New("expires_at exceeds the maximum token lifetime")
	}
	return &ts, nil
}
