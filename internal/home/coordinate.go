package home

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/CherryHQ/stella/internal/asset"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// Coordinate describes an already-authorized workspace coordinate. Scope is
// retained for an unqualified relative name and replaced when Coordinate names
// one of the explicit logical or physical roots.
type Coordinate struct {
	Request   WorkspaceRequest
	Scope     RootScope
	Value     string
	AllowRoot bool
}

// CoordinateResolver is the Home-owned compatibility boundary for persisted
// and current workspace coordinates. Its result contains logical authority
// only; callers never receive a physical root.
type CoordinateResolver interface {
	ResolveCoordinate(Coordinate) (RootScope, string, error)
}

// AssetCompatibility is the temporary GA mutable-asset bridge. Callers must
// already hold the corresponding Home root capability; physical coordinates
// remain inside Home and asset.Store.
type AssetCompatibility interface {
	RestoreAsset(context.Context, *asset.Store, Coordinate) error
	WriteAsset(context.Context, *asset.Store, Coordinate, []byte, os.FileMode, bool) error
	UploadAsset(context.Context, *asset.Store, Coordinate, io.Reader, WriteOptions) error
	RemoveAsset(context.Context, *asset.Store, Coordinate) error
	MoveAsset(context.Context, *asset.Store, Coordinate, Coordinate) error
}

// ResolveLogicalCoordinate canonicalizes portable sandbox coordinates. It is
// exported so consumers with no host-coordinate compatibility requirement can
// use the same Home-owned normalization rules.
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

func (m *WorkspaceManager) ResolveCoordinate(c Coordinate) (RootScope, string, error) {
	if scope, name, err := ResolveLogicalCoordinate(c.Scope, c.Value, c.AllowRoot); err == nil {
		return scope, name, nil
	}
	if !filepath.IsAbs(c.Value) || strings.ContainsRune(c.Value, 0) {
		return 0, "", errors.New("home: invalid workspace coordinate")
	}
	for _, scope := range []RootScope{RootAgentWorkspace, RootPrincipalData} {
		parts, _, err := m.rootSelection(c.Request, scope)
		if err != nil {
			continue
		}
		root := filepath.Join(append([]string{m.base}, parts...)...)
		if name, ok := containedCoordinate(root, c.Value, c.AllowRoot); ok {
			return scope, name, nil
		}
	}
	return 0, "", errors.New("home: coordinate is outside authorized roots")
}

func (m *WorkspaceManager) assetCoordinate(c Coordinate) (string, error) {
	scope, name, err := m.ResolveCoordinate(c)
	if err != nil {
		return "", err
	}
	if scope != RootPrincipalData || !strings.HasPrefix(name, "assets/") {
		return "", errors.New("home: coordinate is not a mutable user asset")
	}
	parts, _, err := m.rootSelection(c.Request, scope)
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{m.base}, append(parts, filepath.FromSlash(name))...)...), nil
}

func (m *WorkspaceManager) RestoreAsset(ctx context.Context, store *asset.Store, c Coordinate) error {
	abs, err := m.assetCoordinate(c)
	if err != nil {
		return err
	}
	return store.Restore(ctx, abs)
}

func (m *WorkspaceManager) WriteAsset(ctx context.Context, store *asset.Store, c Coordinate, data []byte, mode os.FileMode, create bool) error {
	abs, err := m.assetCoordinate(c)
	if err != nil {
		return err
	}
	if create {
		err = store.CreateFile(ctx, abs, data, mode)
	} else {
		err = store.WriteFile(ctx, abs, data, mode)
	}
	if errors.Is(err, asset.ErrOutcomeUnknown) {
		return fmt.Errorf("%w: %w", ErrOutcomeUnknown, err)
	}
	return err
}

func (m *WorkspaceManager) UploadAsset(ctx context.Context, store *asset.Store, c Coordinate, src io.Reader, options WriteOptions) error {
	abs, err := m.assetCoordinate(c)
	if err != nil {
		return err
	}
	err = store.UploadFile(ctx, abs, src, options.Mode, options.MaxBytes, options.Sync)
	if errors.Is(err, asset.ErrUploadLimit) {
		return ErrUploadLimit
	}
	if errors.Is(err, asset.ErrOutcomeUnknown) {
		return fmt.Errorf("%w: %w", ErrOutcomeUnknown, err)
	}
	return err
}

func (m *WorkspaceManager) RemoveAsset(ctx context.Context, store *asset.Store, c Coordinate) error {
	abs, err := m.assetCoordinate(c)
	if err != nil {
		return err
	}
	return store.RemoveFile(ctx, abs)
}

func (m *WorkspaceManager) MoveAsset(ctx context.Context, store *asset.Store, source, destination Coordinate) error {
	src, err := m.assetCoordinate(source)
	if err != nil {
		return err
	}
	dst, err := m.assetCoordinate(destination)
	if err != nil {
		return err
	}
	err = store.MoveFile(ctx, src, dst)
	if errors.Is(err, asset.ErrOutcomeUnknown) {
		return fmt.Errorf("%w: %w", ErrOutcomeUnknown, err)
	}
	return err
}

func containedCoordinate(root, value string, allowRoot bool) (string, bool) {
	root, rootOK := resolveExistingPrefix(filepath.Clean(root))
	value, valueOK := resolveExistingPrefix(filepath.Clean(value))
	if !rootOK || !valueOK {
		return "", false
	}
	rel, err := filepath.Rel(root, value)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if rel == "." {
		return ".", allowRoot
	}
	name := filepath.ToSlash(rel)
	_, name, err = ResolveLogicalCoordinate(RootAgentWorkspace, name, false)
	return name, err == nil
}

// resolveExistingPrefix resolves the longest existing prefix and appends only
// lexical missing descendants. This accepts historical links to deleted files
// while still detecting an escaping symlink in any existing parent. It also
// canonicalizes platform aliases such as macOS /var -> /private/var.
func resolveExistingPrefix(value string) (string, bool) {
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
			// The component itself exists but cannot resolve (for example, a
			// dangling/looping symlink), or cannot be inspected. It is not a
			// lexical missing descendant and must fail closed.
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
