package admin

import "net/http"

func (s *Server) registerRoutes() {
	s.registerStaticRoutes()
	s.registerAuthRoutes()
	s.registerProfileRoutes()
	s.registerPageRoutes()
	s.registerProviderRoutes()
	s.registerAgentRoutes()
	s.registerChannelRoutes()
	s.registerUserRoutes()
	s.registerAuthUserRoutes()
	s.registerSessionRoutes()
	s.registerPluginRoutes()
	s.registerModelRoutes()
	s.registerToolRoutes()
	s.registerSchedulerRoutes()
}

func (s *Server) registerStaticRoutes() {
	s.mux.Handle("GET /static/", staticHandler())
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
}

func (s *Server) registerPageRoutes() {
	s.mux.Handle("GET /providers", s.adminOnlyMiddleware(http.HandlerFunc(s.pageProviders)))
	s.mux.HandleFunc("GET /agents", s.pageAgents)
	s.mux.HandleFunc("GET /channels", s.pageChannels)
	s.mux.Handle("GET /users", s.adminOnlyMiddleware(http.HandlerFunc(s.pageUsers)))
	s.mux.HandleFunc("GET /sessions", s.pageSessions)
	s.mux.HandleFunc("GET /scheduler", s.pageScheduler)
	s.mux.Handle("GET /plugins", s.adminOnlyMiddleware(http.HandlerFunc(s.pagePlugins)))
	s.mux.HandleFunc("GET /profile", s.pageProfile)
}

func (s *Server) registerProviderRoutes() {
	adminAPI := func(handler http.HandlerFunc) http.Handler {
		return s.adminOnlyMiddleware(handler)
	}
	s.mux.Handle("GET /api/providers", adminAPI(s.listProviders))
	s.mux.Handle("POST /api/providers", adminAPI(s.createProvider))
	s.mux.Handle("GET /api/providers/{id}", adminAPI(s.getProvider))
	s.mux.Handle("PUT /api/providers/{id}", adminAPI(s.updateProvider))
	s.mux.Handle("DELETE /api/providers/{id}", adminAPI(s.deleteProvider))
	s.mux.Handle("GET /api/providers/{id}/models", adminAPI(s.listProviderModels))
	s.mux.Handle("POST /api/providers/{id}/models", adminAPI(s.fetchProviderModels))
	s.mux.Handle("GET /api/provider-types", adminAPI(s.listProviderTypes))
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
}

func (s *Server) registerChannelRoutes() {
	adminAPI := func(handler http.HandlerFunc) http.Handler {
		return s.adminOnlyMiddleware(handler)
	}
	s.mux.HandleFunc("GET /api/channels/link-options", s.listChannelLinkOptions)
	s.mux.Handle("GET /api/channels", adminAPI(s.listChannels))
	s.mux.Handle("POST /api/channels", adminAPI(s.createChannel))
	s.mux.Handle("GET /api/channels/{id}", adminAPI(s.getChannel))
	s.mux.Handle("PUT /api/channels/{id}", adminAPI(s.updateChannel))
	s.mux.Handle("DELETE /api/channels/{id}", adminAPI(s.deleteChannel))
	s.mux.HandleFunc("POST /api/channels/weixin/qr", s.startWeixinQR)
	s.mux.HandleFunc("GET /api/channels/weixin/qr/status", s.pollWeixinQRStatus)
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
	s.mux.HandleFunc("GET /api/sessions/{sessionID}", s.getSession)
	s.mux.HandleFunc("GET /api/sessions/{sessionID}/messages", s.getSessionMessages)
	s.mux.HandleFunc("GET /api/sessions/{sessionID}/system-prompt", s.getSessionSystemPrompt)
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

func (s *Server) registerModelRoutes() {
	s.mux.HandleFunc("GET /api/models", s.listCachedModels)
}

func (s *Server) registerToolRoutes() {
	s.mux.HandleFunc("GET /api/tools", s.listAgentTools)
}

func (s *Server) registerSchedulerRoutes() {
	s.mux.HandleFunc("GET /api/scheduler/jobs", s.listSchedulerJobs)
	s.mux.HandleFunc("POST /api/scheduler/jobs", s.createSchedulerJob)
	s.mux.HandleFunc("PUT /api/scheduler/jobs/{id}", s.updateSchedulerJob)
	s.mux.HandleFunc("DELETE /api/scheduler/jobs/{id}", s.deleteSchedulerJob)
}
