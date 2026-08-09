package server

import (
	"context"
	"errors"
	"net/http"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
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

// beginChannelAccess opens channel management for the authenticated caller. An
// admin administers every channel in the deployment; anyone else administers the
// channels of the agents they manage, which is how the owner of an agent gives
// it a Telegram or Discord presence without an admin doing it for them.
//
// The Agent decision is made here, in the transport that already holds the Agent
// PEP, and handed to the control plane as a settled set of ids — the control
// plane never re-derives who may manage what.
func (s *Server) beginChannelAccess(w http.ResponseWriter, r *http.Request) (controlplane.ChannelAccess, bool) {
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
	if info.IsAdmin {
		acc, err := s.controlPlane.Begin(r.Context(), authority)
		if err != nil {
			s.writeControlPlaneError(w, err)
			return nil, false
		}
		return acc, true
	}
	managed, err := s.manageableAgentIDs(r.Context(), authority)
	if err != nil {
		s.writeInternalError(w, err)
		return nil, false
	}
	acc, err := s.controlPlane.BeginAgentChannels(r.Context(), authority, managed)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return nil, false
	}
	return acc, true
}

// manageableAgentIDs lists the agents the caller may manage, asking the Agent PEP
// for a Manage decision on every agent it can already see. Ownership is a small
// set (a user's own agents), so the per-agent decision stays cheap and no rule is
// duplicated here.
//
// The candidate set is deployment-wide on purpose: it is only a candidate set,
// narrowed a line later by a real Manage decision. A non-admin gets their own
// fleet regardless, and everything they can manage is in it.
func (s *Server) manageableAgentIDs(ctx context.Context, authority authz.Authority) ([]string, error) {
	agents, err := s.agentAccess.ListReadable(ctx, authority, true)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(agents))
	for _, agent := range agents {
		switch err := s.agentAccess.Authorize(ctx, authority, agent.ID, authz.ActionManage); {
		case err == nil:
			out = append(out, agent.ID)
		case errors.Is(err, agentaccess.ErrForbidden), errors.Is(err, agentaccess.ErrNotFound):
		default:
			return nil, err
		}
	}
	return out, nil
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
	switch {
	case err == nil:
		return 0, ""
	case errors.Is(err, controlplane.ErrUnavailable):
		return http.StatusServiceUnavailable, "control plane unavailable"
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
