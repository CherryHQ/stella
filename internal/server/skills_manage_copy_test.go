package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	pluginpkg "github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/skill"
	"github.com/CherryHQ/stella/internal/skill/access"
)

func TestWritePackageCopyErrorHTTP(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "source invisible", err: pluginpkg.ErrNotFound, want: http.StatusNotFound},
		{name: "source retired", err: pluginpkg.ErrRetiredDefinition, want: http.StatusNotFound},
		{name: "digest conflict", err: pluginpkg.ErrConflict, want: http.StatusConflict},
		{name: "invalid package", err: pluginpkg.ErrInvalidDefinition, want: http.StatusBadRequest},
		{name: "invalid skill revision", err: skill.ErrInvalidSkillRevision, want: http.StatusBadRequest},
		{name: "target forbidden", err: access.ErrForbidden, want: http.StatusForbidden},
		{name: "target missing or inaccessible", err: access.ErrNotFound, want: http.StatusForbidden},
		{name: "duplicate target", err: skill.ErrSkillNameConflict, want: http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			(&Server{}).writePackageCopyError(recorder, tt.err)
			if recorder.Code != tt.want {
				t.Fatalf("HTTP status = %d, want %d, body=%s", recorder.Code, tt.want, recorder.Body.String())
			}
		})
	}

	// authz.ErrForbidden is a possible authority-level result from a package
	// reader. It must remain a destination authorization response, not 500.
	recorder := httptest.NewRecorder()
	(&Server{}).writePackageCopyError(recorder, authz.ErrForbidden)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("authz forbidden HTTP status = %d, want %d", recorder.Code, http.StatusForbidden)
	}

	if errors.Is(pluginpkg.ErrNotFound, access.ErrNotFound) {
		t.Fatal("plugin source not-found sentinel must stay distinct from target skill access not-found")
	}
}
