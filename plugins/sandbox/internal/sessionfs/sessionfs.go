package sessionfs

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// Mount is a provider-private mapping from one process-visible root to its
// physical backing directory.
type Mount struct {
	HostPath              string
	SandboxPath           string
	ReadOnly              bool
	ResolveSymlinkAliases bool
	physicalPath          string
	processAliases        []string
	root                  *os.Root
}

// Resolved is a provider-private path resolution. Upper layers receive only
// mediated FileAccess operations and never this physical mapping.
type Resolved struct {
	Mount       Mount
	Relative    string
	SandboxPath string
}

// HostPath returns the resolved physical path for provider process setup. File
// operations must use OpenRoot instead so validation and access share one root
// capability.
func (r Resolved) HostPath() string {
	if r.Relative == "." {
		return r.Mount.HostPath
	}
	return filepath.Join(r.Mount.HostPath, filepath.FromSlash(r.Relative))
}

// Resolver is one immutable provider-private mount plan. Canonical and physical
// roots are pairwise disjoint, so one rooted capability always owns an operation
// and a symlink cannot bypass a nested mount's access mode. Cross-mount symlinks
// intentionally fail closed.
type Resolver struct {
	workingDir string
	mounts     []Mount
	mu         sync.RWMutex
	closed     bool
	closeOnce  sync.Once
	closeErr   error
}

func NewResolver(workingDir string, mounts []Mount) (*Resolver, error) {
	if workingDir == "" {
		return nil, errors.New("sandbox: process-visible working directory is required")
	}
	workingDir = path.Clean(workingDir)
	if !path.IsAbs(workingDir) {
		return nil, fmt.Errorf("sandbox: working directory %q is not an absolute process path", workingDir)
	}

	normalized := make([]Mount, 0, len(mounts))
	closeNormalized := func() {
		for _, mount := range normalized {
			if mount.root != nil {
				_ = mount.root.Close()
			}
		}
	}
	seen := make(map[string]struct{}, len(mounts))
	for _, mount := range mounts {
		if mount.HostPath == "" || mount.SandboxPath == "" {
			closeNormalized()
			return nil, errors.New("sandbox: mount source and process path are required")
		}
		source, err := filepath.Abs(mount.HostPath)
		if err != nil {
			closeNormalized()
			return nil, fmt.Errorf("sandbox: resolve mount source: %w", err)
		}
		mount.HostPath = filepath.Clean(source)
		physicalPath, err := filepath.EvalSymlinks(mount.HostPath)
		if err != nil {
			closeNormalized()
			return nil, fmt.Errorf("sandbox: resolve mount source for %q: %w", mount.SandboxPath, err)
		}
		mount.physicalPath = filepath.Clean(physicalPath)
		mount.SandboxPath = path.Clean(mount.SandboxPath)
		if !path.IsAbs(mount.SandboxPath) {
			closeNormalized()
			return nil, fmt.Errorf("sandbox: mount path %q is not an absolute process path", mount.SandboxPath)
		}
		// Direct-execution providers may opt in to a second spelling for the
		// same process-visible root (notably macOS /var -> /private/var). The
		// opt-in is deliberately provider-private: remapping providers such as
		// Docker must never turn a physical mount source into an upper-layer
		// coordinate merely because it can be resolved in the host namespace.
		if mount.ResolveSymlinkAliases {
			alias := path.Clean(filepath.ToSlash(mount.physicalPath))
			if path.IsAbs(alias) && alias != mount.SandboxPath {
				if pathsOverlap(alias, mount.SandboxPath, pkgsandbox.POSIXPathRelative) {
					closeNormalized()
					return nil, fmt.Errorf("sandbox: process mount %q overlaps its symlink alias %q", mount.SandboxPath, alias)
				}
				mount.processAliases = []string{alias}
			}
		}
		if _, ok := seen[mount.SandboxPath]; ok {
			closeNormalized()
			return nil, fmt.Errorf("sandbox: duplicate process mount %q", mount.SandboxPath)
		}
		seen[mount.SandboxPath] = struct{}{}
		root, err := os.OpenRoot(mount.HostPath)
		if err != nil {
			closeNormalized()
			return nil, fmt.Errorf("sandbox: open mount source for %q: %w", mount.SandboxPath, err)
		}
		mount.root = root
		mountInfo, err := root.Stat(".")
		if err != nil {
			_ = root.Close()
			closeNormalized()
			return nil, fmt.Errorf("sandbox: inspect mount source for %q: %w", mount.SandboxPath, err)
		}
		physicalInfo, err := os.Stat(mount.physicalPath)
		if err != nil {
			_ = root.Close()
			closeNormalized()
			return nil, fmt.Errorf("sandbox: mount source for %q changed while opening: %w", mount.SandboxPath, err)
		}
		if !physicalInfo.IsDir() || !os.SameFile(mountInfo, physicalInfo) {
			_ = root.Close()
			closeNormalized()
			return nil, fmt.Errorf("sandbox: mount source for %q changed while opening", mount.SandboxPath)
		}
		for _, existing := range normalized {
			existingInfo, statErr := existing.root.Stat(".")
			if statErr != nil {
				_ = root.Close()
				closeNormalized()
				return nil, fmt.Errorf("sandbox: inspect mount source for %q: %w", existing.SandboxPath, statErr)
			}
			switch {
			case processCoordinatesOverlap(existing, mount):
				_ = root.Close()
				closeNormalized()
				return nil, fmt.Errorf("sandbox: process mounts %q and %q overlap", existing.SandboxPath, mount.SandboxPath)
			case os.SameFile(existingInfo, mountInfo), filesystemPathsOverlap(existing.physicalPath, mount.physicalPath):
				_ = root.Close()
				closeNormalized()
				return nil, fmt.Errorf("sandbox: physical mount sources for %q and %q overlap", existing.SandboxPath, mount.SandboxPath)
			}
		}
		normalized = append(normalized, mount)
	}
	resolver := &Resolver{workingDir: workingDir, mounts: normalized}
	if _, err := resolver.Resolve(workingDir, false); err != nil {
		_ = resolver.Close()
		return nil, fmt.Errorf("sandbox: working directory is outside mount plan: %w", err)
	}
	if err := resolver.ValidateBackingPaths(); err != nil {
		_ = resolver.Close()
		return nil, err
	}
	return resolver, nil
}

// ValidateBackingPaths verifies that every physical pathname still names the
// directory capability pinned when the resolver was created. Providers call
// this as a consistency preflight before process setup; it fails closed on
// ordinary deletion or replacement, but is not an atomic defense against a
// hostile host actor concurrently mutating provider-owned mount source paths.
func (r *Resolver) ValidateBackingPaths() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return errors.New("sandbox: filesystem capability is closed")
	}
	for _, mount := range r.mounts {
		if mount.root == nil {
			return errors.New("sandbox: filesystem capability is closed")
		}
		pinned, err := mount.root.Stat(".")
		if err != nil {
			return fmt.Errorf("sandbox: inspect pinned mount %q: %w", mount.SandboxPath, err)
		}
		current, err := os.Stat(mount.HostPath)
		if err != nil {
			return fmt.Errorf("sandbox: inspect mount source for %q: %w", mount.SandboxPath, err)
		}
		if !current.IsDir() || !os.SameFile(pinned, current) {
			return fmt.Errorf("sandbox: mount source for %q was replaced", mount.SandboxPath)
		}
	}
	return nil
}

// Close releases the pinned directory capabilities. Provider Sessions call it
// exactly once when their filesystem view closes.
func (r *Resolver) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.closed = true
		for _, mount := range r.mounts {
			if mount.root != nil {
				r.closeErr = errors.Join(r.closeErr, mount.root.Close())
			}
		}
	})
	return r.closeErr
}

// Resolve maps canonical process coordinates and provider-declared
// same-namespace symlink aliases. Physical paths supplied by upper layers are
// never accepted as an alternate coordinate system by remapping providers.
func (r *Resolver) Resolve(name string, write bool) (Resolved, error) {
	if name == "" {
		return Resolved{}, errors.New("sandbox: path is required")
	}
	candidate := name
	if !path.IsAbs(candidate) {
		candidate = path.Join(r.workingDir, candidate)
	}
	candidate = path.Clean(candidate)

	best := -1
	bestRelative := ""
	bestRootLength := -1
	for index, mount := range r.mounts {
		relative, ok := pkgsandbox.POSIXPathRelative(mount.SandboxPath, candidate)
		if !ok {
			continue
		}
		if len(mount.SandboxPath) > bestRootLength {
			best, bestRelative = index, relative
			bestRootLength = len(mount.SandboxPath)
		}
	}
	if best < 0 {
		for index, mount := range r.mounts {
			for _, alias := range mount.processAliases {
				relative, ok := pkgsandbox.POSIXPathRelative(alias, candidate)
				if !ok || len(alias) <= bestRootLength {
					continue
				}
				best, bestRelative = index, relative
				bestRootLength = len(alias)
			}
		}
	}
	if best < 0 {
		return Resolved{}, fmt.Errorf("sandbox: path %q is outside process mount plan", name)
	}
	mount := r.mounts[best]
	if write && mount.ReadOnly {
		return Resolved{}, fmt.Errorf("sandbox: path %q is in a read-only mount", name)
	}
	return Resolved{Mount: mount, Relative: bestRelative, SandboxPath: candidate}, nil
}

// ResolveDirectory validates a process cwd through the same rooted capability
// used by FileAccess. It rejects missing paths, non-directories, and symlink
// traversal outside the selected mount before process setup. The provider's
// process view remains the final cwd authority: this preflight does not make a
// writable directory name immutable against concurrent agent changes.
func (r *Resolver) ResolveDirectory(name string) (Resolved, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return Resolved{}, errors.New("sandbox: filesystem capability is closed")
	}
	resolved, err := r.Resolve(name, false)
	if err != nil {
		return Resolved{}, err
	}
	if resolved.Mount.root == nil {
		return Resolved{}, errors.New("sandbox: filesystem capability is closed")
	}
	directory, err := resolved.Mount.root.OpenRoot(filepath.FromSlash(resolved.Relative))
	if err != nil {
		return Resolved{}, err
	}
	if err := directory.Close(); err != nil {
		return Resolved{}, err
	}
	return resolved, nil
}

// PolicyMounts projects a provider-private mount plan into its public,
// process-visible authorized data view.
func PolicyMounts(mounts []Mount) []pkgsandbox.Mount {
	out := make([]pkgsandbox.Mount, 0, len(mounts))
	for _, mount := range mounts {
		access := pkgsandbox.MountReadWrite
		if mount.ReadOnly {
			access = pkgsandbox.MountReadOnly
		}
		out = append(out, pkgsandbox.Mount{SandboxPath: mount.SandboxPath, Access: access})
	}
	return out
}

// Access implements sandbox.FileAccess through rooted capabilities. Path
// validation never produces a host string that a later os.* call can race.
type Access struct {
	resolver *Resolver
	tempDir  string
	mu       sync.Mutex
}

func NewAccess(resolver *Resolver) *Access { return &Access{resolver: resolver} }

func NewAccessWithTempDir(resolver *Resolver, tempDir string) *Access {
	return &Access{resolver: resolver, tempDir: path.Clean(tempDir)}
}

func (a *Access) withRoot(name string, write bool, fn func(*os.Root, string, Resolved) error) error {
	a.resolver.mu.RLock()
	defer a.resolver.mu.RUnlock()
	if a.resolver.closed {
		return errors.New("sandbox: filesystem capability is closed")
	}
	resolved, err := a.resolver.Resolve(name, write)
	if err != nil {
		return err
	}
	if resolved.Mount.root == nil {
		return errors.New("sandbox: filesystem capability is closed")
	}
	return fn(resolved.Mount.root, filepath.FromSlash(resolved.Relative), resolved)
}

func pathsOverlap(a, b string, relative func(string, string) (string, bool)) bool {
	_, aContainsB := relative(a, b)
	_, bContainsA := relative(b, a)
	return aContainsB || bContainsA
}

func filesystemPathsOverlap(a, b string) bool {
	relative := func(root, candidate string) (string, bool) {
		rel, err := filepath.Rel(root, candidate)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", false
		}
		return rel, true
	}
	return pathsOverlap(a, b, relative)
}

func processCoordinatesOverlap(a, b Mount) bool {
	aCoordinates := append([]string{a.SandboxPath}, a.processAliases...)
	bCoordinates := append([]string{b.SandboxPath}, b.processAliases...)
	for _, aCoordinate := range aCoordinates {
		for _, bCoordinate := range bCoordinates {
			if pathsOverlap(aCoordinate, bCoordinate, pkgsandbox.POSIXPathRelative) {
				return true
			}
		}
	}
	return false
}

func (a *Access) ReadFile(name string) ([]byte, error) {
	var content []byte
	err := a.withRoot(name, false, func(root *os.Root, relative string, _ Resolved) error {
		var err error
		content, err = root.ReadFile(relative)
		return err
	})
	return content, err
}

func (a *Access) ReadDir(name string) ([]pkgsandbox.DirEntry, error) {
	var out []pkgsandbox.DirEntry
	err := a.withRoot(name, false, func(root *os.Root, relative string, _ Resolved) error {
		directory, err := root.Open(relative)
		if err != nil {
			return err
		}
		entries, readErr := directory.ReadDir(-1)
		closeErr := directory.Close()
		if readErr != nil || closeErr != nil {
			return errors.Join(readErr, closeErr)
		}
		out = make([]pkgsandbox.DirEntry, 0, len(entries))
		for _, entry := range entries {
			info, err := entry.Info()
			if err == nil {
				out = append(out, pkgsandbox.DirEntry{Name: entry.Name(), IsDir: entry.IsDir(), Size: info.Size()})
			}
		}
		return nil
	})
	return out, err
}

func (a *Access) Stat(name string) (pkgsandbox.FileInfo, error) {
	var out pkgsandbox.FileInfo
	err := a.withRoot(name, false, func(root *os.Root, relative string, _ Resolved) error {
		info, err := root.Stat(relative)
		if err == nil {
			out = pkgsandbox.FileInfo{IsDir: info.IsDir(), Size: info.Size()}
		}
		return err
	})
	return out, err
}

func (a *Access) WriteFile(name string, content []byte, mode fs.FileMode) error {
	return a.withRoot(name, true, func(root *os.Root, relative string, _ Resolved) error {
		if parent := filepath.Dir(relative); parent != "." {
			if err := root.MkdirAll(parent, 0o755); err != nil {
				return err
			}
		}
		return root.WriteFile(relative, content, mode)
	})
}

func (a *Access) ProjectFiles(name string, files []pkgsandbox.ProjectedFile) error {
	validated, err := validateProjectedFiles(files)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.withRoot(name, true, func(root *os.Root, target string, resolved Resolved) error {
		for _, mount := range a.resolver.mounts {
			relative, within := pkgsandbox.POSIXPathRelative(resolved.SandboxPath, mount.SandboxPath)
			if within && relative != "." {
				return fmt.Errorf("%w: projection %q crosses process mount %q", pkgsandbox.ErrProjectionConflict, resolved.SandboxPath, mount.SandboxPath)
			}
		}
		if verifyErr := verifyProjectedFiles(root, target, validated); verifyErr == nil {
			return nil
		} else if !errors.Is(verifyErr, fs.ErrNotExist) || errors.Is(verifyErr, pkgsandbox.ErrProjectionConflict) {
			return verifyErr
		}

		parent := filepath.Dir(target)
		if err := root.MkdirAll(parent, 0o700); err != nil {
			return err
		}
		var entropy [12]byte
		if _, err := rand.Read(entropy[:]); err != nil {
			return err
		}
		stage := filepath.Join(parent, ".stella-project-"+hex.EncodeToString(entropy[:]))
		if err := root.Mkdir(stage, 0o700); err != nil {
			return err
		}
		published := false
		defer func() {
			if !published {
				_ = root.RemoveAll(stage)
			}
		}()
		stageRoot, err := root.OpenRoot(stage)
		if err != nil {
			return err
		}
		for _, file := range validated {
			filename := filepath.FromSlash(file.Path)
			if directory := filepath.Dir(filename); directory != "." {
				if err := stageRoot.MkdirAll(directory, 0o700); err != nil {
					_ = stageRoot.Close()
					return err
				}
			}
			out, err := stageRoot.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, file.Mode)
			if err != nil {
				_ = stageRoot.Close()
				return err
			}
			_, writeErr := out.Write(file.Content)
			if writeErr == nil {
				writeErr = out.Chmod(file.Mode)
			}
			closeErr := out.Close()
			if writeErr != nil || closeErr != nil {
				_ = stageRoot.Close()
				return errors.Join(writeErr, closeErr)
			}
		}
		if err := stageRoot.Close(); err != nil {
			return err
		}
		if err := renameRootNoReplace(root, stage, target); err != nil {
			if verifyErr := verifyProjectedFiles(root, target, validated); verifyErr != nil {
				return errors.Join(err, verifyErr)
			}
			if removeErr := root.RemoveAll(stage); removeErr != nil {
				return errors.Join(err, removeErr)
			}
		}
		if err := verifyProjectedFiles(root, target, validated); err != nil {
			return err
		}
		published = true
		return nil
	})
}

func (a *Access) ProjectTempFiles(name string, files []pkgsandbox.ProjectedFile) (string, error) {
	if a.tempDir == "." || !path.IsAbs(a.tempDir) {
		return "", errors.New("sandbox: Session has no process-visible temporary root")
	}
	if !fs.ValidPath(name) || name == "." {
		return "", fmt.Errorf("sandbox: invalid temporary projection path %q", name)
	}
	visible := path.Join(a.tempDir, name)
	if err := a.ProjectFiles(visible, files); err != nil {
		return "", err
	}
	return visible, nil
}

func validateProjectedFiles(files []pkgsandbox.ProjectedFile) ([]pkgsandbox.ProjectedFile, error) {
	out := append([]pkgsandbox.ProjectedFile(nil), files...)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	seen := make(map[string]struct{}, len(out))
	for _, file := range out {
		if !fs.ValidPath(file.Path) || file.Path == "." || file.Mode&fs.ModeType != 0 {
			return nil, fmt.Errorf("sandbox: invalid projection file %q", file.Path)
		}
		if _, ok := seen[file.Path]; ok {
			return nil, fmt.Errorf("sandbox: duplicate projection file %q", file.Path)
		}
		for parent := path.Dir(file.Path); parent != "."; parent = path.Dir(parent) {
			if _, ok := seen[parent]; ok {
				return nil, fmt.Errorf("sandbox: projection file %q conflicts with directory", parent)
			}
		}
		seen[file.Path] = struct{}{}
	}
	return out, nil
}

func verifyProjectedFiles(root *os.Root, target string, files []pkgsandbox.ProjectedFile) error {
	info, err := root.Lstat(target)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%w: target is not a directory", pkgsandbox.ErrProjectionConflict)
	}
	targetRoot, err := root.OpenRoot(target)
	if err != nil {
		return errors.Join(pkgsandbox.ErrProjectionConflict, err)
	}
	defer targetRoot.Close() //nolint:errcheck
	openedInfo, err := targetRoot.Stat(".")
	if err != nil || !os.SameFile(info, openedInfo) {
		return errors.Join(pkgsandbox.ErrProjectionConflict, err)
	}

	type expectedEntry struct {
		directory bool
		file      *pkgsandbox.ProjectedFile
	}
	expected := map[string]map[string]expectedEntry{".": {}}
	for index := range files {
		file := &files[index]
		parts := strings.Split(file.Path, "/")
		parent := "."
		for partIndex, part := range parts {
			if partIndex == len(parts)-1 {
				expected[parent][part] = expectedEntry{file: file}
				continue
			}
			expected[parent][part] = expectedEntry{directory: true}
			parent = path.Join(parent, part)
			if expected[parent] == nil {
				expected[parent] = map[string]expectedEntry{}
			}
		}
	}
	for relative, wanted := range expected {
		directory, openErr := targetRoot.Open(filepath.FromSlash(relative))
		if openErr != nil {
			return errors.Join(pkgsandbox.ErrProjectionConflict, openErr)
		}
		entries, readErr := directory.ReadDir(len(wanted) + 1)
		closeErr := directory.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil {
			return errors.Join(pkgsandbox.ErrProjectionConflict, readErr, closeErr)
		}
		if len(entries) != len(wanted) {
			return fmt.Errorf("%w: directory %q has unexpected entries", pkgsandbox.ErrProjectionConflict, relative)
		}
		for _, entry := range entries {
			want, ok := wanted[entry.Name()]
			if !ok {
				return fmt.Errorf("%w: unexpected entry %q", pkgsandbox.ErrProjectionConflict, path.Join(relative, entry.Name()))
			}
			entryName := path.Join(relative, entry.Name())
			entryInfo, statErr := targetRoot.Lstat(filepath.FromSlash(entryName))
			if statErr != nil {
				return errors.Join(pkgsandbox.ErrProjectionConflict, statErr)
			}
			if want.directory {
				if !entryInfo.IsDir() || entryInfo.Mode()&fs.ModeSymlink != 0 {
					return fmt.Errorf("%w: entry %q is not a directory", pkgsandbox.ErrProjectionConflict, entryName)
				}
				continue
			}
			if want.file == nil || !entryInfo.Mode().IsRegular() || entryInfo.Mode().Perm() != want.file.Mode.Perm() || entryInfo.Size() != int64(len(want.file.Content)) {
				return fmt.Errorf("%w: file metadata differs for %q", pkgsandbox.ErrProjectionConflict, entryName)
			}
			in, openErr := targetRoot.Open(filepath.FromSlash(entryName))
			if openErr != nil {
				return errors.Join(pkgsandbox.ErrProjectionConflict, openErr)
			}
			openedFileInfo, statErr := in.Stat()
			if statErr != nil || !os.SameFile(entryInfo, openedFileInfo) {
				_ = in.Close()
				return errors.Join(pkgsandbox.ErrProjectionConflict, statErr)
			}
			content, readErr := io.ReadAll(io.LimitReader(in, int64(len(want.file.Content))+1))
			closeErr := in.Close()
			if readErr != nil || closeErr != nil || !bytes.Equal(content, want.file.Content) {
				return errors.Join(pkgsandbox.ErrProjectionConflict, readErr, closeErr)
			}
		}
	}
	finalInfo, err := root.Lstat(target)
	if err != nil || finalInfo.Mode()&fs.ModeSymlink != 0 || !os.SameFile(info, finalInfo) {
		return errors.Join(pkgsandbox.ErrProjectionConflict, err)
	}
	return nil
}
