package home

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

// LocalStore preserves the existing local, none, and Docker-bind filesystem
// layout. Locators are Store-relative compatibility coordinates, never host
// paths; all physical operations consume the persisted locator under os.Root.
type LocalStore struct {
	id       string
	base     string
	syncFile func(*os.File) error
}

// mutableAssetDigest is intentionally package-private: callers can prove
// equality without ever receiving a host filesystem path.
type mutableAssetDigest struct {
	Size   int64
	SHA256 [sha256.Size]byte
}

// mutableAssetStore is a narrow migration-only Store facet. It cannot expose a
// host path, list a Home, or mutate arbitrary files.
type mutableAssetStore interface {
	inspectMutableAsset(context.Context, Record, string) (mutableAssetDigest, error)
	installMutableAsset(context.Context, Record, string, io.ReadCloser) (mutableAssetDigest, bool, error)
	syncMutableAsset(context.Context, Record, string) error
}

func NewLocalStore(id, base string) (*LocalStore, error) {
	if err := validateStoreID(id); err != nil {
		return nil, fmt.Errorf("home: invalid local store ID: %w", err)
	}
	if base == "" {
		return nil, errors.New("home: local store base is required")
	}
	absolute, err := filepath.Abs(base)
	if err != nil {
		return nil, fmt.Errorf("home: resolve local store base: %w", err)
	}
	return &LocalStore{id: id, base: absolute, syncFile: (*os.File).Sync}, nil
}

func (s *LocalStore) ID() string { return s.id }

func (s *LocalStore) Allocate(key Key) (string, error) {
	locator, err := s.allocateUnchecked(key)
	if err != nil {
		return "", err
	}
	return locator, s.ValidateLocator(key, locator)
}

func (s *LocalStore) ValidateLocator(key Key, locator string) error {
	if locator == "" || path.IsAbs(locator) || filepath.IsAbs(locator) || filepath.Clean(locator) != filepath.FromSlash(locator) || strings.Contains(locator, `\`) {
		return errors.New("locator must be a clean relative path")
	}
	for part := range strings.SplitSeq(locator, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("locator escapes store root")
		}
	}
	expected, err := s.allocateUnchecked(key)
	if err != nil {
		return err
	}
	if locator != expected {
		return errors.New("locator does not match Home identity")
	}
	return nil
}

func (s *LocalStore) allocateUnchecked(key Key) (string, error) {
	if err := key.Validate(); err != nil {
		return "", err
	}
	switch key.Kind {
	case PrincipalHome:
		if key.PrincipalKind == GroupPrincipal {
			return path.Join("users", "group-"+key.PrincipalID), nil
		}
		return path.Join("users", key.PrincipalID), nil
	case AgentHome:
		principal, err := s.allocateUnchecked(Principal(key.PrincipalKind, key.PrincipalID))
		if err != nil {
			return "", err
		}
		return path.Join(principal, "agents", key.AgentID), nil
	case SystemSkillRoot:
		return path.Join(".agents", "db-skills"), nil
	case SystemAgentSkillRoot:
		return path.Join("agents", key.AgentID, ".agents", "skills"), nil
	}
	return "", fmt.Errorf("home: unsupported kind %q", key.Kind)
}

func (s *LocalStore) Ensure(_ context.Context, home Record) error {
	root, err := os.OpenRoot(s.base)
	if err != nil {
		return fmt.Errorf("open store root: %w", err)
	}
	defer func() { _ = root.Close() }()
	if err := root.MkdirAll(home.Locator, 0o755); err != nil {
		return fmt.Errorf("create home: %w", err)
	}
	return nil
}

func (s *LocalStore) Purge(_ context.Context, home Record) error {
	root, err := os.OpenRoot(s.base)
	if err != nil {
		return fmt.Errorf("open store root: %w", err)
	}
	defer func() { _ = root.Close() }()
	if err := root.RemoveAll(home.Locator); err != nil {
		return fmt.Errorf("remove home: %w", err)
	}
	return nil
}

func (s *LocalStore) Attachment(home Record, readOnly bool) sandbox.HomeAttachment {
	return sandbox.HomeAttachment{HomeID: home.ID, StoreID: home.StoreID, Locator: home.Locator, ReadOnly: readOnly}
}

// pathFor is the sole Phase-1 local compatibility projection. It validates the
// persisted relative locator before resolving it beneath this configured root.
func (s *LocalStore) pathFor(home Record) (string, error) {
	if home.StoreID != s.id {
		return "", errors.New("home belongs to another Store")
	}
	if err := s.ValidateLocator(home.Key, home.Locator); err != nil {
		return "", fmt.Errorf("invalid persisted locator: %w", err)
	}
	return filepath.Join(s.base, filepath.FromSlash(home.Locator)), nil
}

// PrepareWorkspace creates the legacy workspace shape through an os.Root. A
// symlink beneath a Home can therefore never redirect writes outside the Store.
// WorkspaceView calls it before opening its short DB revalidation transaction.
func (s *LocalStore) PrepareWorkspace(principal, agent Record) error {
	if _, _, _, err := s.WorkspacePaths(principal, agent); err != nil {
		return err
	}
	root, err := os.OpenRoot(s.base)
	if err != nil {
		return fmt.Errorf("open store root: %w", err)
	}
	defer func() { _ = root.Close() }()
	for _, dir := range []string{
		principal.Locator,
		path.Join(principal.Locator, "data"),
		path.Join(principal.Locator, "data", ".cache"),
		path.Join(principal.Locator, "data", ".agents", "skills"),
		path.Join(principal.Locator, "data", ".agents", "delegates"),
		path.Join(principal.Locator, "data", "assets"),
		agent.Locator,
	} {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("materialize workspace %q: %w", dir, err)
		}
	}
	return nil
}

// WorkspacePaths validates and projects an already-prepared workspace without
// filesystem I/O, so it is safe inside the final DB revalidation phase.
func (s *LocalStore) WorkspacePaths(principal, agent Record) (string, string, string, error) {
	principalRoot, err := s.pathFor(principal)
	if err != nil {
		return "", "", "", err
	}
	agentRoot, err := s.pathFor(agent)
	if err != nil {
		return "", "", "", err
	}
	return principalRoot, filepath.Join(principalRoot, "data"), agentRoot, nil
}

// LegacyAgentIDs inspects only the agents child of an already-authoritative
// principal Home. It never derives principals from directory names.
func (s *LocalStore) LegacyAgentIDs(key Key) ([]string, error) {
	if key.Kind != PrincipalHome {
		return nil, errors.New("home: legacy agent parent must be a principal")
	}
	locator, err := s.Allocate(key)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(s.base)
	if err != nil {
		return nil, fmt.Errorf("open store root: %w", err)
	}
	defer func() { _ = root.Close() }()
	dir, err := root.Open(path.Join(locator, "agents"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open legacy agent directory: %w", err)
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("read legacy agent directory: %w", err)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			continue
		}
		ids = append(ids, entry.Name())
	}
	return ids, nil
}

// inspectMutableAsset hashes one validated relative asset path beneath a typed
// PrincipalHome. It is used by migration verification only.
func (s *LocalStore) inspectMutableAsset(ctx context.Context, home Record, relative string) (mutableAssetDigest, error) {
	root, name, err := s.mutableAssetRoot(home, relative)
	if err != nil {
		return mutableAssetDigest{}, err
	}
	defer func() { _ = root.Close() }()
	return digestRootFile(ctx, root, name)
}

// installMutableAsset streams source to a same-directory temporary file, then
// publishes it with Link rather than Rename. A concurrent local winner is
// accepted only if it proves byte-for-byte identical.
func (s *LocalStore) installMutableAsset(ctx context.Context, home Record, relative string, source io.ReadCloser) (digest mutableAssetDigest, published bool, err error) {
	if source == nil {
		return digest, false, errors.New("home: migration source is required")
	}
	root, name, err := s.mutableAssetRoot(home, relative)
	if err != nil {
		return digest, false, errors.Join(err, source.Close())
	}
	defer func() { _ = root.Close() }()
	dir := path.Dir(name)
	if err := root.MkdirAll(dir, 0o755); err != nil {
		return digest, false, errors.Join(fmt.Errorf("create asset directory: %w", err), source.Close())
	}
	tmp, tmpName, err := createLocalAssetTemp(root, dir)
	if err != nil {
		return digest, false, errors.Join(err, source.Close())
	}
	defer func() {
		if tmpName != "" {
			_ = root.Remove(tmpName)
		}
	}()
	digest, err = copyDigest(ctx, tmp, source)
	closeSourceErr := source.Close()
	if err != nil {
		_ = tmp.Close()
		return digest, false, errors.Join(err, closeSourceErr)
	}
	if closeSourceErr != nil {
		_ = tmp.Close()
		return digest, false, fmt.Errorf("close migration source: %w", closeSourceErr)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return digest, false, fmt.Errorf("chmod migration temp: %w", err)
	}
	if err := s.sync(tmp); err != nil {
		_ = tmp.Close()
		return digest, false, fmt.Errorf("sync migration temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return digest, false, fmt.Errorf("close migration temp: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return digest, false, err
	}
	if existing, statErr := root.Lstat(name); statErr == nil {
		if !existing.Mode().IsRegular() {
			return digest, false, fmt.Errorf("migration destination is not a regular file")
		}
		target, err := digestRootFile(ctx, root, name)
		if err != nil {
			return digest, false, err
		}
		if target != digest {
			return digest, false, fmt.Errorf("migration destination differs from source")
		}
		return digest, false, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return digest, false, fmt.Errorf("inspect migration destination: %w", statErr)
	}
	if err := root.Link(tmpName, name); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return digest, false, fmt.Errorf("publish migration asset: %w", err)
		}
		target, readErr := digestRootFile(ctx, root, name)
		if readErr != nil {
			return digest, false, readErr
		}
		if target != digest {
			return digest, false, fmt.Errorf("concurrent migration destination differs from source")
		}
		return digest, false, nil
	}
	published = true
	if err := root.Remove(tmpName); err != nil {
		return digest, true, fmt.Errorf("remove published migration temp: %w", err)
	}
	tmpName = ""
	if err := ctx.Err(); err != nil {
		return digest, true, fmt.Errorf("%w: migration asset published: %w", sandbox.ErrOutcomeUnknown, err)
	}
	return digest, true, nil
}

// syncMutableAsset establishes durability for a verified target. It syncs the
// inode and each parent from its destination directory through the typed Home
// root, which persists both Link publication and any newly-created directories.
func (s *LocalStore) syncMutableAsset(ctx context.Context, home Record, relative string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, name, err := s.mutableAssetRoot(home, relative)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	syncErr := s.sync(file)
	closeErr := file.Close()
	if syncErr != nil || closeErr != nil {
		return errors.Join(syncErr, closeErr)
	}
	for dir := path.Dir(name); ; dir = path.Dir(dir) {
		if err := ctx.Err(); err != nil {
			return err
		}
		folder, err := root.Open(dir)
		if err != nil {
			return err
		}
		syncErr := s.sync(folder)
		closeErr := folder.Close()
		if syncErr != nil || closeErr != nil {
			return errors.Join(syncErr, closeErr)
		}
		if dir == "." {
			return nil
		}
	}
}

// writeInboundAsset uses the same no-replace, durable contained publication as
// the migration writer. It is only reachable through Registry.WriteInboundAsset.
func (s *LocalStore) writeInboundAsset(ctx context.Context, home Record, relative string, content []byte) error {
	_, published, err := s.installMutableAsset(ctx, home, relative, io.NopCloser(bytes.NewReader(content)))
	if err != nil {
		if published {
			return fmt.Errorf("%w: inbound asset published: %w", sandbox.ErrOutcomeUnknown, err)
		}
		return err
	}
	if err := s.syncMutableAsset(ctx, home, relative); err != nil {
		return fmt.Errorf("%w: inbound asset published: %w", sandbox.ErrOutcomeUnknown, err)
	}
	return nil
}

func (s *LocalStore) mutableAssetRoot(home Record, relative string) (*os.Root, string, error) {
	if home.Key.Kind != PrincipalHome || home.StoreID != s.id {
		return nil, "", errors.New("home: mutable asset target must be a local PrincipalHome")
	}
	if err := s.ValidateLocator(home.Key, home.Locator); err != nil {
		return nil, "", fmt.Errorf("home: invalid mutable asset locator: %w", err)
	}
	if err := validateMutableAssetRelative(relative); err != nil {
		return nil, "", err
	}
	root, err := os.OpenRoot(s.base)
	if err != nil {
		return nil, "", fmt.Errorf("open store root: %w", err)
	}
	if info, err := root.Lstat(home.Locator); err != nil {
		_ = root.Close()
		return nil, "", err
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		_ = root.Close()
		return nil, "", errors.New("home: mutable asset Home is not a directory")
	}
	homeRoot, err := root.OpenRoot(home.Locator)
	_ = root.Close()
	if err != nil {
		return nil, "", fmt.Errorf("open mutable asset Home: %w", err)
	}
	return homeRoot, path.Join("data", "assets", relative), nil
}

func validateMutableAssetRelative(relative string) error {
	if relative == "" || path.IsAbs(relative) || filepath.IsAbs(relative) || filepath.Clean(relative) != relative || strings.Contains(relative, `\`) {
		return errors.New("home: mutable asset path must be a clean relative path")
	}
	for part := range strings.SplitSeq(relative, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("home: mutable asset path escapes its Home")
		}
	}
	return nil
}

func createLocalAssetTemp(root *os.Root, dir string) (*os.File, string, error) {
	for range 100 {
		var random [8]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", err
		}
		name := path.Join(dir, ".stella-migrate-"+hex.EncodeToString(random[:]))
		file, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return file, name, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("home: create migration temp: too many collisions")
}

func (s *LocalStore) sync(file *os.File) error {
	if s.syncFile == nil {
		return file.Sync()
	}
	return s.syncFile(file)
}

func copyDigest(ctx context.Context, dst io.Writer, src io.Reader) (mutableAssetDigest, error) {
	hash := sha256.New()
	buffer := make([]byte, 32*1024)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return mutableAssetDigest{}, err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			written, writeErr := dst.Write(buffer[:n])
			if writeErr != nil {
				return mutableAssetDigest{}, writeErr
			}
			if written != n {
				return mutableAssetDigest{}, io.ErrShortWrite
			}
			if _, err := hash.Write(buffer[:n]); err != nil {
				return mutableAssetDigest{}, err
			}
			size += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return mutableAssetDigest{}, readErr
		}
	}
	var digest mutableAssetDigest
	digest.Size = size
	copy(digest.SHA256[:], hash.Sum(nil))
	return digest, nil
}

func digestRootFile(ctx context.Context, root *os.Root, name string) (mutableAssetDigest, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return mutableAssetDigest{}, err
	}
	if !info.Mode().IsRegular() {
		return mutableAssetDigest{}, errors.New("home: migration destination is not a regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return mutableAssetDigest{}, err
	}
	digest, readErr := copyDigest(ctx, io.Discard, file)
	closeErr := file.Close()
	if readErr != nil {
		return mutableAssetDigest{}, errors.Join(readErr, closeErr)
	}
	if closeErr != nil {
		return mutableAssetDigest{}, closeErr
	}
	return digest, nil
}
