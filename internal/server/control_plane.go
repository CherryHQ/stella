package server

import (
	"errors"
	"net/http"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/controlplane"
)

// beginControlPlane derives the trusted Authority for the authenticated caller.
// The controlplane Service is the sole enforcement point for provider, settings,
// plugin, and channel operations; Begin rejects non-admin actors before returning
// an Access. The handler never inspects identity beyond deriving the Authority
// from verified session claims.
func (s *Server) beginControlPlane(w http.ResponseWriter, r *http.Request) (*controlplane.Access, bool) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return nil, false
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return nil, false
	}
	acc, err := s.controlPlane.Begin(r.Context(), authority)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return nil, false
	}
	return acc, true
}

// controlPlaneError maps a control-plane PEP error to an HTTP status and client
// message, preserving the historical per-resource bodies. A ForbiddenError (a
// non-policy precondition, e.g. an env-locked sandbox backend) is a 403 with its
// own message and is only ever returned after authorization already succeeded, so
// it cannot leak to an unauthorized caller; an opaque policy denial is 403
// "forbidden".
func controlPlaneError(err error) (int, string) {
	var nf *controlplane.NotFoundError
	var ve *controlplane.ValidationError
	var fe *controlplane.ForbiddenError
	var ce *controlplane.ConflictError
	var ue *controlplane.UpstreamError
	var una *controlplane.UnavailableError
	switch {
	case err == nil:
		return 0, ""
	case errors.Is(err, controlplane.ErrUnavailable):
		return http.StatusServiceUnavailable, "control plane unavailable"
	case errors.As(err, &una):
		return http.StatusServiceUnavailable, una.Msg
	case errors.Is(err, authz.ErrUnauthenticated):
		return http.StatusUnauthorized, "authentication required"
	case errors.As(err, &fe):
		return http.StatusForbidden, fe.Msg
	case errors.Is(err, authz.ErrForbidden):
		return http.StatusForbidden, "forbidden"
	case errors.As(err, &nf):
		return http.StatusNotFound, nf.Msg
	case errors.As(err, &ce):
		return http.StatusConflict, ce.Msg
	case errors.As(err, &ve):
		return http.StatusBadRequest, ve.Msg
	case errors.As(err, &ue):
		return http.StatusBadGateway, "upstream service error"
	default:
		return http.StatusInternalServerError, "internal error"
	}
}

// writeControlPlaneError writes the mapped control-plane error, logging the causes
// the legacy handlers logged (internal 500s and upstream 502s) so operator
// visibility is preserved after the persistence moved into the PEP.
func (s *Server) writeControlPlaneError(w http.ResponseWriter, err error) {
	code, msg := controlPlaneError(err)
	switch code {
	case http.StatusInternalServerError:
		s.log.Error("control-plane handler error", "error", err)
	case http.StatusBadGateway:
		s.log.Error("control-plane upstream error", "error", err)
	}
	writeError(w, code, msg)
}
