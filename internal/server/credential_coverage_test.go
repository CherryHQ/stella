package server

import (
	"net/http"
	"strings"
	"testing"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/credential"
)

// recordingMux captures the route patterns oapi-codegen registers, without
// serving anything.
type recordingMux struct{ patterns []string }

func (m *recordingMux) HandleFunc(pattern string, _ func(http.ResponseWriter, *http.Request)) {
	m.patterns = append(m.patterns, pattern)
}

func (m *recordingMux) ServeHTTP(http.ResponseWriter, *http.Request) {}

// TestEveryAPIRouteHasRegisteredScope is the registration-discipline gate: every
// generated /api route must be classified by credential.RequiredScope (either an
// exposed scope or an explicit deny). An unclassified route is a security gap --
// this fails the build so a new endpoint can never silently default.
func TestEveryAPIRouteHasRegisteredScope(t *testing.T) {
	rm := &recordingMux{}
	apiserver.HandlerFromMux(&Server{}, rm)

	seen := false
	for _, p := range rm.patterns {
		method, path, ok := strings.Cut(p, " ")
		if !ok || !strings.HasPrefix(path, "/api/") {
			continue
		}
		seen = true
		if _, _, registered := credential.RequiredScope(method, path); !registered {
			t.Errorf("route %s %s has no registered scope classification; add its resource to credential.RequiredScope (exposed) or deniedResources (denied)", method, path)
		}
	}
	if !seen {
		t.Fatal("no /api routes were enumerated; the coverage test is not exercising the router")
	}
}
