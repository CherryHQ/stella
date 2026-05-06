package server

import (
	"net/http"

	apiserver "github.com/vaayne/anna/api/server"
	"github.com/vaayne/anna/web"
)

func (s *Server) registerRoutes() {
	s.registerStaticRoutes()
	s.registerAuthRoutes()
	s.registerProfileRoutes()
	s.registerPageRoutes()
	s.registerAgentRoutes()
	s.registerUserRoutes()
	s.registerAuthUserRoutes()
	s.registerSessionRoutes()
	s.registerPluginRoutes()
	s.registerSkillRoutes()
	s.registerManifestPluginRoutes()
	s.registerAPIRoutes()
}

// registerAPIRoutes mounts the generated recally + scheduler REST API onto the
// admin mux. Auth is enforced by the global authMiddleware (Bearer + session).
func (s *Server) registerAPIRoutes() {
	apiserver.HandlerFromMux(s, s.mux)
}

func (s *Server) registerStaticRoutes() {
	s.mux.Handle("GET /static/", web.StaticHandler())
	s.mux.HandleFunc("GET /login", s.pageLogin)
	s.mux.HandleFunc("GET /{$}", s.redirectRoot)
}

func (s *Server) registerAuthRoutes() {
	s.mux.HandleFunc("POST /api/auth/register", s.registerHandler)
	s.mux.HandleFunc("POST /api/auth/login", s.loginHandler)
	s.mux.HandleFunc("POST /api/auth/logout", s.logoutHandler)
	s.mux.HandleFunc("GET /api/auth/me", s.meHandler)
}

func (s *Server) registerProfileRoutes() {
	s.mux.HandleFunc("GET /api/auth/profile/identities", s.listProfileIdentities)
	s.mux.HandleFunc("PUT /api/auth/profile/password", s.changePassword)
	s.mux.HandleFunc("POST /api/auth/profile/link-code", s.generateLinkCode)
	s.mux.HandleFunc("DELETE /api/auth/profile/identities/{id}", s.unlinkIdentity)
	s.mux.HandleFunc("GET /api/auth/profile/memories", s.listProfileMemories)
	s.mux.HandleFunc("PUT /api/auth/profile/memories/{agentId}", s.setProfileMemory)
	s.mux.HandleFunc("DELETE /api/auth/profile/memories/{agentId}", s.deleteProfileMemory)
	s.mux.HandleFunc("PUT /api/auth/profile/soul/{agentId}", s.setProfileSoul)

	// Vault (per-user encrypted secrets).
	s.mux.HandleFunc("GET /api/auth/profile/vault", s.listVaultEntries)
	s.mux.HandleFunc("PUT /api/auth/profile/vault/{name}", s.setVaultEntry)
	s.mux.HandleFunc("DELETE /api/auth/profile/vault/{name}", s.deleteVaultEntry)

	// OAuth CLI device-flow (connect/disconnect GitHub and Lark credentials).
	s.mux.HandleFunc("GET /api/auth/profile/oauth/providers", s.listOAuthProviders)
	s.mux.HandleFunc("POST /api/auth/profile/oauth/{provider}/start", s.startOAuthFlow)
	s.mux.HandleFunc("GET /api/auth/profile/oauth/{provider}/status/{flowID}", s.pollOAuthFlow)
	s.mux.HandleFunc("GET /api/auth/profile/oauth/{provider}/connected", s.getOAuthConnected)
	s.mux.HandleFunc("DELETE /api/auth/profile/oauth/{provider}", s.disconnectOAuth)
	s.mux.HandleFunc("GET /api/auth/profile/oauth/{provider}/callback", s.oauthCallback)

	// Self-service user skills.
	s.mux.HandleFunc("GET /api/auth/profile/skills", s.listProfileSkills)
	s.mux.HandleFunc("POST /api/auth/profile/skills/install", s.installProfileSkill)
	s.mux.HandleFunc("POST /api/auth/profile/skills/upload", s.uploadProfileSkill)
	s.mux.HandleFunc("GET /api/auth/profile/skills/{skillId}", s.getProfileSkill)
	s.mux.HandleFunc("GET /api/auth/profile/skills/{skillId}/file", s.getProfileSkillFile)
	s.mux.HandleFunc("PUT /api/auth/profile/skills/{skillId}", s.updateProfileSkill)
	s.mux.HandleFunc("DELETE /api/auth/profile/skills/{skillId}", s.deleteProfileSkill)
	s.mux.HandleFunc("DELETE /api/auth/profile/skills/{skillId}/file", s.deleteProfileSkillFile)
}

func (s *Server) registerPageRoutes() {
	s.mux.Handle("GET /providers", s.adminOnlyMiddleware(http.HandlerFunc(s.pageProviders)))
	s.mux.HandleFunc("GET /agents", s.pageAgents)
	s.mux.HandleFunc("GET /channels", s.pageChannels)
	s.mux.Handle("GET /users", s.adminOnlyMiddleware(http.HandlerFunc(s.pageUsers)))
	s.mux.HandleFunc("GET /sessions", s.pageSessions)
	s.mux.HandleFunc("GET /sessions/{sessionID}", s.pageSessions)
	s.mux.HandleFunc("GET /scheduler", s.pageScheduler)
	s.mux.Handle("GET /plugins", s.adminOnlyMiddleware(http.HandlerFunc(s.pagePlugins)))
	s.mux.HandleFunc("GET /profile", s.pageProfile)
	s.mux.HandleFunc("GET /account", s.pageAccount)
	s.mux.HandleFunc("GET /credentials", s.pageCredentials)
}

func (s *Server) registerAgentRoutes() {
	adminAPI := func(handler http.HandlerFunc) http.Handler {
		return s.adminOnlyMiddleware(handler)
	}
	s.mux.HandleFunc("GET /api/agents", s.listAgents)
	s.mux.HandleFunc("POST /api/agents", s.createAgent)
	s.mux.HandleFunc("GET /api/agents/{id}", s.getAgent)
	s.mux.HandleFunc("PUT /api/agents/{id}", s.updateAgent)
	s.mux.HandleFunc("DELETE /api/agents/{id}", s.deleteAgent)

	s.mux.Handle("GET /api/agents/{id}/users", adminAPI(s.listAgentUsers))
	s.mux.Handle("POST /api/agents/{id}/users", adminAPI(s.assignAgentUser))
	s.mux.Handle("DELETE /api/agents/{id}/users/{userId}", adminAPI(s.removeAgentUser))

	// Agent-scoped skills (creator or admin).
	s.mux.HandleFunc("GET /api/agents/{id}/skills", s.listAgentSkills)
	s.mux.HandleFunc("POST /api/agents/{id}/skills/install", s.installAgentSkill)
	s.mux.HandleFunc("POST /api/agents/{id}/skills/upload", s.uploadAgentSkill)
	s.mux.HandleFunc("POST /api/agents/{id}/skills/from-builtin/{skillId}", s.duplicateBuiltinSkillToAgent)
	s.mux.HandleFunc("GET /api/agents/{id}/skills/{skillId}", s.getAgentSkill)
	s.mux.HandleFunc("GET /api/agents/{id}/skills/{skillId}/file", s.getAgentSkillFile)
	s.mux.HandleFunc("PUT /api/agents/{id}/skills/{skillId}", s.updateAgentSkill)
	s.mux.HandleFunc("DELETE /api/agents/{id}/skills/{skillId}", s.deleteAgentSkill)
	s.mux.HandleFunc("DELETE /api/agents/{id}/skills/{skillId}/file", s.deleteAgentSkillFile)
}

func (s *Server) registerUserRoutes() {
	adminAPI := func(handler http.HandlerFunc) http.Handler {
		return s.adminOnlyMiddleware(handler)
	}
	s.mux.Handle("PUT /api/users/{id}/default-agent", adminAPI(s.updateUserDefaultAgent))
	s.mux.Handle("PUT /api/users/{id}/notify-identity", adminAPI(s.updateUserNotifyIdentity))
	s.mux.Handle("GET /api/users/{id}/memories", adminAPI(s.listUserMemories))
	s.mux.Handle("PUT /api/users/{id}/memories/{agentId}", adminAPI(s.setUserMemory))
	s.mux.Handle("DELETE /api/users/{id}/memories/{agentId}", adminAPI(s.deleteUserMemory))
}

func (s *Server) registerAuthUserRoutes() {
	adminAPI := func(handler http.HandlerFunc) http.Handler {
		return s.adminOnlyMiddleware(handler)
	}
	s.mux.Handle("GET /api/auth/users", adminAPI(s.listAuthUsers))
	s.mux.Handle("GET /api/auth/users/{id}", adminAPI(s.getAuthUser))
	s.mux.Handle("PUT /api/auth/users/{id}/role", adminAPI(s.updateAuthUserRole))
	s.mux.Handle("GET /api/auth/users/{id}/agents", adminAPI(s.listAuthUserAgents))
	s.mux.Handle("PUT /api/auth/users/{id}/agents", adminAPI(s.updateAuthUserAgents))
	s.mux.Handle("DELETE /api/auth/users/{id}/identities/{identityId}", adminAPI(s.deleteAuthUserIdentity))
	s.mux.Handle("PUT /api/auth/users/{id}/active", adminAPI(s.updateAuthUserActive))
}

func (s *Server) registerSessionRoutes() {
	s.mux.HandleFunc("GET /api/sessions", s.listSessions)
	s.mux.HandleFunc("POST /api/sessions", s.createSession)
	s.mux.HandleFunc("GET /api/sessions/{sessionID}", s.getSession)
	s.mux.HandleFunc("GET /api/sessions/{sessionID}/messages", s.getSessionMessages)
	s.mux.HandleFunc("GET /api/sessions/{sessionID}/system-prompt", s.getSessionSystemPrompt)
	s.mux.HandleFunc("POST /api/sessions/{sessionID}/messages", s.sendSessionMessage)
}

func (s *Server) registerPluginRoutes() {
	adminAPI := func(handler http.HandlerFunc) http.Handler {
		return s.adminOnlyMiddleware(handler)
	}
	s.mux.Handle("GET /api/plugins", adminAPI(s.listPlugins))
	s.mux.Handle("GET /api/plugin-status/{kind}/{name}", adminAPI(s.getPluginStatus))
	s.mux.Handle("GET /api/plugin-config/{kind}/{name}", adminAPI(s.getPluginConfig))
	s.mux.Handle("GET /api/plugin-config-schema/{kind}/{name}", adminAPI(s.getPluginConfigSchema))
	s.mux.Handle("PATCH /api/plugins/{id...}", adminAPI(s.togglePlugin))
	s.mux.Handle("PUT /api/plugin-config/{kind}/{name}", adminAPI(s.updatePluginConfig))
}

func (s *Server) registerSkillRoutes() {
	adminAPI := func(handler http.HandlerFunc) http.Handler {
		return s.adminOnlyMiddleware(handler)
	}
	s.mux.Handle("GET /api/skills", adminAPI(s.listSkills))
	s.mux.HandleFunc("GET /api/skills/search", s.searchSkills)
	s.mux.Handle("GET /api/skills/{id}", adminAPI(s.getSkill))
	s.mux.Handle("GET /api/skills/{id}/file", adminAPI(s.getSkillFile))
	s.mux.Handle("POST /api/skills", adminAPI(s.createSkill))
	s.mux.Handle("POST /api/skills/install", adminAPI(s.installSkill))
	s.mux.Handle("PUT /api/skills/{id}", adminAPI(s.updateSkill))
	s.mux.Handle("DELETE /api/skills/{id}", adminAPI(s.deleteSkill))
	s.mux.Handle("DELETE /api/skills/{id}/file", adminAPI(s.deleteSkillFile))
}

func (s *Server) registerManifestPluginRoutes() {
	adminAPI := func(handler http.HandlerFunc) http.Handler {
		return s.adminOnlyMiddleware(handler)
	}
	s.mux.Handle("GET /api/manifest-plugins", adminAPI(s.listManifestPlugins))
	s.mux.Handle("PUT /api/manifest-plugins", adminAPI(s.saveManifestPlugins))
	s.mux.Handle("POST /api/manifest-plugins/sync", adminAPI(s.syncManifestPlugins))
}
