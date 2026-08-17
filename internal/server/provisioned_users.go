package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/provisioning"
)

// ListProvisionedUsers handles GET /api/provisioned-users.
func (s *Server) ListProvisionedUsers(w http.ResponseWriter, r *http.Request, params apiserver.ListProvisionedUsersParams) {
	if requireProvisioningBearer(w, r) == nil {
		return
	}
	if s.provisioningSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "provisioning is unavailable")
		return
	}
	limit, cursor, err := parseProvisionedUserPage(params.PageSize, params.PageToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	users, err := s.provisioningSvc.ListAfter(r.Context(), limit+1, cursor)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list provisioned users", "error", err)
		writeError(w, http.StatusInternalServerError, "list provisioned users failed")
		return
	}
	page, next, err := nextProvisionedUserPage(users, limit)
	if err != nil {
		s.log.ErrorContext(r.Context(), "encode provisioned user page", "error", err)
		writeError(w, http.StatusInternalServerError, "list provisioned users failed")
		return
	}
	out := make([]apitypes.ProvisionedUser, 0, len(page))
	for _, user := range page {
		out = append(out, provisionedUserToAPI(user))
	}
	var nextPtr *string
	if next != "" {
		nextPtr = &next
	}
	writeData(w, http.StatusOK, apitypes.ProvisionedUserList{ProvisionedUsers: out, NextPageToken: nextPtr})
}

// CreateProvisionedUser handles POST /api/provisioned-users. The response is
// the only path that carries the minted plaintext token.
func (s *Server) CreateProvisionedUser(w http.ResponseWriter, r *http.Request) {
	info := requireProvisioningBearer(w, r)
	if info == nil {
		return
	}
	if s.provisioningSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "provisioning is unavailable")
		return
	}
	var body apitypes.CreateProvisionedUserRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	in, err := provisionedCreateInput(body, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.provisioningSvc.Create(r.Context(), provisioningIssuer(info), in)
	if err != nil {
		s.writeProvisioningError(w, err)
		return
	}
	writeData(w, http.StatusCreated, apitypes.CreateProvisionedUserResponse{ProvisionedUser: provisionedUserToAPI(result.User), Token: result.Token})
}

// GetProvisionedUser handles GET /api/provisioned-users/{id}.
func (s *Server) GetProvisionedUser(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	if requireProvisioningBearer(w, r) == nil {
		return
	}
	if s.provisioningSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "provisioning is unavailable")
		return
	}
	user, err := s.provisioningSvc.Get(r.Context(), id.String())
	if err != nil {
		s.writeProvisioningError(w, err)
		return
	}
	writeData(w, http.StatusOK, provisionedUserToAPI(user))
}

// RotateProvisionedUserToken handles POST /api/provisioned-users/{id}/rotate-token.
func (s *Server) RotateProvisionedUserToken(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	info := requireProvisioningBearer(w, r)
	if info == nil {
		return
	}
	if s.provisioningSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "provisioning is unavailable")
		return
	}
	var body apitypes.RotateProvisionedUserTokenRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	name, expiresAt, err := provisionedTokenInput(body.TokenName, body.ExpiresAt, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.provisioningSvc.Rotate(r.Context(), provisioningIssuer(info), id.String(), name, *expiresAt)
	if err != nil {
		s.writeProvisioningError(w, err)
		return
	}
	writeData(w, http.StatusOK, apitypes.RotateProvisionedUserTokenResponse{ProvisionedUser: provisionedUserToAPI(result.User), Token: result.Token})
}

// DeactivateProvisionedUser handles POST /api/provisioned-users/{id}/deactivate.
func (s *Server) DeactivateProvisionedUser(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	info := requireProvisioningBearer(w, r)
	if info == nil {
		return
	}
	if s.provisioningSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "provisioning is unavailable")
		return
	}
	authority, err := info.principal.Authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "provisioning token required")
		return
	}
	user, err := s.provisioningSvc.Deactivate(r.Context(), provisioningIssuer(info), authority, id.String())
	if err != nil {
		s.writeProvisioningError(w, err)
		return
	}
	writeData(w, http.StatusOK, provisionedUserToAPI(user))
}

// CreateProvisionedUserChannelIdentity handles
// POST /api/provisioned-users/{id}/channel-identities.
func (s *Server) CreateProvisionedUserChannelIdentity(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	info := requireProvisioningBearer(w, r)
	if info == nil {
		return
	}
	if s.provisioningSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "provisioning is unavailable")
		return
	}
	var body apitypes.CreateProvisionedUserChannelIdentityRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	in, err := provisionedChannelIdentityInput(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	identity, err := s.provisioningSvc.CreateChannelIdentity(r.Context(), provisioningIssuer(info), id.String(), in)
	if err != nil {
		s.writeProvisioningError(w, err)
		return
	}
	writeData(w, http.StatusCreated, provisionedChannelIdentityToAPI(identity))
}

const provisionedUserPageTokenKind = "provisioned_user"

type provisionedUserPageToken struct {
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

func parseProvisionedUserPage(pageSize *int, pageToken *string) (int, *provisioning.Cursor, error) {
	limit := defaultPageSize
	if pageSize != nil {
		if *pageSize < 1 || *pageSize > maxPageSize {
			return 0, nil, errors.New("page_size must be between 1 and 500")
		}
		limit = *pageSize
	}
	if pageToken == nil {
		return limit, nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(*pageToken)
	if err != nil {
		return 0, nil, errors.New("page_token is malformed")
	}
	var token provisionedUserPageToken
	if err := json.Unmarshal(payload, &token); err != nil || token.Kind != provisionedUserPageTokenKind || token.CreatedAt.IsZero() {
		return 0, nil, errors.New("page_token is malformed")
	}
	if _, err := uuid.Parse(token.ID); err != nil {
		return 0, nil, errors.New("page_token is malformed")
	}
	return limit, &provisioning.Cursor{CreatedAt: token.CreatedAt.UTC(), ID: token.ID}, nil
}

func nextProvisionedUserPage(users []provisioning.User, limit int) ([]provisioning.User, string, error) {
	if len(users) <= limit {
		return users, "", nil
	}
	page := users[:limit]
	last := page[len(page)-1]
	payload, err := json.Marshal(provisionedUserPageToken{Kind: provisionedUserPageTokenKind, CreatedAt: last.CreatedAt.UTC(), ID: last.ID})
	if err != nil {
		return nil, "", fmt.Errorf("encode provisioned user page token: %w", err)
	}
	return page, base64.RawURLEncoding.EncodeToString(payload), nil
}

func provisioningIssuer(info *AuthInfo) provisioning.Issuer {
	return provisioning.Issuer{UserID: info.UserID, TokenID: info.principal.CredentialID}
}

func provisionedCreateInput(body apitypes.CreateProvisionedUserRequest, now time.Time) (provisioning.CreateInput, error) {
	externalID := strings.TrimSpace(body.ExternalId)
	email := normalizeEmail(string(body.Email))
	name := strings.TrimSpace(body.Name)
	if externalID == "" || name == "" || email == "" {
		return provisioning.CreateInput{}, errors.New("external_id, email, and name are required")
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return provisioning.CreateInput{}, errors.New("email must be a valid address")
	}
	tokenName, expiresAt, err := provisionedTokenInput(body.TokenName, body.ExpiresAt, now)
	if err != nil {
		return provisioning.CreateInput{}, err
	}
	return provisioning.CreateInput{ExternalID: externalID, Email: email, Name: name, TokenName: tokenName, ExpiresAt: *expiresAt}, nil
}

func provisionedChannelIdentityInput(body apitypes.CreateProvisionedUserChannelIdentityRequest) (provisioning.ChannelIdentityInput, error) {
	platform := strings.TrimSpace(body.Platform)
	externalID := strings.TrimSpace(body.ExternalId)
	if platform == "" || externalID == "" {
		return provisioning.ChannelIdentityInput{}, errors.New("platform and external_id are required")
	}
	name := ""
	if body.Name != nil {
		name = strings.TrimSpace(*body.Name)
	}
	return provisioning.ChannelIdentityInput{Platform: platform, ExternalID: externalID, Name: name}, nil
}

func provisionedTokenInput(tokenName *string, requestedExpiry *time.Time, now time.Time) (string, *time.Time, error) {
	name := provisioning.DefaultTokenName
	if tokenName != nil {
		name = strings.TrimSpace(*tokenName)
		if name == "" {
			return "", nil, errors.New("token_name must not be empty")
		}
	}
	expiresAt, err := resolveProvisioningTokenExpiry(requestedExpiry, now)
	if err != nil {
		return "", nil, err
	}
	return name, expiresAt, nil
}

func provisionedUserToAPI(user provisioning.User) apitypes.ProvisionedUser {
	id := uuid.MustParse(user.ID)
	externalID, email, name, role := user.ExternalID, openapi_types.Email(user.Email), user.Name, user.Role
	isActive, createdAt, updatedAt := user.IsActive, user.CreatedAt.UTC(), user.UpdatedAt.UTC()
	out := apitypes.ProvisionedUser{Id: &id, ExternalId: &externalID, Email: &email, Name: &name, Role: &role, IsActive: &isActive, CreatedAt: &createdAt, UpdatedAt: &updatedAt}
	if user.ActiveToken != nil {
		token := user.ActiveToken
		id, name, last4, createdAt := token.ID, token.Name, token.Last4, token.CreatedAt.UTC()
		out.ActiveToken = &apitypes.ProvisionedUserToken{Id: &id, Name: &name, Last4: &last4, ExpiresAt: token.ExpiresAt, LastUsedAt: token.LastUsedAt, CreatedAt: &createdAt}
	}
	return out
}

func provisionedChannelIdentityToAPI(identity provisioning.ChannelIdentity) apitypes.ChannelIdentity {
	return apitypes.ChannelIdentity{
		Id: identity.ID, UserId: identity.UserID, Platform: identity.Platform,
		ExternalId: identity.ExternalID, Name: identity.Name,
		CreatedAt: identity.CreatedAt.UTC(), UpdatedAt: identity.UpdatedAt.UTC(),
	}
}

func (s *Server) writeProvisioningError(w http.ResponseWriter, err error) {
	var external *provisioning.ExternalIDConflict
	switch {
	case errors.As(err, &external):
		writeErrorDetails(w, http.StatusConflict, "external_id is already provisioned", map[string]any{"provisioned_user": provisionedUserToAPI(external.Existing)})
	case errors.Is(err, provisioning.ErrEmailDup):
		// Do not reveal whether this belongs to a self-registered, OIDC, or
		// otherwise unmanaged account.
		writeError(w, http.StatusConflict, "email is already in use")
	case errors.Is(err, provisioning.ErrIdentityDup):
		writeError(w, http.StatusConflict, "channel identity is already linked")
	case errors.Is(err, provisioning.ErrNotFound):
		writeError(w, http.StatusNotFound, "provisioned user not found")
	case errors.Is(err, provisioning.ErrForbidden):
		writeError(w, http.StatusForbidden, "provisioned user lifecycle forbidden")
	default:
		s.log.Error("provisioned user operation", "error", err)
		writeError(w, http.StatusInternalServerError, "provisioned user operation failed")
	}
}
