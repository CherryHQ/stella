package server

// registerLocalOIDCRoutes mounts the built-in local OIDC issuer endpoints under
// /oidc/local/. These routes are exempt from the auth middleware (they implement
// the issuer side of the OIDC protocol).
func (s *Server) registerLocalOIDCRoutes() {
	if s.localOIDC == nil {
		return
	}
	s.mux.HandleFunc("GET /oidc/local/.well-known/openid-configuration", s.localOIDC.HandleDiscovery)
	s.mux.HandleFunc("GET /oidc/local/jwks.json", s.localOIDC.HandleJWKS)
	s.mux.HandleFunc("GET /oidc/local/authorize", s.localOIDC.HandleAuthorize)
	s.mux.HandleFunc("POST /oidc/local/authorize", s.localOIDC.HandleAuthorize)
	s.mux.HandleFunc("POST /oidc/local/token", s.localOIDC.HandleToken)
	s.mux.HandleFunc("GET /oidc/local/userinfo", s.localOIDC.HandleUserinfo)
}

// localOIDCExempt reports whether path is a local OIDC issuer endpoint that
// must be exempt from the session auth middleware.
func localOIDCExempt(path string) bool {
	switch path {
	case "/oidc/local/.well-known/openid-configuration", "/oidc/local/jwks.json", "/oidc/local/authorize", "/oidc/local/token", "/oidc/local/userinfo":
		return true
	}
	return false
}
