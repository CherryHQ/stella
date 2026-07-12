package share

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// Access is one share use case bound to exactly one Authorizer evaluation. The
// share Service is the sole policy-enforcement point for the ResourceShare
// capability: transports and the agent tool pass a trusted authz.Authority and
// never a bare identity. A share is user-owned — is_owner is always true for the
// acting user's own shares. agentScoped/agentID record the actor: an AgentActor
// (agent tool) is confined to its bound agent's workspace, while a UserActor
// (HTTP) selects the workspace agent from the request body, not from identity.
type Access struct {
	svc         *Service
	eval        authz.Evaluation
	userID      string
	agentID     string
	agentScoped bool
}

// Begin opens exactly one evaluation for one share use case.
func (s *Service) Begin(ctx context.Context, authority authz.Authority) (*Access, error) {
	if s == nil || s.authz == nil {
		return nil, fmt.Errorf("share authorization unavailable: authorizer not configured")
	}
	if !authority.Valid() {
		return nil, authz.ErrForbidden
	}
	eval, err := s.authz.Begin(ctx, authority)
	if err != nil {
		return nil, fmt.Errorf("share authorization begin: %w", err)
	}
	actor := authority.Actor()
	agentScoped := actor.Kind() == authz.ActorAgent
	agentID := ""
	if agentScoped {
		agentID = string(actor.AgentID())
	}
	return &Access{svc: s, eval: eval, userID: string(actor.UserID()), agentID: agentID, agentScoped: agentScoped}, nil
}

// ShareArtifact publishes a workspace file for the acting user. requestedAgentID
// selects which session/workspace to read: for a UserActor it is the request-body
// agent id (a resource selector, not identity); for an agent-scoped AgentActor it
// must equal the bound agent, so a delegated turn can never reach another agent's
// workspace. The UserID/AgentID/AgentScoped semantics passed to
// sessionWorkspaceRoot preserve the pre-cutover confinement exactly.
func (a *Access) ShareArtifact(ctx context.Context, sessionID, path, scope, requestedAgentID, expiresIn string) (Created, error) {
	if err := a.authorize(authz.ActionCreate); err != nil {
		return Created{}, err
	}
	if a.agentScoped && requestedAgentID != a.agentID {
		return Created{}, authz.ErrForbidden
	}
	if requestedAgentID == "" {
		return Created{}, fmt.Errorf("agent_id is required for artifact shares: %w", ErrInvalidInput)
	}
	if sessionID == "" {
		return Created{}, fmt.Errorf("session_id is required for artifact shares: %w", ErrInvalidInput)
	}
	if path == "" {
		return Created{}, fmt.Errorf("path is required for artifact shares: %w", ErrInvalidInput)
	}

	rel := path
	if stripped, ok := strings.CutPrefix(path, pkgsandbox.MountUserData+"/"); ok {
		scope, rel = "user", stripped
	} else if stripped, ok := strings.CutPrefix(path, pkgsandbox.MountWorkspace+"/"); ok {
		scope, rel = "agent", stripped
	}
	ident := authz.Identity{UserID: a.userID, AgentID: requestedAgentID, AgentScoped: a.agentScoped}
	root, err := a.svc.sessionWorkspaceRoot(ctx, ident, sessionID, scope)
	if err != nil {
		return Created{}, err
	}
	rootFS, name, err := OpenSafeRoot(root, rel)
	if err != nil {
		return Created{}, authz.ErrNotFound
	}
	defer func() { _ = rootFS.Close() }()
	abs := filepath.Join(root, name)
	fi, err := rootFS.Stat(name)
	if err != nil {
		if os.IsNotExist(err) && a.svc.assets.Restore(ctx, abs) == nil {
			fi, err = rootFS.Stat(name)
		}
		if err != nil {
			return Created{}, authz.ErrNotFound
		}
	}
	if fi.IsDir() {
		return Created{}, ErrDirectory
	}
	if fi.Size() > MaxShareSize {
		return Created{}, ErrTooLarge
	}
	mt := ArtifactMediaType(path)
	if mt == "" {
		return Created{}, ErrUnsupportedType
	}
	data, err := rootFS.ReadFile(name)
	if err != nil {
		return Created{}, err
	}
	return a.svc.create(ctx, a.userID, filepath.Base(path), mt, data, expiresIn)
}

// ShareArticle publishes a rendered Recally article owned by the acting user.
func (a *Access) ShareArticle(ctx context.Context, articleID, expiresIn string) (Created, error) {
	if err := a.authorize(authz.ActionCreate); err != nil {
		return Created{}, err
	}
	if articleID == "" {
		return Created{}, fmt.Errorf("article_id is required for article shares: %w", ErrInvalidInput)
	}
	article, err := a.svc.store.GetArticle(ctx, a.userID, articleID)
	if err != nil {
		return Created{}, authz.ErrNotFound
	}
	if article.UserID != a.userID {
		return Created{}, authz.ErrForbidden
	}
	md, err := a.svc.recallySvc.ReadArticleBody(ctx, article)
	if err != nil {
		return Created{}, err
	}
	if md == "" {
		return Created{}, ErrNoContent
	}
	rendered, err := RenderMarkdownPage(RenderMarkdownOpts{Title: article.Title, Author: article.Author, SourceURL: article.URL, Summary: article.Summary, Tags: article.Tags}, []byte(md))
	if err != nil {
		return Created{}, err
	}
	return a.svc.create(ctx, a.userID, article.Title, "text/html; charset=utf-8", rendered, expiresIn)
}

// List returns the acting user's own shares.
func (a *Access) List(ctx context.Context, limit, offset int) (ListResult, error) {
	if a.userID == "" {
		return ListResult{}, authz.ErrUnauthenticated
	}
	req, err := policy.ShareListRequest()
	if err != nil {
		return ListResult{}, authz.ErrForbidden
	}
	if err := a.decide(req); err != nil {
		return ListResult{}, err
	}
	rows, err := a.svc.q.ListSharesByUser(ctx, sqlc.ListSharesByUserParams{UserID: a.userID, Limit: int32(limit + 1), Offset: int32(offset)})
	if err != nil {
		return ListResult{}, err
	}
	page, next := pageRows(rows, limit, offset)
	return ListResult{Shares: page, NextPageToken: next}, nil
}

// Revoke disables one of the acting user's shares. A missing row is ErrNotFound
// (from the store), not a policy denial.
func (a *Access) Revoke(ctx context.Context, id string) error {
	if err := a.authorize(authz.ActionDelete); err != nil {
		return err
	}
	rows, err := a.svc.q.DeleteShareByUser(ctx, sqlc.DeleteShareByUserParams{ID: id, UserID: a.userID})
	if err != nil {
		return err
	}
	if rows == 0 {
		return authz.ErrNotFound
	}
	return nil
}

// authorize decides one share action for the acting user under this Access's
// single revision. is_owner is always true: the resource is the acting user's own
// share. A denial is an authenticated 403 (ErrForbidden).
func (a *Access) authorize(action authz.Action) error {
	if a.userID == "" {
		return authz.ErrUnauthenticated
	}
	facts := policy.OwnedFacts{Owner: a.userID, Agent: a.agentID, IsOwner: true}
	req, err := policy.ShareRequest(action, a.userID, a.userID, facts)
	if err != nil {
		return authz.ErrForbidden
	}
	return a.decide(req)
}

func (a *Access) decide(req authz.Request) error {
	dec, err := a.eval.Decide(req)
	if err != nil {
		return fmt.Errorf("share decide: %w", err)
	}
	if !dec.Allowed() {
		return authz.ErrForbidden
	}
	return nil
}
