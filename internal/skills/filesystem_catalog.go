package skills

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

const (
	// Catalog reads only control and prompt files. Raise these ceilings only when
	// Skill authoring needs larger text; binary companions are deliberately not read.
	maxCatalogSkillBytes    = 1 << 20
	maxCatalogMetadataBytes = 64 << 10
	// Managed revision verification is intentionally bounded. Raise only when
	// published Skills need larger trees; streaming avoids retaining companions.
	maxManagedTreeEntries = 512
	maxManagedTreeDepth   = 16
	maxManagedFileBytes   = 8 << 20
	maxManagedTreeBytes   = 32 << 20
)

// FilesystemCatalogRoot is an opaque, attachment-backed catalog coordinate.
// Its constructors are the only way to bind a filesystem root to scope identity.
type FilesystemCatalogRoot struct {
	root    string
	scope   string
	userID  string
	agentID string
	homeID  string
}

func SystemFilesystemCatalogRoot(root string, attachment sandbox.HomeAttachment) (FilesystemCatalogRoot, error) {
	return newFilesystemCatalogRoot(root, attachment, "system", "", "")
}

func SystemAgentFilesystemCatalogRoot(root string, attachment sandbox.HomeAttachment, agentID string) (FilesystemCatalogRoot, error) {
	return newFilesystemCatalogRoot(root, attachment, "system_agent", "", agentID)
}

func UserFilesystemCatalogRoot(root string, attachment sandbox.HomeAttachment, userID string) (FilesystemCatalogRoot, error) {
	return newFilesystemCatalogRoot(root, attachment, "user", userID, "")
}

func UserAgentFilesystemCatalogRoot(root string, attachment sandbox.HomeAttachment, userID, agentID string) (FilesystemCatalogRoot, error) {
	return newFilesystemCatalogRoot(root, attachment, "user_agent", userID, agentID)
}

// FilesystemSkillDescriptor pins one catalog result. RevisionPath is always a
// canonical sandbox path: managed entries point at the inspected immutable
// revision, while ordinary entries point at their direct POSIX directory.
type FilesystemSkillDescriptor struct {
	Skill        pkgplugins.Skill
	RevisionPath string
	Digest       string
	Managed      bool
}

// FilesystemCatalogSnapshot retains inactive descriptors separately so callers
// exclude deprecated Skills intentionally rather than treating them as malformed.
type FilesystemCatalogSnapshot struct {
	Active     []FilesystemSkillDescriptor
	Deprecated []FilesystemSkillDescriptor
}

// SnapshotFilesystemCatalog scans direct children of root through the mediated
// filesystem. It never follows a managed top-level link after inspection.
func SnapshotFilesystemCatalog(ctx context.Context, filesystem sandbox.Filesystem, catalogRoot FilesystemCatalogRoot) (FilesystemCatalogSnapshot, error) {
	if filesystem == nil {
		return FilesystemCatalogSnapshot{}, errors.New("skills: filesystem is required")
	}
	inspector, ok := filesystem.(sandbox.ManagedSkillTargetInspector)
	if !ok {
		return FilesystemCatalogSnapshot{}, errors.New("skills: filesystem does not support managed Skill inspection")
	}
	entries, err := filesystem.List(ctx, catalogRoot.root)
	if err != nil {
		return FilesystemCatalogSnapshot{}, fmt.Errorf("skills: list filesystem catalog: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	seen := make(map[string]struct{}, len(entries))
	var snapshot FilesystemCatalogSnapshot
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name, ".") { // includes .stella-revisions
			continue
		}
		if err := skillNameValidationError(entry.Name, entry.Name); err != nil {
			return FilesystemCatalogSnapshot{}, fmt.Errorf("skills: invalid filesystem catalog entry %q: %w", entry.Name, err)
		}
		if _, duplicate := seen[entry.Name]; duplicate {
			return FilesystemCatalogSnapshot{}, fmt.Errorf("skills: duplicate filesystem catalog entry %q", entry.Name)
		}
		seen[entry.Name] = struct{}{}
		entryPath := path.Join(catalogRoot.root, entry.Name)
		target, err := inspector.InspectManagedSkillTarget(ctx, entryPath)
		if err != nil {
			return FilesystemCatalogSnapshot{}, fmt.Errorf("skills: inspect managed target %q: %w", entry.Name, err)
		}
		managed := target.Managed
		revisionPath := entryPath
		if managed {
			revisionPath = path.Join(catalogRoot.root, ".stella-revisions", entry.Name, target.Digest)
		} else if !entry.IsDir || entry.Mode&fs.ModeSymlink != 0 {
			return FilesystemCatalogSnapshot{}, fmt.Errorf("skills: catalog entry %q is not a real directory", entry.Name)
		}
		if managed {
			if err := verifyManagedRevision(ctx, filesystem, revisionPath, target.Digest); err != nil {
				return FilesystemCatalogSnapshot{}, fmt.Errorf("skills: verify managed revision %q: %w", entry.Name, err)
			}
		}
		descriptor, deprecated, err := snapshotFilesystemSkill(ctx, filesystem, revisionPath, entry.Name, catalogRoot, target, managed)
		if err != nil {
			return FilesystemCatalogSnapshot{}, err
		}
		if deprecated {
			snapshot.Deprecated = append(snapshot.Deprecated, descriptor)
		} else {
			snapshot.Active = append(snapshot.Active, descriptor)
		}
	}
	return snapshot, nil
}

func snapshotFilesystemSkill(ctx context.Context, filesystem sandbox.Filesystem, directory, name string, scope FilesystemCatalogRoot, target sandbox.ManagedSkillTarget, managed bool) (FilesystemSkillDescriptor, bool, error) {
	main, err := readCatalogFile(ctx, filesystem, path.Join(directory, pkgplugins.SkillMainFile), maxCatalogSkillBytes)
	if err != nil {
		return FilesystemSkillDescriptor{}, false, fmt.Errorf("skills: read %s/%s: %w", name, pkgplugins.SkillMainFile, err)
	}
	if len(main) == 0 {
		return FilesystemSkillDescriptor{}, false, fmt.Errorf("skills: %s/%s is empty", name, pkgplugins.SkillMainFile)
	}
	frontmatter, err := parseFrontmatter(string(main))
	if err != nil {
		return FilesystemSkillDescriptor{}, false, fmt.Errorf("skills: parse %s frontmatter: %w", name, err)
	}
	if frontmatter.Name == "" {
		frontmatter.Name = name
	}
	if err := skillNameValidationError(frontmatter.Name, name); err != nil {
		return FilesystemSkillDescriptor{}, false, fmt.Errorf("skills: %s frontmatter: %w", name, err)
	}
	if strings.TrimSpace(frontmatter.Description) == "" {
		return FilesystemSkillDescriptor{}, false, fmt.Errorf("skills: %s frontmatter description is required", name)
	}
	envelope := defaultSkillMetadataEnvelope()
	metadataPath := path.Join(directory, skillMetadataFile)
	metadata, metadataErr := readCatalogFile(ctx, filesystem, metadataPath, maxCatalogMetadataBytes)
	hasMetadata := metadataErr == nil
	if hasMetadata {
		envelope, err = decodeSkillMetadataEnvelope(metadata)
		if err != nil {
			return FilesystemSkillDescriptor{}, false, fmt.Errorf("skills: decode %s metadata: %w", name, err)
		}
		if managed {
			canonical, err := encodeSkillMetadataEnvelope(envelope)
			if err != nil || string(canonical) != string(metadata) {
				return FilesystemSkillDescriptor{}, false, fmt.Errorf("skills: %s metadata is not canonical v1", name)
			}
		}
	} else if managed || !errors.Is(metadataErr, fs.ErrNotExist) {
		return FilesystemSkillDescriptor{}, false, fmt.Errorf("skills: read %s metadata: %w", name, metadataErr)
	}
	if !managed && !hasMetadata {
		envelope.DisableModelInvocation = frontmatter.DisableModelInvocation
	}
	pluginMetadata, err := canonicalJSON(envelope.Metadata)
	if err != nil {
		return FilesystemSkillDescriptor{}, false, fmt.Errorf("skills: encode %s metadata: %w", name, err)
	}
	descriptor := FilesystemSkillDescriptor{
		Skill:        pkgplugins.Skill{ID: scope.scope + ":" + scope.homeID + ":" + name, Scope: scope.scope, UserID: scope.userID, AgentID: scope.agentID, Name: name, Description: frontmatter.Description, Status: envelope.Status, DisableModelInvocation: envelope.DisableModelInvocation, Metadata: pluginMetadata, CreatedAt: envelope.CreatedAt, UpdatedAt: envelope.UpdatedAt, Version: envelope.LegacyLifecycleVersion},
		RevisionPath: directory, Digest: target.Digest, Managed: managed,
	}
	return descriptor, envelope.Status == SkillStatusDeprecated, nil
}

func newFilesystemCatalogRoot(root string, attachment sandbox.HomeAttachment, scope, userID, agentID string) (FilesystemCatalogRoot, error) {
	if root == "" || !strings.HasPrefix(root, "/") || path.Clean(root) != root {
		return FilesystemCatalogRoot{}, errors.New("skills: invalid filesystem catalog root")
	}
	if attachment.HomeID == "" || attachment.StoreID == "" || attachment.Locator == "" {
		return FilesystemCatalogRoot{}, errors.New("skills: filesystem catalog attachment is incomplete")
	}
	catalogRoot := FilesystemCatalogRoot{root: root, scope: scope, userID: userID, agentID: agentID, homeID: attachment.HomeID}
	if err := validateFilesystemCatalogScope(catalogRoot); err != nil {
		return FilesystemCatalogRoot{}, err
	}
	return catalogRoot, nil
}

func validateFilesystemCatalogScope(scope FilesystemCatalogRoot) error {
	switch scope.scope {
	case "system":
		if scope.userID != "" || scope.agentID != "" {
			return errors.New("skills: system filesystem catalog has owners")
		}
	case "system_agent":
		if scope.userID != "" || scope.agentID == "" {
			return errors.New("skills: system_agent filesystem catalog identity is invalid")
		}
	case "user":
		if scope.userID == "" || scope.agentID != "" {
			return errors.New("skills: user filesystem catalog identity is invalid")
		}
	case "user_agent":
		if scope.userID == "" || scope.agentID == "" {
			return errors.New("skills: user_agent filesystem catalog identity is invalid")
		}
	default:
		return errors.New("skills: invalid filesystem catalog scope")
	}
	return nil
}

type managedTreeFile struct {
	path     string
	mode     fs.FileMode
	size     int64
	absolute string
}

// managedRevisionInvalidError distinguishes deterministic corrupt-tree facts
// from filesystem and transport failures while preflighting an existing target.
type managedRevisionInvalidError struct{ err error }

func (e *managedRevisionInvalidError) Error() string { return e.err.Error() }
func (e *managedRevisionInvalidError) Unwrap() error { return e.err }
func invalidManagedRevision(err error) error         { return &managedRevisionInvalidError{err: err} }

func verifyManagedRevision(ctx context.Context, filesystem sandbox.Filesystem, root, wantDigest string) error {
	entries := 0
	files, err := collectManagedTree(ctx, filesystem, root, "", 0, &entries, nil)
	if err != nil {
		return err
	}
	var metadata managedTreeFile
	main := false
	for _, file := range files {
		if file.path == skillMetadataFile {
			metadata = file
			continue
		}
		if file.path == pkgplugins.SkillMainFile {
			main = true
		}
	}
	if metadata.absolute == "" || !main {
		return invalidManagedRevision(errors.New("managed revision lacks required control files"))
	}
	if metadata.mode != 0o644 {
		return invalidManagedRevision(errors.New("managed revision metadata mode is not regular 0644"))
	}
	metadataBytes, err := readManagedMetadata(ctx, filesystem, metadata)
	if err != nil {
		return err
	}
	envelope, err := decodeSkillMetadataEnvelope(metadataBytes)
	if err != nil {
		return invalidManagedRevision(err)
	}
	canonical, err := encodeSkillMetadataEnvelope(envelope)
	if err != nil {
		return invalidManagedRevision(err)
	}
	if string(canonical) != string(metadataBytes) {
		return invalidManagedRevision(errors.New("managed revision metadata is not canonical v1"))
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	var total int64
	digestEntries := make([]sandbox.ManagedSkillTreeEntry, 0, len(files))
	for _, file := range files {
		if file.path != skillMetadataFile {
			if file.size > maxManagedFileBytes || total > maxManagedTreeBytes-file.size {
				return invalidManagedRevision(errors.New("managed revision exceeds content limit"))
			}
			total += file.size
		}

		if file.path == skillMetadataFile {
			digestEntries = append(digestEntries, sandbox.ManagedSkillTreeEntry{Path: file.path, Mode: 0o644, Length: int64(len(canonical)), Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(string(canonical))), nil }})
			continue
		}
		digestEntries = append(digestEntries, sandbox.ManagedSkillTreeEntry{Path: file.path, Mode: file.mode, Length: file.size, Open: func() (io.ReadCloser, error) {
			reader, info, err := filesystem.Read(ctx, file.absolute, sandbox.ReadOptions{MaxBytes: maxManagedFileBytes})
			if err != nil {
				return nil, err
			}
			if info.Size != file.size || info.Mode != file.mode || info.Mode&fs.ModeType != 0 {
				_ = reader.Close()
				return nil, errors.New("managed revision file changed during read")
			}
			return reader, nil
		}})
	}
	got, err := sandbox.DigestManagedSkillTreeV1(digestEntries)
	if err != nil {
		return err
	}
	if got != wantDigest {
		return invalidManagedRevision(fmt.Errorf("managed revision digest %q does not match target %q", got, wantDigest))
	}
	return nil
}

func readManagedMetadata(ctx context.Context, filesystem sandbox.Filesystem, metadata managedTreeFile) ([]byte, error) {
	reader, info, err := filesystem.Read(ctx, metadata.absolute, sandbox.ReadOptions{MaxBytes: maxCatalogMetadataBytes})
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if info.Size != metadata.size || info.Mode != metadata.mode || info.Mode != 0o644 || int64(len(data)) != metadata.size {
		return nil, errors.New("managed revision metadata changed during read")
	}
	return data, nil
}

func collectManagedTree(ctx context.Context, filesystem sandbox.Filesystem, root, relative string, depth int, entriesSeen *int, files []managedTreeFile) ([]managedTreeFile, error) {
	if depth > maxManagedTreeDepth {
		return nil, invalidManagedRevision(errors.New("managed revision exceeds directory depth"))
	}
	directory := root
	if relative != "" {
		directory = path.Join(root, relative)
	}
	entries, err := filesystem.List(ctx, directory)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		// Count every discovered entry before descending or reading it. The root
		// itself is not an entry, so it deliberately does not consume the budget.
		if *entriesSeen >= maxManagedTreeEntries {
			return nil, invalidManagedRevision(errors.New("managed revision exceeds entry limit"))
		}
		*entriesSeen++
		if entry.Name == "" || strings.Contains(entry.Name, "/") || entry.Name == "." || entry.Name == ".." {
			return nil, invalidManagedRevision(errors.New("managed revision has invalid entry name"))
		}
		rel := entry.Name
		if relative != "" {
			rel = path.Join(relative, entry.Name)
		}
		if rel == ".stella-revisions" || strings.HasPrefix(rel, ".stella-revisions/") {
			return nil, invalidManagedRevision(errors.New("managed revision uses reserved control namespace"))
		}
		if entry.Mode&fs.ModeSymlink != 0 || entry.Mode&fs.ModeType != 0 && !entry.Mode.IsDir() {
			return nil, invalidManagedRevision(fmt.Errorf("managed revision entry %q is not a regular file or directory", rel))
		}
		if entry.IsDir {
			var nestedErr error
			files, nestedErr = collectManagedTree(ctx, filesystem, root, rel, depth+1, entriesSeen, files)
			if nestedErr != nil {
				return nil, nestedErr
			}
			continue
		}
		if err := validateSkillTreePath(rel); err != nil && rel != skillMetadataFile {
			return nil, invalidManagedRevision(err)
		}
		files = append(files, managedTreeFile{path: rel, mode: entry.Mode, size: entry.Size, absolute: path.Join(root, rel)})
	}
	return files, nil
}

func readCatalogFile(ctx context.Context, filesystem sandbox.Filesystem, filename string, limit int64) ([]byte, error) {
	reader, _, err := filesystem.Read(ctx, filename, sandbox.ReadOptions{MaxBytes: limit})
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return data, nil
}
