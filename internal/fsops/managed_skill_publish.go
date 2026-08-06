package fsops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

const (
	maxManagedTreeEntries       = 512
	maxManagedTreeDepth         = 16
	maxManagedFileBytes   int64 = 8 << 20
	maxManagedTreeBytes   int64 = 32 << 20
)

// PublishManagedSkillAt is the sole content mutation for managed Skills. A
// completed revision is immutable: an existing digest is accepted only after an
// exact re-verification. The stage is private to .stella-revisions/name.
func (r *Root) PublishManagedSkillAt(ctx context.Context, catalogRoot, name, want string, publication sandbox.ManagedSkillPublication) (err error) {
	if err := managedSkillPlatformSupported(); err != nil {
		return err
	}
	if !validCatalogRoot(catalogRoot) || !validManagedSkillName(name) || !validManagedSkillDigest(want) {
		return errors.New("fsops: invalid managed skill publication")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateManagedSkillPublication(publication); err != nil {
		return err
	}
	if err := r.validateManagedSkillOccupantAt(catalogRoot, name); err != nil {
		return err
	}
	parent := path.Join(catalogRoot, managedSkillRevisionsDir, name)
	if err := r.ensureRealDirectory(parent); err != nil {
		return err
	}
	if err := r.syncManagedRevisionParents(catalogRoot, name); err != nil {
		return err
	}
	final := path.Join(parent, want)
	stage, err := r.createRevisionStage(parent)
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			_ = r.root.RemoveAll(stage)
		}
	}()
	if err := r.writePublication(ctx, stage, publication); err != nil {
		return err
	}
	if err := r.syncTree(ctx, stage); err != nil {
		return err
	}
	if got, err := r.digestTree(ctx, stage); err != nil || got != want {
		if err == nil {
			err = fmt.Errorf("digest %q differs from requested digest %q", got, want)
		}
		return fmt.Errorf("fsops: verify staged managed skill revision: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.Rename(ctx, stage, final); err != nil {
		if errors.Is(err, fs.ErrExist) {
			if verifyErr := r.verifyPublishedTree(ctx, final, want); verifyErr == nil {
				return r.selectPublishedRevision(ctx, catalogRoot, name, final, want)
			}
		}
		return fmt.Errorf("fsops: publish revision %q to %q: %w", stage, final, err)
	}
	published = true
	if err := r.verifyPublishedTree(ctx, final, want); err != nil {
		return managedSkillOutcomeUnknown(err)
	}
	if err := r.syncDirectory(parent); err != nil {
		return managedSkillOutcomeUnknown(err)
	}
	return r.selectPublishedRevision(ctx, catalogRoot, name, final, want)
}

func (r *Root) selectPublishedRevision(ctx context.Context, catalogRoot, name, final, want string) error {
	if err := r.swapManagedSkillTargetAt(ctx, catalogRoot, name, want); err != nil {
		if sandbox.IsOutcomeUnknown(err) {
			return err
		}
		return managedSkillOutcomeUnknown(err)
	}
	if err := r.verifyPublishedTree(ctx, final, want); err != nil {
		return managedSkillOutcomeUnknown(err)
	}
	return nil
}

func (r *Root) syncManagedRevisionParents(catalogRoot, name string) error {
	// Sync child then parent. This persists both the revision namespace and every
	// newly-created catalog-root directory entry before a link can name it.
	dirs := []string{path.Join(catalogRoot, managedSkillRevisionsDir, name), path.Join(catalogRoot, managedSkillRevisionsDir)}
	for d := catalogRoot; ; d = path.Dir(d) {
		dirs = append(dirs, d)
		if d == "." {
			break
		}
	}
	seen := make(map[string]struct{}, len(dirs))
	for _, d := range dirs {
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		if err := r.syncDirectory(d); err != nil {
			return err
		}
	}
	return nil
}

func validCatalogRoot(p string) bool {
	if !(p == "." || (p != "" && !path.IsAbs(p) && path.Clean(p) == p && p != ".." && !strings.HasPrefix(p, "../"))) {
		return false
	}
	for _, component := range splitPathForward(p) {
		if component == managedSkillRevisionsDir || strings.HasPrefix(component, ".stella-skill-target-") || strings.HasPrefix(component, ".stage-") {
			return false
		}
	}
	return true
}

// ValidateManagedSkillPublication is the shared provider-neutral structural gate.
func ValidateManagedSkillPublication(p sandbox.ManagedSkillPublication) error {
	if len(p.Files) == 0 || len(p.Files) > 512 {
		return errors.New("fsops: managed skill publication entry limit")
	}
	var total int64
	seen := map[string]bool{}
	main, metadata := false, false
	previous := ""
	for _, f := range p.Files {
		if err := sandbox.ValidateManagedSkillTreePath(f.Path); err != nil {
			return err
		}
		if f.Path == ".stella-revisions" || len(f.Path) > len(".stella-revisions/") && f.Path[:len(".stella-revisions/")] == ".stella-revisions/" {
			return errors.New("fsops: managed skill publication uses reserved namespace")
		}
		if f.Mode&fs.ModeType != 0 || f.Length < 0 || f.Length > 8<<20 || f.Open == nil || seen[f.Path] || (previous != "" && previous >= f.Path) {
			return errors.New("fsops: invalid managed skill publication entry")
		}
		if f.Path == "SKILL.md" {
			main = f.Length > 0 && f.Mode.Perm() == 0o644
		}
		if f.Path == ".stella-skill.json" {
			metadata = f.Length > 0 && f.Mode.Perm() == 0o644
		}
		if depth := len(splitPath(f.Path)); depth > 16 {
			return errors.New("fsops: managed skill publication exceeds directory depth")
		}
		if total > 32<<20-f.Length {
			return errors.New("fsops: managed skill publication exceeds content limit")
		}
		total += f.Length
		seen[f.Path] = true
		previous = f.Path
	}
	if !main || !metadata {
		return errors.New("fsops: managed skill publication lacks canonical control files")
	}
	return nil
}

func splitPath(p string) []string {
	var out []string
	for p != "" {
		var e string
		e, p = path.Base(p), path.Dir(p)
		out = append(out, e)
		if p == "." {
			break
		}
	}
	return out
}

func (r *Root) createRevisionStage(parent string) (string, error) {
	for range 16 {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		n := path.Join(parent, ".stage-"+hex.EncodeToString(b[:]))
		if err := r.root.Mkdir(n, 0o700); err == nil {
			return n, nil
		} else if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", errors.New("fsops: allocate managed skill stage")
}

func (r *Root) ensureRealDirectory(p string) error {
	cur := "."
	for _, e := range splitPathForward(p) {
		cur = path.Join(cur, e)
		info, err := r.root.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			if err = r.root.Mkdir(cur, 0o755); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("fsops: managed skill directory %q is not real", cur)
		}
	}
	return nil
}

func splitPathForward(p string) []string {
	if p == "." {
		return nil
	}
	var out []string
	for _, e := range stringsSplit(p, "/") {
		if e != "." && e != "" {
			out = append(out, e)
		}
	}
	return out
}

func stringsSplit(s, sep string) []string {
	var out []string
	for {
		i := 0
		for i < len(s) && s[i:i+1] != sep {
			i++
		}
		out = append(out, s[:i])
		if i == len(s) {
			return out
		}
		s = s[i+1:]
	}
}

func (r *Root) writePublication(ctx context.Context, stage string, p sandbox.ManagedSkillPublication) error {
	for _, f := range p.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		parent := path.Dir(path.Join(stage, f.Path))
		if err := r.ensureRealDirectory(parent); err != nil {
			return err
		}
		src, err := f.Open()
		if err != nil {
			return err
		}
		dst, err := r.root.OpenFile(path.Join(stage, f.Path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, f.Mode.Perm())
		if err != nil {
			_ = src.Close()
			return err
		}
		if err := dst.Chmod(f.Mode.Perm()); err != nil {
			_ = src.Close()
			_ = dst.Close()
			return fmt.Errorf("fsops: chmod managed skill entry %q: %w", f.Path, err)
		}
		n, copyErr := copyContext(ctx, dst, io.LimitReader(src, f.Length+1))
		closeSrc := src.Close()
		info, statErr := dst.Stat()
		syncErr := dst.Sync()
		closeDst := dst.Close()
		if copyErr != nil || closeSrc != nil || statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != f.Mode.Perm() || info.Size() != f.Length || syncErr != nil || closeDst != nil || n != f.Length {
			return fmt.Errorf("fsops: write managed skill entry %q failed", f.Path)
		}
	}
	return nil
}

func (r *Root) digestTree(ctx context.Context, root string) (string, error) {
	entries, _, err := r.collectPublishedTree(ctx, root)
	if err != nil {
		return "", err
	}
	return sandbox.DigestManagedSkillTreeV1(entries)
}

func (r *Root) collectPublishedTree(ctx context.Context, root string) ([]sandbox.ManagedSkillTreeEntry, []string, error) {
	var out []sandbox.ManagedSkillTreeEntry
	var dirs []string
	entries, total := 0, int64(0)
	var walk func(string, int) error
	walk = func(rel string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > maxManagedTreeDepth {
			return errors.New("fsops: managed skill tree exceeds directory depth")
		}
		d := path.Join(root, rel)
		f, err := r.root.Open(d)
		if err != nil {
			return err
		}
		list, err := f.ReadDir(-1)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		for _, e := range list {
			entries++
			if entries > maxManagedTreeEntries {
				return errors.New("fsops: managed skill tree exceeds entry limit")
			}
			if e.Name() == "" || e.Name() == "." || e.Name() == ".." || path.Base(e.Name()) != e.Name() {
				return errors.New("fsops: invalid managed skill tree entry")
			}
			p := e.Name()
			if rel != "" {
				p = path.Join(rel, p)
			}
			info, err := r.root.Lstat(path.Join(root, p))
			if err != nil {
				return err
			}
			if err := sandbox.ValidateManagedSkillTreePath(p); err != nil || p == ".stella-revisions" || strings.HasPrefix(p, ".stella-revisions/") {
				return errors.New("fsops: invalid managed skill tree path")
			}
			if info.Mode()&fs.ModeSymlink != 0 {
				return errors.New("fsops: managed skill tree contains symlink")
			}
			if info.IsDir() {
				if err := walk(p, depth+1); err != nil {
					return err
				}
				continue
			}
			if info.Mode()&fs.ModeType != 0 {
				return errors.New("fsops: managed skill tree contains special file")
			}
			if info.Size() > maxManagedFileBytes || total > maxManagedTreeBytes-info.Size() {
				return errors.New("fsops: managed skill tree exceeds content limit")
			}
			total += info.Size()
			full := path.Join(root, p)
			mode, size := info.Mode(), info.Size()
			out = append(out, sandbox.ManagedSkillTreeEntry{Path: p, Mode: mode, Length: size, Open: func() (io.ReadCloser, error) {
				f, err := r.root.Open(full)
				if err != nil {
					return nil, err
				}
				current, statErr := f.Stat()
				if statErr != nil || !current.Mode().IsRegular() || current.Mode() != mode || current.Size() != size {
					_ = f.Close()
					if statErr != nil {
						return nil, statErr
					}
					return nil, errors.New("fsops: managed skill tree changed during digest")
				}
				return f, nil
			}})
		}
		dirs = append(dirs, d)
		return nil
	}
	if err := walk("", 0); err != nil {
		return nil, nil, err
	}
	return out, dirs, nil
}

func (r *Root) verifyPublishedTree(ctx context.Context, p, want string) error {
	entries, _, err := r.collectPublishedTree(ctx, p)
	if err != nil {
		return err
	}
	main, metadata := false, false
	for _, entry := range entries {
		if entry.Path == "SKILL.md" {
			main = entry.Length > 0 && entry.Mode.Perm() == 0o644
		}
		if entry.Path == ".stella-skill.json" {
			metadata = entry.Mode.Perm() == 0o644
		}
	}
	if !main || !metadata {
		return errors.New("fsops: managed skill revision lacks canonical control files")
	}
	got, err := sandbox.DigestManagedSkillTreeV1(entries)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("managed skill digest %q does not match %q", got, want)
	}
	return nil
}

func (r *Root) syncDirectory(p string) error {
	if r.syncManagedDirectory != nil {
		r.syncManagedDirectory(p)
	}
	if r.syncManagedDirectoryError != nil {
		if err := r.syncManagedDirectoryError(p); err != nil {
			return err
		}
	}
	f, err := r.root.Open(p)
	if err != nil {
		return err
	}
	err = f.Sync()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (r *Root) syncTree(ctx context.Context, p string) error {
	_, dirs, err := r.collectPublishedTree(ctx, p)
	if err != nil {
		return err
	}
	for _, d := range dirs {
		if err := r.syncDirectory(d); err != nil {
			return err
		}
	}
	return nil
}
