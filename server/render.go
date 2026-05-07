package server

import (
	"net/http"

	"github.com/a-h/templ"
	"github.com/vaayne/anna/web"
	"github.com/vaayne/anna/web/pages"
)

func (s *Server) pageLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := web.LoginLayout("", pages.LoginPage(), "login").Render(r.Context(), w); err != nil {
		s.log.Error("render page", "page", "login", "error", err)
	}
}

func (s *Server) pageProviders(w http.ResponseWriter, r *http.Request) {
	s.renderPageWithVite(w, r, "providers", "", pages.ProvidersPage(), "providers")
}

func (s *Server) pageAgents(w http.ResponseWriter, r *http.Request) {
	s.renderPageWithVite(w, r, "agents", "", pages.AgentsPage(), "agents")
}

func (s *Server) pageChannels(w http.ResponseWriter, r *http.Request) {
	s.renderPageWithVite(w, r, "channels", "", pages.ChannelsPage(), "channels")
}

func (s *Server) pageUsers(w http.ResponseWriter, r *http.Request) {
	s.renderPageWithVite(w, r, "users", "", pages.UsersPage(), "users")
}

func (s *Server) pageSessions(w http.ResponseWriter, r *http.Request) {
	s.renderPageWithVite(w, r, "sessions", "", pages.SessionsPage(), "sessions")
}

func (s *Server) pageScheduler(w http.ResponseWriter, r *http.Request) {
	s.renderPageWithVite(w, r, "scheduler", "", pages.SchedulerPage(), "scheduler")
}

func (s *Server) pagePlugins(w http.ResponseWriter, r *http.Request) {
	s.renderPageWithVite(w, r, "plugins", "", pages.PluginsPage(), "plugins")
}

func (s *Server) pageProfile(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/account", http.StatusFound)
}

func (s *Server) pageAccount(w http.ResponseWriter, r *http.Request) {
	s.renderPageWithVite(w, r, "account", "", pages.AccountPage(), "account")
}

func (s *Server) pageCredentials(w http.ResponseWriter, r *http.Request) {
	s.renderPageWithVite(w, r, "credentials", "", pages.CredentialsPage(), "credentials")
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
