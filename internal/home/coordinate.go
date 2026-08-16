package home

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

const projectCoordinateConstraint = "project_base_dir_canonical_check"

type persistedProjectCoordinate struct {
	id, userID, agentID, value string
}

// ProjectCoordinateReconcileResult reports legacy rows that cannot be mapped
// without guessing. They remain rejected by runtime coordinate validation, but
// no longer prevent unrelated Projects from being served.
type ProjectCoordinateReconcileResult struct {
	Updated       int
	UnresolvedIDs []string
}

// ResolveLogicalCoordinate canonicalizes portable sandbox coordinates.
func ResolveLogicalCoordinate(scope RootScope, value string, allowRoot bool) (RootScope, string, error) {
	if scope != RootAgentWorkspace && scope != RootPrincipalData {
		return 0, "", errors.New("home: invalid workspace scope")
	}
	if strings.Contains(value, `\`) {
		return 0, "", errors.New("home: malformed coordinate")
	}
	if name, suffix, ok, err := pkgsandbox.SplitLeadingPathVariable(value); err != nil {
		return 0, "", errors.New("home: malformed path variable")
	} else if ok {
		switch name {
		case pkgsandbox.EnvHome:
			scope, value = RootAgentWorkspace, strings.TrimPrefix(suffix, "/")
		case pkgsandbox.EnvStellaAssetsDir:
			scope, value = RootPrincipalData, "assets"+suffix
		default:
			return 0, "", errors.New("home: unsupported path variable")
		}
	} else if strings.HasPrefix(value, "/") {
		switch {
		case value == pkgsandbox.MountWorkspace:
			scope, value = RootAgentWorkspace, ""
		case strings.HasPrefix(value, pkgsandbox.MountWorkspace+"/"):
			scope, value = RootAgentWorkspace, strings.TrimPrefix(value, pkgsandbox.MountWorkspace+"/")
		case value == pkgsandbox.MountUserData:
			scope, value = RootPrincipalData, ""
		case strings.HasPrefix(value, pkgsandbox.MountUserData+"/"):
			scope, value = RootPrincipalData, strings.TrimPrefix(value, pkgsandbox.MountUserData+"/")
		default:
			return 0, "", errors.New("home: non-logical absolute coordinate")
		}
	}
	// Logical coordinates are portable persisted data. Reject a native absolute
	// path after rewriting the recognized sandbox aliases, and reject characters
	// that could become drive or stream syntax on another platform.
	if filepath.IsAbs(value) {
		return 0, "", errors.New("home: non-logical absolute coordinate")
	}
	if value == "" && allowRoot {
		return scope, ".", nil
	}
	if value == "." && allowRoot {
		return scope, ".", nil
	}
	if value == "" || path.IsAbs(value) || path.Clean(value) != value {
		return 0, "", errors.New("home: canonical relative name required")
	}
	for part := range strings.SplitSeq(value, "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsRune(part, ':') || strings.IndexFunc(part, func(r rune) bool {
			return r < ' ' || r == '\x7f'
		}) >= 0 {
			return 0, "", errors.New("home: canonical relative name required")
		}
	}
	return scope, value, nil
}

// ReconcileProjectCoordinates converts every safely resolvable legacy row and
// leaves ambiguous rows isolated behind runtime coordinate validation. The
// pending CHECK continues to reject new noncanonical values; it is validated
// only after the final historical row has been reconciled.
func (m *WorkspaceManager) ReconcileProjectCoordinates(ctx context.Context) (ProjectCoordinateReconcileResult, error) {
	var result ProjectCoordinateReconcileResult
	if err := m.admit(ctx); err != nil {
		return result, err
	}
	if err := m.verifyPinnedRoot(); err != nil {
		return result, err
	}
	validated, err := m.projectCoordinateConstraintValidated(ctx, m.db)
	if err != nil {
		return result, err
	}
	if validated {
		return result, nil
	}

	// Resolve filesystem-dependent historical coordinates before opening the
	// database transaction. The pending CHECK already rejects new noncanonical
	// values; conditional updates below tolerate rows concurrently deleted or
	// canonicalized while keeping slow filesystem inspection outside DB locks.
	rows, err := m.db.Query(ctx, "SELECT id, user_id, agent_id, base_dir FROM project ORDER BY id")
	if err != nil {
		return result, fmt.Errorf("home: inventory project coordinates: %w", err)
	}
	var projects []persistedProjectCoordinate
	for rows.Next() {
		var project persistedProjectCoordinate
		if err := rows.Scan(&project.id, &project.userID, &project.agentID, &project.value); err != nil {
			rows.Close()
			return result, fmt.Errorf("home: scan project coordinate: %w", err)
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, fmt.Errorf("home: inventory project coordinates: %w", err)
	}
	rows.Close()
	type projectCoordinateUpdate struct {
		project   persistedProjectCoordinate
		canonical string
	}
	updates := make([]projectCoordinateUpdate, 0, len(projects))
	for _, project := range projects {
		canonical, err := m.canonicalProjectCoordinate(project)
		if err != nil {
			result.UnresolvedIDs = append(result.UnresolvedIDs, project.id)
			continue
		}
		if canonical != project.value {
			updates = append(updates, projectCoordinateUpdate{project: project, canonical: canonical})
		}
	}

	tx, err := m.db.Begin(ctx)
	if err != nil {
		return result, fmt.Errorf("home: begin project coordinate migration: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '10s'"); err != nil {
		return result, fmt.Errorf("home: bound project coordinate migration lock: %w", err)
	}
	if err := m.verifyPinnedRoot(); err != nil {
		return result, err
	}
	validated, err = m.projectCoordinateConstraintValidated(ctx, tx)
	if err != nil {
		return result, err
	}
	if validated {
		return result, tx.Commit(ctx)
	}

	for _, update := range updates {
		project := update.project
		tag, err := tx.Exec(ctx, `
			UPDATE project SET base_dir = $1
			WHERE id = $2 AND user_id = $3 AND agent_id = $4 AND base_dir = $5
		`, update.canonical, project.id, project.userID, project.agentID, project.value)
		if err != nil {
			return result, fmt.Errorf("home: migrate project %s coordinate: %w", project.id, err)
		}
		result.Updated += int(tag.RowsAffected())
	}
	if len(result.UnresolvedIDs) == 0 {
		if _, err := tx.Exec(ctx, "ALTER TABLE project VALIDATE CONSTRAINT "+projectCoordinateConstraint); err != nil {
			return result, fmt.Errorf("home: validate project coordinates: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("home: commit project coordinate migration: %w", err)
	}
	return result, nil
}

type projectCoordinateConstraintReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (m *WorkspaceManager) projectCoordinateConstraintValidated(ctx context.Context, reader projectCoordinateConstraintReader) (bool, error) {
	var validated bool
	err := reader.QueryRow(ctx, `
		SELECT convalidated
		FROM pg_constraint
		WHERE conrelid = 'project'::regclass AND conname = $1
	`, projectCoordinateConstraint).Scan(&validated)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, errors.New("home: project coordinate schema migration is missing")
	}
	if err != nil {
		return false, fmt.Errorf("home: inspect project coordinate migration: %w", err)
	}
	return validated, nil
}

func (m *WorkspaceManager) canonicalProjectCoordinate(project persistedProjectCoordinate) (string, error) {
	value := project.value
	if value == "" {
		value = "."
	}
	if scope, name, err := ResolveLogicalCoordinate(RootAgentWorkspace, value, true); err == nil && scope == RootAgentWorkspace {
		return name, nil
	}
	if strings.ContainsRune(project.value, 0) {
		return "", errors.New("coordinate is neither canonical nor an absolute historical path")
	}
	if !filepath.IsAbs(project.value) {
		return "", errors.New("coordinate is neither canonical nor an absolute historical path")
	}
	parts, _, err := m.rootSelection(WorkspaceRequest{UserID: project.userID, AgentID: project.agentID}, RootAgentWorkspace)
	if err != nil {
		return "", err
	}
	root := filepath.Join(append([]string{m.base}, parts...)...)
	name, ok := containedProjectMigrationCoordinate(root, project.value)
	if ok {
		return name, nil
	}
	return m.knownLegacyProjectCoordinate(project, root)
}

func (m *WorkspaceManager) knownLegacyProjectCoordinate(project persistedProjectCoordinate, ownerRoot string) (string, error) {
	legacyRoot := filepath.Join(m.base, "workspaces", project.agentID, "users", project.userID)
	name, ok := containedProjectMigrationCoordinate(legacyRoot, project.value)
	if !ok || name != "." && !strings.HasPrefix(name, "repos/") {
		return "", errors.New("physical coordinate is outside its durable owner root")
	}
	// Two distinct existing trees provide no cheap, reliable evidence that a
	// potentially huge Project was copied completely. Keep the row isolated
	// until the historical source has been moved away.
	if _, err := os.Stat(project.value); err == nil {
		return "", errors.New("legacy project source still exists")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", errors.New("legacy project source cannot be inspected safely")
	}
	target := ownerRoot
	if name != "." {
		target = filepath.Join(ownerRoot, filepath.FromSlash(name))
	}
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		return "", errors.New("known legacy project target is not present under its durable owner root")
	}
	if canonical, contained := containedProjectMigrationCoordinate(ownerRoot, target); contained && canonical == name {
		return name, nil
	}
	return "", errors.New("known legacy project target escapes its durable owner root")
}

// containedProjectMigrationCoordinate is deliberately migration-only. Runtime
// consumers accept logical coordinates exclusively.
func containedProjectMigrationCoordinate(root, value string) (string, bool) {
	root, rootOK := resolveProjectMigrationPrefix(filepath.Clean(root))
	value, valueOK := resolveProjectMigrationPrefix(filepath.Clean(value))
	if !rootOK || !valueOK {
		return "", false
	}
	rel, err := filepath.Rel(root, value)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if rel == "." {
		return ".", true
	}
	name := filepath.ToSlash(rel)
	scope, name, err := ResolveLogicalCoordinate(RootAgentWorkspace, name, false)
	return name, err == nil && scope == RootAgentWorkspace
}

func resolveProjectMigrationPrefix(value string) (string, bool) {
	current := value
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, true
		}
		if _, lstatErr := os.Lstat(current); lstatErr == nil || !errors.Is(lstatErr, os.ErrNotExist) {
			return "", false
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}
