package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/credential"
)

// ListProvisioningTokens handles GET /api/admin/provisioning-tokens. Only an
// interactive admin session can inspect the provisioning credentials it owns.
func (s *Server) ListProvisioningTokens(w http.ResponseWriter, r *http.Request) {
	info := requireInteractiveAdmin(w, r)
	if info == nil {
		return
	}
	if s.credResolver == nil {
		writeCapabilityUnavailable(w, capPAT)
		return
	}
	recs, err := s.credResolver.ListProvisioningTokens(r.Context(), info.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list provisioning tokens failed")
		return
	}
	out := make([]apitypes.ProvisioningToken, 0, len(recs))
	for _, rec := range recs {
		out = append(out, provisioningTokenToAPI(rec))
	}
	writeData(w, http.StatusOK, apitypes.ProvisioningTokenList{ProvisioningTokens: out})
}

// CreateProvisioningToken handles POST /api/admin/provisioning-tokens. The
// plaintext token is intentionally returned exactly once and always expires.
func (s *Server) CreateProvisioningToken(w http.ResponseWriter, r *http.Request) {
	info := requireInteractiveAdmin(w, r)
	if info == nil {
		return
	}
	if s.credResolver == nil {
		writeCapabilityUnavailable(w, capPAT)
		return
	}

	var body apitypes.CreateProvisioningTokenRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	expiresAt, err := resolveProvisioningTokenExpiry(body.ExpiresAt, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	plaintext, rec, err := s.credResolver.CreateProvisioningToken(r.Context(), info.UserID, name, *expiresAt)
	if err != nil {
		if errors.Is(err, credential.ErrForbidden) {
			writeError(w, http.StatusForbidden, "active admin account required")
			return
		}
		if errors.Is(err, credential.ErrProvisioningTokenLimit) {
			writeError(w, http.StatusConflict, "at most two active provisioning tokens are allowed")
			return
		}
		s.log.Error("create provisioning token", "error", err, "user_id", info.UserID)
		writeError(w, http.StatusInternalServerError, "create provisioning token failed")
		return
	}
	writeData(w, http.StatusCreated, apitypes.CreateProvisioningTokenResponse{
		Token:             plaintext,
		ProvisioningToken: provisioningTokenToAPI(rec),
	})
}

// RevokeProvisioningToken handles DELETE /api/admin/provisioning-tokens/{id}.
func (s *Server) RevokeProvisioningToken(w http.ResponseWriter, r *http.Request, id string) {
	info := requireInteractiveAdmin(w, r)
	if info == nil {
		return
	}
	if s.credResolver == nil {
		writeCapabilityUnavailable(w, capPAT)
		return
	}
	revoked, err := s.credResolver.RevokeProvisioningToken(r.Context(), id, info.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "revoke provisioning token failed")
		return
	}
	if !revoked {
		writeError(w, http.StatusNotFound, "provisioning token not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func provisioningTokenToAPI(rec credential.PATRecord) apitypes.ProvisioningToken {
	return apitypes.ProvisioningToken{
		Id:         rec.ID,
		Name:       rec.Name,
		Last4:      rec.Last4,
		ExpiresAt:  rec.ExpiresAt,
		LastUsedAt: rec.LastUsedAt,
		RevokedAt:  rec.RevokedAt,
		CreatedAt:  rec.CreatedAt,
	}
}

// resolveProvisioningTokenExpiry applies the normal 90-day default and one-year
// ceiling but deliberately provides no never-expiring escape hatch.
func resolveProvisioningTokenExpiry(expiresAt *time.Time, now time.Time) (*time.Time, error) {
	return resolvePATExpiry(expiresAt, false, now)
}
