package server

import (
	"errors"
	"net/http"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/credential"
	"github.com/CherryHQ/stella/internal/oidc"
)

// OAuth2 client-management and authorized-apps API (issue #613). These are
// self-service routes under /api/users/me: a user manages their own clients and
// the apps they have authorized. Account-equivalent PATs may use them; delegated
// OAuth tokens are denied by usersRouteScope so a third-party token cannot
// manage OAuth clients.

// ListOAuthClients handles GET /api/users/me/oauth-clients.
func (s *Server) ListOAuthClients(w http.ResponseWriter, r *http.Request) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if s.oauthAS == nil {
		writeCapabilityUnavailable(w, capOAuthServer)
		return
	}
	clients, err := s.oauthAS.ListClients(r.Context(), info.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list clients failed")
		return
	}
	out := make([]apitypes.OAuthClient, 0, len(clients))
	for _, c := range clients {
		out = append(out, oauthClientToAPI(c))
	}
	writeData(w, http.StatusOK, apitypes.OAuthClientList{OauthClients: out})
}

// CreateOAuthClient handles POST /api/users/me/oauth-clients. The plaintext
// secret is returned exactly once (empty for public clients).
func (s *Server) CreateOAuthClient(w http.ResponseWriter, r *http.Request) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if s.oauthAS == nil {
		writeCapabilityUnavailable(w, capOAuthServer)
		return
	}
	var body apitypes.CreateOAuthClientRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	clientType := oidc.ClientTypeConfidential
	if body.ClientType != nil {
		clientType = string(*body.ClientType)
	}
	client, secret, err := s.oauthAS.RegisterClient(r.Context(), info.UserID, oidc.ClientRegistration{
		Name:         body.Name,
		ClientType:   clientType,
		RedirectURIs: body.RedirectUris,
		Scopes:       body.Scopes,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp := apitypes.CreateOAuthClientResponse{OauthClient: oauthClientToAPI(client)}
	if secret != "" {
		resp.ClientSecret = &secret
	}
	writeData(w, http.StatusCreated, resp)
}

// DisableOAuthClient handles DELETE /api/users/me/oauth-clients/{clientId}.
func (s *Server) DisableOAuthClient(w http.ResponseWriter, r *http.Request, clientId string) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if s.oauthAS == nil {
		writeCapabilityUnavailable(w, capOAuthServer)
		return
	}
	ok, err := s.oauthAS.DisableClient(r.Context(), info.UserID, clientId)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "disable client failed")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "client not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RotateOAuthClientSecret handles POST /api/users/me/oauth-clients/{clientId}/rotate-secret.
func (s *Server) RotateOAuthClientSecret(w http.ResponseWriter, r *http.Request, clientId string) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if s.oauthAS == nil {
		writeCapabilityUnavailable(w, capOAuthServer)
		return
	}
	secret, err := s.oauthAS.RotateSecret(r.Context(), info.UserID, clientId)
	if err != nil {
		if errors.Is(err, oidc.ErrClientNotFound) {
			writeError(w, http.StatusNotFound, "client not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeData(w, http.StatusOK, apitypes.RotateOAuthClientSecretResponse{ClientSecret: secret})
}

// ListOAuthClientScopes handles GET /api/users/me/oauth-client-scopes: the
// single source of truth for the client-registration UI (catalog + OAuth
// exposability policy).
func (s *Server) ListOAuthClientScopes(w http.ResponseWriter, r *http.Request) {
	if info := requireAuth(w, r); info == nil {
		return
	}
	var scopes []apitypes.OAuthClientScope
	for _, sc := range credential.Catalog() {
		if !sc.ExposableToOAuth {
			continue
		}
		scopes = append(scopes,
			apitypes.OAuthClientScope{Id: sc.Resource + ":*", Description: sc.Description + " (read and write)"},
			apitypes.OAuthClientScope{Id: sc.Resource + ":read", Description: sc.Description + " (read only)"},
		)
	}
	writeData(w, http.StatusOK, apitypes.OAuthClientScopeList{Scopes: scopes})
}

// ListAuthorizedApps handles GET /api/users/me/authorized-apps.
func (s *Server) ListAuthorizedApps(w http.ResponseWriter, r *http.Request) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if s.oauthAS == nil {
		writeCapabilityUnavailable(w, capOAuthServer)
		return
	}
	apps, err := s.oauthAS.ListAuthorizedApps(r.Context(), info.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list authorized apps failed")
		return
	}
	out := make([]apitypes.AuthorizedApp, 0, len(apps))
	for _, a := range apps {
		out = append(out, apitypes.AuthorizedApp{
			ClientId:   a.ClientID,
			ClientName: a.ClientName,
			Scopes:     a.Scopes,
			GrantedAt:  a.GrantedAt,
		})
	}
	writeData(w, http.StatusOK, apitypes.AuthorizedAppList{AuthorizedApps: out})
}

// RevokeAuthorizedApp handles DELETE /api/users/me/authorized-apps/{clientId}.
func (s *Server) RevokeAuthorizedApp(w http.ResponseWriter, r *http.Request, clientId string) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	if s.oauthAS == nil {
		writeCapabilityUnavailable(w, capOAuthServer)
		return
	}
	if err := s.oauthAS.RevokeGrant(r.Context(), info.UserID, clientId); err != nil {
		writeError(w, http.StatusInternalServerError, "revoke grant failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func oauthClientToAPI(c oidc.Client) apitypes.OAuthClient {
	return apitypes.OAuthClient{
		ClientId:     c.ClientID,
		Name:         c.Name,
		ClientType:   apitypes.OAuthClientClientType(c.ClientType),
		RedirectUris: c.RedirectURIs,
		Scopes:       c.Scopes,
		Disabled:     c.Disabled,
		CreatedAt:    c.CreatedAt,
	}
}
