package skills

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/home"
)

const managedRevisionRoot = ".stella-revisions"

const publicationReconcileTimeout = 5 * time.Second

var errCurrentSkillSelectorMissing = errors.New("skills: current selector is missing")

type managedSnapshot struct {
	Skill Skill
	Files []revisionFile
}

func validInventoryComponent(value string) bool {
	return value != "" && len(value) <= 128 && value != "." && value != ".." && !strings.ContainsAny(value, "/\\\x00")
}

func skillRootSelection(identity Skill) (home.WorkspaceRequest, home.RootScope, error) {
	request := home.WorkspaceRequest{UserID: identity.UserID, AgentID: identity.AgentID}
	switch identity.Scope {
	case "system":
		return request, home.RootSystemSkills, nil
	case "system_agent":
		return request, home.RootSystemAgentSkills, nil
	case "user":
		return request, home.RootUserSkills, nil
	case "user_agent":
		return request, home.RootUserAgentSkills, nil
	default:
		return home.WorkspaceRequest{}, 0, fmt.Errorf("%w: unknown scope %q", ErrInvalidSkillRevision, identity.Scope)
	}
}

func selectedRevisionPath(identity Skill, digest string) (string, error) {
	if !validInventoryComponent(identity.ID) || !validSkillDigest(digest) {
		return "", fmt.Errorf("%w: invalid identity or digest", ErrInvalidSkillRevision)
	}
	return path.Join(managedRevisionRoot, identity.ID, digest), nil
}

func parseSelectedRevision(identity Skill, target string) (string, error) {
	prefix := path.Join(managedRevisionRoot, identity.ID) + "/"
	if !strings.HasPrefix(target, prefix) || path.Clean(target) != target {
		return "", fmt.Errorf("%w: selected target", ErrInvalidSkillRevision)
	}
	digest := strings.TrimPrefix(target, prefix)
	if strings.Contains(digest, "/") || !validSkillDigest(digest) {
		return "", fmt.Errorf("%w: selected digest", ErrInvalidSkillRevision)
	}
	return digest, nil
}

func readRootBytes(ctx context.Context, root home.RootOperations, filename string, limit int64) ([]byte, error) {
	var output bytes.Buffer
	if err := root.Read(ctx, filename, &output, home.ReadOptions{MaxBytes: limit}); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func readCurrentSnapshot(ctx context.Context, root home.SkillRootOperations, identity Skill) (managedSnapshot, error) {
	target, err := root.Readlink(ctx, identity.ID)
	if errors.Is(err, fs.ErrNotExist) {
		return managedSnapshot{}, errors.Join(errCurrentSkillSelectorMissing, err)
	}
	if err != nil {
		return managedSnapshot{}, err
	}
	digest, err := parseSelectedRevision(identity, target)
	if err != nil {
		return managedSnapshot{}, err
	}
	return readRevisionSnapshot(ctx, root, identity, digest)
}

func readRevisionSnapshot(ctx context.Context, root home.SkillRootOperations, identity Skill, digest string) (managedSnapshot, error) {
	revision, err := selectedRevisionPath(identity, digest)
	if err != nil {
		return managedSnapshot{}, err
	}
	info, err := root.Lstat(ctx, revision)
	if err != nil {
		return managedSnapshot{}, err
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return managedSnapshot{}, fmt.Errorf("%w: revision is not an immutable directory", ErrInvalidSkillRevision)
	}
	manifest, err := readRootBytes(ctx, root, path.Join(revision, SkillManifestFile), MaxManagedSkillManifestBytes)
	if err != nil {
		return managedSnapshot{}, err
	}
	skill, err := decodeCanonicalManifest(manifest)
	if err != nil {
		return managedSnapshot{}, err
	}
	if !sameSkillIdentity(identity, skill) {
		return managedSnapshot{}, fmt.Errorf("%w: manifest identity differs from inventory", ErrInvalidSkillRevision)
	}
	files, err := readRevisionFiles(ctx, root, revision)
	if err != nil {
		return managedSnapshot{}, err
	}
	computed, err := digestRevision(manifest, files)
	if err != nil {
		return managedSnapshot{}, err
	}
	if computed != digest {
		return managedSnapshot{}, fmt.Errorf("%w: revision digest mismatch", ErrInvalidSkillRevision)
	}
	skill.ContentDigest = digest
	return managedSnapshot{Skill: skill, Files: files}, nil
}

func readRevisionFiles(ctx context.Context, root home.SkillRootOperations, revision string) ([]revisionFile, error) {
	files := make([]revisionFile, 0)
	entriesSeen := 0
	// Every file can contribute at most maxManagedSkillTreeDepth directory
	// entries. Keep traversal bounded without rejecting a valid 512-file tree
	// merely because its files use distinct nested directories.
	maxEntries := MaxManagedSkillFiles*(maxManagedSkillTreeDepth+1) + 1
	var total int64
	var walk func(string, string, int) error
	walk = func(directory, relative string, depth int) error {
		if depth > maxManagedSkillTreeDepth {
			return ErrSkillLimit
		}
		entries, err := root.List(ctx, directory, home.ListOptions{Limit: MaxManagedSkillFiles + 2})
		if err != nil {
			return err
		}
		for _, entry := range entries {
			entriesSeen++
			if entriesSeen > maxEntries || entry.Name() == "" || strings.Contains(entry.Name(), "/") {
				return ErrSkillLimit
			}
			filename := entry.Name()
			if relative != "" {
				filename = path.Join(relative, filename)
			}
			full := path.Join(revision, filename)
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode()&fs.ModeSymlink != 0 || info.Mode()&fs.ModeType != 0 && !info.IsDir() {
				return fmt.Errorf("%w: special entry %q", ErrInvalidSkillRevision, filename)
			}
			if info.IsDir() {
				if strings.HasPrefix(entry.Name(), ".stella-") {
					return fmt.Errorf("%w: reserved directory %q", ErrInvalidSkillRevision, filename)
				}
				if err := walk(full, filename, depth+1); err != nil {
					return err
				}
				continue
			}
			if filename == SkillManifestFile {
				if relative != "" || info.Mode().Perm() != 0o644 {
					return fmt.Errorf("%w: invalid manifest entry", ErrInvalidSkillRevision)
				}
				continue
			}
			if err := validateSkillPath(filename); err != nil {
				return err
			}
			if info.Size() < 0 || info.Size() > MaxManagedSkillFileBytes || total > MaxManagedSkillAggregateBytes-info.Size() {
				return ErrSkillLimit
			}
			content, err := readRootBytes(ctx, root, full, MaxManagedSkillFileBytes)
			if err != nil {
				return err
			}
			if int64(len(content)) != info.Size() {
				return fmt.Errorf("%w: file changed during read", ErrInvalidSkillRevision)
			}
			total += int64(len(content))
			files = append(files, revisionFile{Path: filename, Mode: info.Mode().Perm(), Content: content})
		}
		return nil
	}
	if err := walk(revision, "", 0); err != nil {
		return nil, err
	}
	return validateRevisionFiles(files)
}

func writeRevision(ctx context.Context, root home.SkillRootOperations, stage string, manifest []byte, files []revisionFile) error {
	if err := root.Mkdir(ctx, stage, 0o700, home.MkdirOptions{}); err != nil {
		return err
	}
	directories := map[string]struct{}{stage: {}}
	for _, file := range files {
		parent := path.Dir(file.Path)
		if parent != "." {
			directory := path.Join(stage, parent)
			if err := root.Mkdir(ctx, directory, 0o755, home.MkdirOptions{Parents: true}); err != nil {
				return err
			}
			for current := directory; current != stage && current != "."; current = path.Dir(current) {
				directories[current] = struct{}{}
			}
		}
		if err := root.Write(ctx, path.Join(stage, file.Path), bytes.NewReader(file.Content), home.WriteOptions{Mode: file.Mode, Exclusive: true, Sync: true}); err != nil {
			return err
		}
	}
	if err := root.Write(ctx, path.Join(stage, SkillManifestFile), bytes.NewReader(manifest), home.WriteOptions{Mode: 0o644, Exclusive: true, Sync: true}); err != nil {
		return err
	}
	ordered := make([]string, 0, len(directories))
	for directory := range directories {
		ordered = append(ordered, directory)
	}
	sort.Slice(ordered, func(i, j int) bool { return strings.Count(ordered[i], "/") > strings.Count(ordered[j], "/") })
	for _, directory := range ordered {
		if err := root.SyncDirectory(ctx, directory); err != nil {
			return err
		}
	}
	return nil
}

func publishRevision(ctx context.Context, root home.SkillRootOperations, desired Skill, files []revisionFile, expectedDigest string, create bool, random func([]byte) error) (managedSnapshot, error) {
	manifest, err := canonicalManifest(desired)
	if err != nil {
		return managedSnapshot{}, err
	}
	digest, err := digestRevision(manifest, files)
	if err != nil {
		return managedSnapshot{}, err
	}
	desired.ContentDigest = digest

	current, currentErr := readCurrentSnapshot(ctx, root, desired)
	if create {
		if currentErr == nil {
			if current.Skill.ContentDigest != digest {
				return managedSnapshot{}, ErrSkillDigestConflict
			}
			parent := path.Join(managedRevisionRoot, desired.ID)
			for _, directory := range []string{parent, managedRevisionRoot, "."} {
				if err := root.SyncDirectory(ctx, directory); err != nil {
					return managedSnapshot{}, err
				}
			}
			selected, err := readCurrentSnapshot(ctx, root, desired)
			if err != nil || selected.Skill.ContentDigest != digest {
				return managedSnapshot{}, errors.Join(err, ErrSkillDigestConflict)
			}
			return selected, nil
		}
		if !errors.Is(currentErr, fs.ErrNotExist) {
			return managedSnapshot{}, currentErr
		}
	} else {
		if !validSkillDigest(expectedDigest) {
			return managedSnapshot{}, ErrSkillDigestRequired
		}
		if currentErr != nil {
			return managedSnapshot{}, currentErr
		}
		if current.Skill.ContentDigest != expectedDigest {
			return managedSnapshot{}, ErrSkillDigestConflict
		}
	}

	parent := path.Join(managedRevisionRoot, desired.ID)
	if err := root.Mkdir(ctx, parent, 0o700, home.MkdirOptions{Parents: true}); err != nil {
		return managedSnapshot{}, err
	}
	for _, directory := range []string{managedRevisionRoot, parent} {
		info, err := root.Lstat(ctx, directory)
		if err != nil {
			return managedSnapshot{}, err
		}
		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return managedSnapshot{}, fmt.Errorf("%w: publication directory %q", ErrInvalidSkillRevision, directory)
		}
	}
	var entropy [12]byte
	if random == nil {
		return managedSnapshot{}, errors.New("skills: publication entropy source is required")
	}
	if err := random(entropy[:]); err != nil {
		return managedSnapshot{}, err
	}
	suffix := hex.EncodeToString(entropy[:])
	stage := path.Join(parent, ".stage-"+suffix)
	defer removePublicationArtifact(root, stage, home.RemoveOptions{Recursive: true})
	if err := writeRevision(ctx, root, stage, manifest, files); err != nil {
		return managedSnapshot{}, err
	}
	revision, _ := selectedRevisionPath(desired, digest)
	if err := root.Rename(ctx, stage, revision, home.RenameOptions{NoReplace: true}); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return managedSnapshot{}, err
		}
		existing, verifyErr := readRevisionSnapshot(ctx, root, desired, digest)
		if verifyErr != nil || existing.Skill.ContentDigest != digest {
			return managedSnapshot{}, errors.Join(ErrSkillDigestConflict, verifyErr)
		}
	}
	// The revision name and its skill-ID parent must be durable before a durable
	// selector is allowed to point at them. Re-fence both on every retry.
	for _, directory := range []string{parent, managedRevisionRoot, "."} {
		if err := root.SyncDirectory(ctx, directory); err != nil {
			return managedSnapshot{}, fmt.Errorf("%w: fence revision publication: %w", home.ErrOutcomeUnknown, err)
		}
	}

	temporary := "." + desired.ID + ".current-" + suffix
	defer removePublicationArtifact(root, temporary, home.RemoveOptions{})
	if err := root.Symlink(ctx, revision, temporary); err != nil {
		return managedSnapshot{}, err
	}
	if err := root.Rename(ctx, temporary, desired.ID, home.RenameOptions{NoReplace: create}); err != nil {
		if create && errors.Is(err, fs.ErrExist) {
			return managedSnapshot{}, ErrSkillDigestConflict
		}
		if !home.IsOutcomeUnknown(err) {
			return managedSnapshot{}, err
		}
		reconcileCtx, cancel := context.WithTimeout(context.Background(), publicationReconcileTimeout)
		selected, verifyErr := selectionMatchesRoot(reconcileCtx, root, desired, digest)
		cancel()
		if !selected || verifyErr != nil {
			return managedSnapshot{}, errors.Join(err, verifyErr)
		}
	}
	reconcileCtx, cancel := context.WithTimeout(context.Background(), publicationReconcileTimeout)
	defer cancel()
	if err := root.SyncDirectory(reconcileCtx, "."); err != nil {
		return managedSnapshot{}, errors.Join(home.ErrOutcomeUnknown, fmt.Errorf("sync current selector: %w", err))
	}
	selected, err := readCurrentSnapshot(reconcileCtx, root, desired)
	if err != nil || selected.Skill.ContentDigest != digest {
		return managedSnapshot{}, fmt.Errorf("%w: exact selector reread: %w", home.ErrOutcomeUnknown, errors.Join(err, ErrSkillDigestConflict))
	}
	return selected, nil
}

func removePublicationArtifact(root home.SkillRootOperations, name string, options home.RemoveOptions) {
	ctx, cancel := context.WithTimeout(context.Background(), publicationReconcileTimeout)
	defer cancel()
	_ = root.Remove(ctx, name, options)
}

func selectionMatchesRoot(ctx context.Context, root home.SkillRootOperations, identity Skill, digest string) (bool, error) {
	snapshot, err := readCurrentSnapshot(ctx, root, identity)
	if err != nil {
		return false, err
	}
	return snapshot.Skill.ContentDigest == digest, nil
}
