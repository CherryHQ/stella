package admin

import (
	"net/http"

	"github.com/a-h/templ"
	"github.com/vaayne/anna/internal/admin/ui"
	"github.com/vaayne/anna/internal/admin/ui/pages"
)

func (s *Server) pageLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := ui.LoginLayout("/static/js/pages/login.js", pages.LoginPage()).Render(r.Context(), w); err != nil {
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
	s.renderPage(w, r, "sessions", "/static/js/pages/sessions.js", pages.SessionsPage())
}

func (s *Server) pageScheduler(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, r, "scheduler", "/static/js/pages/scheduler.js", pages.SchedulerPage())
}

func (s *Server) pageSettings(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, r, "settings", "/static/js/pages/settings.js", pages.SettingsPage())
}

// renderPage sets the HTML content type and renders the layout with the
// given page content. Auth info is extracted from context for the navbar.
func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, activePage, pageScript string, content templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	username := ""
	isAdmin := false
	if info := UserFromContext(r.Context()); info != nil {
		username = info.Username
		isAdmin = info.IsAdmin
	}

	if err := ui.Layout(activePage, pageScript, username, isAdmin, content).Render(r.Context(), w); err != nil {
		s.log.Error("render page", "page", activePage, "error", err)
	}
}
