// Package fsops implements contained filesystem operations shared by trusted
// in-process adapters and the restricted stella-fs helper.
package fsops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

// Root confines operations to one provider-authorized mount. It relies on
// os.Root for the security property rather than a racy pre-check: ordinary
// POSIX semantics hold, symlinks that resolve within the mount are followed,
// and any path — symlink or not — that would escape the mount fails closed.
// Stricter contained-relative-link rules (e.g. managed Skill publication) are a
// later dedicated module, not this generic boundary.
type Root struct {
	root                           *os.Root
	syncRootDirectory              func(*os.File) error
	afterManagedSkillTemporaryLink func(string)
	afterManagedSkillRename        func()
}

// Mount binds one canonical sandbox root to a provider-authorized directory.
// Directory is consumed here and never exposed by the Filesystem interface.
type Mount struct {
	Path      string
	Directory string
	ReadOnly  bool
}

// Filesystem dispatches canonical sandbox paths to contained mount Roots.
type (
	Filesystem  struct{ mounts []mountedRoot }
	mountedRoot struct {
		path     string
		root     *Root
		readOnly bool
	}
)

func NewFilesystem(mounts []Mount) (*Filesystem, error) {
	if len(mounts) == 0 {
		return nil, errors.New("fsops: at least one mount is required")
	}
	f := &Filesystem{mounts: make([]mountedRoot, 0, len(mounts))}
	seen := make(map[string]struct{}, len(mounts))
	for _, mount := range mounts {
		if !canonicalMount(mount.Path) || mount.Directory == "" {
			// Roll back roots already opened; the validation error is primary.
			_ = f.Close()
			return nil, errors.New("fsops: invalid mount")
		}
		if _, duplicate := seen[mount.Path]; duplicate {
			_ = f.Close()
			return nil, fmt.Errorf("fsops: duplicate mount %q", mount.Path)
		}
		seen[mount.Path] = struct{}{}
		r, err := Open(mount.Directory)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		f.mounts = append(f.mounts, mountedRoot{path: mount.Path, root: r, readOnly: mount.ReadOnly})
	}
	return f, nil
}

func canonicalMount(p string) bool {
	return p == sandbox.PathWorkspace || p == sandbox.PathUser || p == sandbox.PathTemp
}

func (f *Filesystem) Close() error {
	var errs []error
	for _, mount := range f.mounts {
		errs = append(errs, mount.root.Close())
	}
	return errors.Join(errs...)
}

func (f *Filesystem) Read(ctx context.Context, p string, o sandbox.ReadOptions) (io.ReadCloser, sandbox.FileInfo, error) {
	m, rel, err := f.mount(p, false)
	if err != nil {
		return nil, sandbox.FileInfo{}, err
	}
	return m.root.Read(ctx, rel, o)
}

func (f *Filesystem) Stat(ctx context.Context, p string) (sandbox.FileInfo, error) {
	m, rel, err := f.mount(p, false)
	if err != nil {
		return sandbox.FileInfo{}, err
	}
	return m.root.Stat(ctx, rel)
}

func (f *Filesystem) List(ctx context.Context, p string) ([]sandbox.DirEntry, error) {
	m, rel, err := f.mount(p, false)
	if err != nil {
		return nil, err
	}
	return m.root.List(ctx, rel)
}

// InspectManagedSkillTarget is the only managed-link inspection exposed through
// the provider-neutral boundary. It accepts a direct canonical entry path, not
// an arbitrary link target.
func (f *Filesystem) InspectManagedSkillTarget(ctx context.Context, p string) (sandbox.ManagedSkillTarget, error) {
	m, rel, err := f.mount(p, false)
	if err != nil {
		return sandbox.ManagedSkillTarget{}, err
	}
	parent, name := path.Dir(rel), path.Base(rel)
	if rel == "." || name == "." {
		return sandbox.ManagedSkillTarget{}, fmt.Errorf("fsops: managed skill target %q is not a direct entry", p)
	}
	digest, managed, err := m.root.ManagedSkillTargetAt(ctx, parent, name)
	if err != nil {
		return sandbox.ManagedSkillTarget{}, err
	}
	return sandbox.ManagedSkillTarget{Digest: digest, Managed: managed}, nil
}

func (f *Filesystem) Mkdir(ctx context.Context, p string, perm fs.FileMode) error {
	m, rel, err := f.mount(p, true)
	if err != nil {
		return err
	}
	return m.root.Mkdir(ctx, rel, perm)
}

func (f *Filesystem) Remove(ctx context.Context, p string, recursive bool) error {
	m, rel, err := f.mount(p, true)
	if err != nil {
		return err
	}
	return m.root.Remove(ctx, rel, recursive)
}

func (f *Filesystem) Rename(ctx context.Context, oldPath, newPath string) error {
	oldMount, oldRel, err := f.mount(oldPath, true)
	if err != nil {
		return err
	}
	newMount, newRel, err := f.mount(newPath, true)
	if err != nil {
		return err
	}
	if oldMount != newMount {
		return errors.New("fsops: cross-mount rename is not supported")
	}
	return oldMount.root.Rename(ctx, oldRel, newRel)
}

func (f *Filesystem) Write(ctx context.Context, p string, r io.Reader, o sandbox.WriteOptions) error {
	m, rel, err := f.mount(p, true)
	if err != nil {
		return err
	}
	return m.root.Write(ctx, rel, r, o)
}

func (f *Filesystem) Upload(ctx context.Context, p string, r io.Reader, o sandbox.WriteOptions) error {
	return f.Write(ctx, p, r, o)
}

func (f *Filesystem) mount(p string, write bool) (*mountedRoot, string, error) {
	if !strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return nil, "", fmt.Errorf("fsops: path %q is not canonical", p)
	}
	var best *mountedRoot
	for i := range f.mounts {
		mount := &f.mounts[i]
		if p == mount.path || strings.HasPrefix(p, mount.path+"/") {
			if best == nil || len(mount.path) > len(best.path) {
				best = mount
			}
		}
	}
	if best == nil {
		return nil, "", fmt.Errorf("fsops: path %q is outside mounted roots", p)
	}
	if write && best.readOnly {
		return nil, "", fmt.Errorf("fsops: path %q is read-only", p)
	}
	return best, strings.TrimPrefix(strings.TrimPrefix(p, best.path), "/"), nil
}

func Open(dir string) (*Root, error) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("fsops: open root: %w", err)
	}
	return &Root{root: r}, nil
}

func (r *Root) Close() error { return r.root.Close() }

func (r *Root) Read(ctx context.Context, name string, opts sandbox.ReadOptions) (io.ReadCloser, sandbox.FileInfo, error) {
	if opts.MaxBytes <= 0 {
		return nil, sandbox.FileInfo{}, errors.New("fsops: read limit must be positive")
	}
	if err := ctx.Err(); err != nil {
		return nil, sandbox.FileInfo{}, err
	}
	info, err := r.root.Stat(clean(name))
	if err != nil {
		return nil, sandbox.FileInfo{}, fmt.Errorf("fsops: stat: %w", err)
	}
	if info.IsDir() {
		return nil, sandbox.FileInfo{}, errors.New("fsops: cannot read directory")
	}
	f, err := r.root.Open(clean(name))
	if err != nil {
		return nil, sandbox.FileInfo{}, fmt.Errorf("fsops: open: %w", err)
	}
	return &limitedReadCloser{ReadCloser: f, remaining: opts.MaxBytes}, fileInfo(info), nil
}

func (r *Root) Write(ctx context.Context, name string, src io.Reader, opts sandbox.WriteOptions) error {
	if src == nil {
		return errors.New("fsops: write source is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	perm := opts.Perm.Perm()
	if perm == 0 {
		perm = 0o644
	}
	f, err := r.root.OpenFile(clean(name), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("fsops: open write: %w", err)
	}
	_, copyErr := copyContext(ctx, f, src)
	closeErr := f.Close()
	if copyErr != nil {
		return fmt.Errorf("fsops: write: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("fsops: close write: %w", closeErr)
	}
	return nil
}

func (r *Root) Upload(ctx context.Context, name string, src io.Reader, opts sandbox.WriteOptions) error {
	return r.Write(ctx, name, src, opts)
}

func (r *Root) Stat(ctx context.Context, name string) (sandbox.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return sandbox.FileInfo{}, err
	}
	info, err := r.root.Stat(clean(name))
	if err != nil {
		return sandbox.FileInfo{}, fmt.Errorf("fsops: stat: %w", err)
	}
	return fileInfo(info), nil
}

func (r *Root) List(ctx context.Context, name string) ([]sandbox.DirEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, err := r.root.Open(clean(name))
	if err != nil {
		return nil, fmt.Errorf("fsops: open directory: %w", err)
	}
	defer func() { _ = d.Close() }() // read-only handle; close error cannot affect the listing
	entries, err := d.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("fsops: list: %w", err)
	}
	out := make([]sandbox.DirEntry, 0, len(entries))
	for _, entry := range entries {
		// Report entries as-is, describing symlinks by their own metadata (never
		// the target). os.Root guarantees any later access through an escaping
		// link fails closed; ordinary POSIX listing does not resolve entries.
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("fsops: entry %q: %w", entry.Name(), err)
		}
		out = append(out, sandbox.DirEntry{Name: entry.Name(), IsDir: info.IsDir(), Size: info.Size(), Mode: info.Mode()})
	}
	return out, nil
}

func (r *Root) Mkdir(ctx context.Context, name string, perm fs.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if perm == 0 {
		perm = 0o755
	}
	if err := r.root.MkdirAll(clean(name), perm.Perm()); err != nil {
		return fmt.Errorf("fsops: mkdir: %w", err)
	}
	return nil
}

func (r *Root) Remove(ctx context.Context, name string, recursive bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if clean(name) == "." {
		return errors.New("fsops: cannot remove root")
	}
	var err error
	if recursive {
		err = r.root.RemoveAll(clean(name))
	} else {
		err = r.root.Remove(clean(name))
	}
	if err != nil {
		return fmt.Errorf("fsops: remove: %w", err)
	}
	return nil
}

func (r *Root) Rename(ctx context.Context, oldName, newName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	oldName, newName = clean(oldName), clean(newName)
	if oldName == "." || newName == "." {
		return errors.New("fsops: cannot rename root")
	}
	oldParent, err := r.root.Open(path.Dir(oldName))
	if err != nil {
		return fmt.Errorf("fsops: open rename source parent: %w", err)
	}
	defer func() { _ = oldParent.Close() }()
	newParent, err := r.root.Open(path.Dir(newName))
	if err != nil {
		return fmt.Errorf("fsops: open rename destination parent: %w", err)
	}
	defer func() { _ = newParent.Close() }()
	if err := renameNoReplace(oldParent, path.Base(oldName), newParent, path.Base(newName)); err != nil {
		return fmt.Errorf("fsops: rename: %w", err)
	}
	return nil
}

func clean(name string) string {
	if name == "" {
		return "."
	}
	return path.Clean(strings.TrimPrefix(name, "/"))
}

type limitedReadCloser struct {
	io.ReadCloser
	remaining int64
	exceeded  bool
}

func (r *limitedReadCloser) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		if r.exceeded {
			return 0, sandbox.ErrReadLimit
		}
		var one [1]byte
		n, err := r.ReadCloser.Read(one[:])
		if n > 0 {
			r.exceeded = true
			return 0, sandbox.ErrReadLimit
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.ReadCloser.Read(p)
	r.remaining -= int64(n)
	return n, err
}

func copyContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 32*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, er := src.Read(buf)
		if n > 0 {
			w, ew := dst.Write(buf[:n])
			total += int64(w)
			if ew != nil {
				return total, ew
			}
			if w != n {
				return total, io.ErrShortWrite
			}
		}
		if er == io.EOF {
			return total, nil
		}
		if er != nil {
			return total, er
		}
	}
}

func fileInfo(info fs.FileInfo) sandbox.FileInfo {
	return sandbox.FileInfo{Name: info.Name(), Size: info.Size(), Mode: info.Mode(), ModTime: info.ModTime(), IsDir: info.IsDir()}
}
