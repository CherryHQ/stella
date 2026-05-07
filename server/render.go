package server

import (
	"net/http"

	"github.com/a-h/templ"
	"github.com/vaayne/anna/web"
	"github.com/vaayne/anna/web/pages"
)

func (s *Server) pageLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := web.LoginLayout("/static/js/pages/login.js", pages.LoginPage()).Render(r.Context(), w); err != nil {
		s.log.Error("render page", "page", "login", "error", err)
	}
}

func (s *Server) pageProviders(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, r, "providers", "/static/js/pages/providers.js", pages.ProvidersPage())
}

func (s *Server) pageAgents(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, r, "agents", "/static/js/pages/agents.js", pages.AgentsPage())
}

func (s *Server) pageChannels(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, r, "channels", "/static/js/pages/channels.js", pages.ChannelsPage())
}

func (s *Server) pageUsers(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, r, "users", "/static/js/pages/users.js", pages.UsersPage())
}

func (s *Server) pageSessions(w http.ResponseWriter, r *http.Request) {
	s.renderPageWithVite(w, r, "sessions", "", pages.SessionsPage(), "sessions")
}

func (s *Server) pageScheduler(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, r, "scheduler", "/static/js/pages/scheduler.js", pages.SchedulerPage())
}

func (s *Server) pagePlugins(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, r, "plugins", "/static/js/pages/plugins.js", pages.PluginsPage())
}

func (s *Server) pageProfile(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/account", http.StatusFound)
}

func (s *Server) pageAccount(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, r, "account", "/static/js/pages/account.js", pages.AccountPage())
}

func (s *Server) pageCredentials(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, r, "credentials", "/static/js/pages/credentials.js", pages.CredentialsPage())
}

// renderPage sets the HTML content type and renders the layout with the
// given page content. Auth info is extracted from context for the navbar.
func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, activePage, pageScript string, content templ.Component) {
	s.renderPageWithVite(w, r, activePage, pageScript, content)
}

func (s *Server) renderPageWithVite(w http.ResponseWriter, r *http.Request, activePage, pageScript string, content templ.Component, viteEntries ...string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	username := ""
	isAdmin := false
	if info := UserFromContext(r.Context()); info != nil {
		username = info.Username
		isAdmin = info.IsAdmin
	}

	if err := web.Layout(activePage, pageScript, username, isAdmin, content, viteEntries...).Render(r.Context(), w); err != nil {
		s.log.Error("render page", "page", activePage, "error", err)
	}
}
