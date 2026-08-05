package share

import (
	"context"
	"errors"
	"fmt"

	sessionaccess "github.com/CherryHQ/stella/internal/agent/session/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Access is one share use case bound to one trusted Authority. Share snapshots
// are owned by its captured user; artifact and article reads perform their own
// source-specific authorization with this same Authority.
type Access struct {
	svc       *Service
	authority authz.Authority
	userID    string
}

// Access binds one share use case to a trusted Authority. It rejects an invalid
// Authority (403) and one carrying no user (401) up front.
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
	return &Access{svc: s, authority: authority, userID: userID}, nil
}

// ShareArtifact copies one exact session artifact into an immutable PostgreSQL
// snapshot. The source is never a host path and is never restored from blob.
func (a *Access) ShareArtifact(ctx context.Context, sessionID, artifactPath, scope, requestedAgentID, expiresIn string) (Created, error) {
	if requestedAgentID == "" {
		return Created{}, fmt.Errorf("agent_id is required for artifact shares: %w", ErrInvalidInput)
	}
	if sessionID == "" {
		return Created{}, fmt.Errorf("session_id is required for artifact shares: %w", ErrInvalidInput)
	}
	if artifactPath == "" {
		return Created{}, fmt.Errorf("path is required for artifact shares: %w", ErrInvalidInput)
	}
	if a.svc.sessions == nil {
		return Created{}, fmt.Errorf("session artifact reader is unavailable")
	}
	artifactScope := sessionaccess.WorkspaceScope(scope)
	if artifactScope == "" {
		artifactScope = sessionaccess.WorkspaceScopeAgent
	}
	reader, err := a.svc.sessions.Begin(ctx, a.authority)
	if err != nil {
		return Created{}, mapArtifactError(err)
	}
	artifact, err := reader.ReadArtifact(ctx, sessionaccess.ArtifactReadInput{
		AgentID: requestedAgentID, SessionID: sessionID, Scope: artifactScope, Path: artifactPath, MaxBytes: MaxShareSize,
	})
	if err != nil {
		return Created{}, mapArtifactError(err)
	}
	mediaType := ArtifactMediaType(artifact.Name)
	if mediaType == "" {
		return Created{}, ErrUnsupportedType
	}
	return a.svc.create(ctx, a.userID, artifact.Name, mediaType, artifact.Content, expiresIn)
}

func mapArtifactError(err error) error {
	switch {
	case errors.Is(err, sessionaccess.ErrNotFound):
		return authz.ErrNotFound
	case errors.Is(err, sessionaccess.ErrForbidden):
		return authz.ErrForbidden
	case errors.Is(err, sessionaccess.ErrIsDir):
		return ErrDirectory
	case errors.Is(err, sessionaccess.ErrTooLarge):
		return ErrTooLarge
	case errors.Is(err, sessionaccess.ErrInvalid):
		return ErrInvalidArtifactPath
	default:
		return err
	}
}

// ShareArticle renders an owner-scoped Recally article into an immutable share
// snapshot. Recally owns article authorization; Share does not repeat it.
func (a *Access) ShareArticle(ctx context.Context, articleID, expiresIn string) (Created, error) {
	if articleID == "" {
		return Created{}, fmt.Errorf("article_id is required for article shares: %w", ErrInvalidInput)
	}
	if a.svc.recally == nil {
		return Created{}, fmt.Errorf("recally service is unavailable")
	}
	recallyAccess, err := a.svc.recally.Access(a.authority)
	if err != nil {
		return Created{}, err
	}
	article, err := recallyAccess.GetArticle(ctx, articleID)
	if err != nil {
		return Created{}, err
	}
	md, err := recallyAccess.ReadArticleBody(ctx, article)
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
