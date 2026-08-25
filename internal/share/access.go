package share

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Access is one share use case bound to one trusted Authority. A share is a
// user-owned capability: ownership is the captured userID, and every durable
// query is scoped to it. agentScoped/agentID record the actor so an AgentActor
// (agent tool) is confined to its bound agent's workspace, while a UserActor
// (HTTP) selects the workspace agent from the request body, not from identity.
// Artifact publication additionally takes a fresh Agent read decision before
// opening the rooted workspace capability. The public capability-URL view
// (token hash + expiry, no session) is served outside Access entirely.
type Access struct {
	svc         *Service
	authority   authz.Authority
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
	return &Access{svc: s, authority: authority, userID: userID, agentID: agentID, agentScoped: agentScoped}, nil
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
	if a.svc.agents == nil {
		return Created{}, fmt.Errorf("agent authorization is unavailable")
	}
	if _, err := a.svc.agents.Read(ctx, a.authority, requestedAgentID); err != nil {
		switch {
		case errors.Is(err, agentaccess.ErrForbidden):
			return Created{}, authz.ErrForbidden
		case errors.Is(err, agentaccess.ErrNotFound):
			return Created{}, authz.ErrNotFound
		default:
			return Created{}, err
		}
	}

	ident := authz.Identity{UserID: a.userID, AgentID: requestedAgentID, AgentScoped: a.agentScoped}
	req, rootScope, err := a.svc.sessionWorkspaceRoot(ctx, ident, sessionID, scope)
	if err != nil {
		return Created{}, err
	}
	rootScope, rel, err := home.ResolveLogicalCoordinate(rootScope, path, false)
	if err != nil {
		if strings.HasPrefix(path, "$") {
			return Created{}, fmt.Errorf("%w: %w", ErrInvalidArtifactPath, err)
		}
		return Created{}, authz.ErrNotFound
	}
	root, err := a.svc.homes.OpenRoot(ctx, req, rootScope, home.RootReadOnly)
	if err != nil {
		return Created{}, err
	}
	snapshot, err := func() (data []byte, resultErr error) {
		defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
		fi, err := root.Stat(ctx, rel)
		if err != nil {
			return nil, authz.ErrNotFound
		}
		if fi.IsDir() {
			return nil, ErrDirectory
		}
		if fi.Size() > MaxShareSize {
			return nil, ErrTooLarge
		}
		if ArtifactMediaType(rel) == "" {
			return nil, ErrUnsupportedType
		}
		var content bytes.Buffer
		if err := root.Read(ctx, rel, &content, home.ReadOptions{MaxBytes: MaxShareSize}); err != nil {
			if errors.Is(err, home.ErrReadLimit) {
				return nil, ErrTooLarge
			}
			return nil, err
		}
		return content.Bytes(), nil
	}()
	if err != nil {
		return Created{}, err
	}
	mt := ArtifactMediaType(rel)
	return a.svc.create(ctx, a.userID, filepath.Base(rel), mt, snapshot, expiresIn)
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
	if _, guarded := agentrun.GuardFromContext(ctx); guarded && a.svc.db == nil {
		return fmt.Errorf("share AgentRun fencing is unavailable")
	}
	var rows int64
	var err error
	if a.svc.db != nil {
		rows, err = agentrun.WriteTxValue(ctx, a.svc.db, func(q *sqlc.Queries) (int64, error) {
			return q.DeleteShareByUser(ctx, sqlc.DeleteShareByUserParams{ID: id, UserID: a.userID})
		})
	} else {
		rows, err = a.svc.q.DeleteShareByUser(ctx, sqlc.DeleteShareByUserParams{ID: id, UserID: a.userID})
	}
	if err != nil {
		return err
	}
	if rows == 0 {
		return authz.ErrNotFound
	}
	return nil
}
