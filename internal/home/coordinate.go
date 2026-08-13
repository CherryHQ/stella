package home

import (
	"context"
	"errors"
	"fmt"
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

// EnsureProjectCoordinates completes the one-time physical-to-logical project
// migration before project runtime wiring. The SQL migration preserves every
// historical value and installs a NOT VALID constraint; this filesystem-aware
// pass resolves physical paths and symlink aliases under the exact durable owner
// root, then validates that constraint. Once validated, the method is a read-only
// no-op and no runtime compatibility path remains.
func (m *WorkspaceManager) EnsureProjectCoordinates(ctx context.Context) error {
	if err := m.verifyPinnedRoot(); err != nil {
		return err
	}
	validated, err := m.projectCoordinateConstraintValidated(ctx, m.db)
	if err != nil {
		return err
	}
	if validated {
		return nil
	}

	// Resolve filesystem-dependent historical coordinates before opening the
	// database transaction. The pending CHECK already rejects new noncanonical
	// values; conditional updates below tolerate rows concurrently deleted or
	// canonicalized while keeping slow filesystem inspection outside DB locks.
	rows, err := m.db.Query(ctx, "SELECT id, user_id, agent_id, base_dir FROM project ORDER BY id")
	if err != nil {
		return fmt.Errorf("home: inventory project coordinates: %w", err)
	}
	var projects []persistedProjectCoordinate
	for rows.Next() {
		var project persistedProjectCoordinate
		if err := rows.Scan(&project.id, &project.userID, &project.agentID, &project.value); err != nil {
			rows.Close()
			return fmt.Errorf("home: scan project coordinate: %w", err)
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("home: inventory project coordinates: %w", err)
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
			return fmt.Errorf("home: project %s has an unresolvable legacy base_dir: %w", project.id, err)
		}
		if canonical != project.value {
			updates = append(updates, projectCoordinateUpdate{project: project, canonical: canonical})
		}
	}

	tx, err := m.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("home: begin project coordinate migration: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '10s'"); err != nil {
		return fmt.Errorf("home: bound project coordinate migration lock: %w", err)
	}
	if err := m.verifyPinnedRoot(); err != nil {
		return err
	}
	validated, err = m.projectCoordinateConstraintValidated(ctx, tx)
	if err != nil {
		return err
	}
	if validated {
		return tx.Commit(ctx)
	}

	for _, update := range updates {
		project := update.project
		if _, err := tx.Exec(ctx, `
			UPDATE project SET base_dir = $1
			WHERE id = $2 AND user_id = $3 AND agent_id = $4 AND base_dir = $5
		`, update.canonical, project.id, project.userID, project.agentID, project.value); err != nil {
			return fmt.Errorf("home: migrate project %s coordinate: %w", project.id, err)
		}
	}
	if _, err := tx.Exec(ctx, "ALTER TABLE project VALIDATE CONSTRAINT "+projectCoordinateConstraint); err != nil {
		return fmt.Errorf("home: validate project coordinates: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("home: commit project coordinate migration: %w", err)
	}
	return nil
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
	if !ok {
		return "", errors.New("physical coordinate is outside its durable owner root")
	}
	return name, nil
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
