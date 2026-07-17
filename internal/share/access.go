package share

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// Access is one share use case bound to one trusted Authority. A share is a
// user-owned capability: ownership is the captured userID, and every durable
// query is scoped to it. agentScoped/agentID record the actor so an AgentActor
// (agent tool) is confined to its bound agent's workspace, while a UserActor
// (HTTP) selects the workspace agent from the request body, not from identity.
// There is no policy evaluation; the acting user (and, for artifacts, the
// os.Root-confined workspace) is the boundary. The public capability-URL view
// (token hash + expiry, no session) is served outside Access entirely.
type Access struct {
	svc         *Service
	userID      string
	agentID     string
	agentScoped bool
}

// Access binds one share use case to a trusted Authority. It rejects an invalid
// Authority (403) and one carrying no user (401) up front, so every method can
// assume a non-empty acting user.
func (s *Service) Access(authority authz.Authority) (*Access, error) {
	if s == nil {
		return nil, fmt.Errorf("share service is unavailable — try again later")
	}
	if !authority.Valid() {
		return nil, authz.ErrForbidden
	}
	userID := string(authority.UserID())
	if userID == "" {
		return nil, authz.ErrUnauthenticated
	}
	agentScoped := authority.Kind() == authz.ActorAgent
	agentID := ""
	if agentScoped {
		agentID = string(authority.AgentID())
	}
	return &Access{svc: s, userID: userID, agentID: agentID, agentScoped: agentScoped}, nil
}

// ShareArtifact publishes a workspace file for the acting user. requestedAgentID
// selects which session/workspace to read: for a UserActor it is the request-body
// agent id (a resource selector, not identity); for an agent-scoped AgentActor it
// must equal the bound agent, so a delegated turn can never reach another agent's
// workspace. The UserID/AgentID/AgentScoped semantics passed to
// sessionWorkspaceRoot preserve the workspace confinement exactly.
func (a *Access) ShareArtifact(ctx context.Context, sessionID, path, scope, requestedAgentID, expiresIn string) (Created, error) {
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

// List returns the acting user's own shares. The query is scoped to the captured
// userID, so it can never surface another user's shares.
func (a *Access) List(ctx context.Context, limit, offset int) (ListResult, error) {
	rows, err := a.svc.q.ListSharesByUser(ctx, sqlc.ListSharesByUserParams{UserID: a.userID, Limit: int32(limit + 1), Offset: int32(offset)})
	if err != nil {
		return ListResult{}, err
	}
	page, next := pageRows(rows, limit, offset)
	shares := make([]Share, 0, len(page))
	for _, r := range page {
		shares = append(shares, summaryFromRow(r))
	}
	return ListResult{Shares: shares, NextPageToken: next}, nil
}

// Revoke disables one of the acting user's shares. The delete is scoped to the
// captured userID, so a share owned by another user is never touched; a missing
// (or foreign) row is ErrNotFound.
func (a *Access) Revoke(ctx context.Context, id string) error {
	rows, err := a.svc.q.DeleteShareByUser(ctx, sqlc.DeleteShareByUserParams{ID: id, UserID: a.userID})
	if err != nil {
		return err
	}
	if rows == 0 {
		return authz.ErrNotFound
	}
	return nil
}
