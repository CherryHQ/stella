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

	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

// HomeCatalogFilesystem is the only Home capability catalog reads need. It
// deliberately cannot ensure a Home, expose an attachment, or reveal a host
// locator.
type HomeCatalogFilesystem interface {
	UseExistingSkillFilesystem(context.Context, *home.SkillRoot, func(sandbox.Filesystem) error) (bool, error)
}

// HomeCatalogInventory supplies authoritative global identities. Directory
// names are untrusted content and must never become user or agent identities.
type HomeCatalogInventory interface {
	ListRoots(context.Context) ([]HomeCatalogRoot, error)
}

// HomeCatalogRoot identifies one of the four typed catalog scopes.
type HomeCatalogRoot struct {
	Scope, UserID, AgentID string
}

// HomeCatalogSkill keeps Digest for source compatibility with catalog callers
// that predate Skill.ContentDigest. For managed results they are always equal;
// Version remains independent legacy lifecycle metadata.
type HomeCatalogSkill struct {
	Skill   Skill
	Digest  string
	Managed bool
}

// HomeManagedSkillSnapshot is an immutable-revision read suitable as the
// starting point for one managed mutation. Files deliberately retain opaque
// bytes and modes; callers never need a Home locator or host path to build the
// next complete revision.
type HomeManagedSkillSnapshot struct {
	Skill         Skill
	ContentDigest string
	Files         []HomeSkillFile
}

// HomeCatalog is a read-only catalog over ready Homes. It does not fall back
// to PostgreSQL Skill rows and it never creates a Home.
type HomeCatalog struct {
	homes     HomeCatalogFilesystem
	inventory HomeCatalogInventory
}

func NewHomeCatalog(homes HomeCatalogFilesystem, inventory HomeCatalogInventory) (*HomeCatalog, error) {
	if homes == nil {
		return nil, errors.New("skills: Home catalog filesystem is required")
	}
	return &HomeCatalog{homes: homes, inventory: inventory}, nil
}

// Get reads one exact canonical filesystem Skill ID. It opens only that typed
// root and takes one catalog snapshot, so managed metadata remains bound to
// the inspected immutable revision without scanning unrelated Homes.
func (c *HomeCatalog) Get(ctx context.Context, id string) (HomeCatalogSkill, error) {
	var out HomeCatalogSkill
	err := c.useSkillByID(ctx, id, func(_ sandbox.Filesystem, descriptor FilesystemSkillDescriptor) error {
		out = homeCatalogSkill(descriptor)
		return nil
	})
	return out, err
}

func (c *HomeCatalog) List(ctx context.Context, vc ViewContext) ([]HomeCatalogSkill, error) {
	roots := []HomeCatalogRoot{{Scope: "user_agent", UserID: vc.UserID, AgentID: vc.AgentID}, {Scope: "user", UserID: vc.UserID}, {Scope: "system_agent", AgentID: vc.AgentID}, {Scope: "system"}}
	seen := map[string]struct{}{}
	out := make([]HomeCatalogSkill, 0)
	for _, root := range roots {
		if !applicableHomeCatalogRoot(root) {
			continue
		}
		skills, err := c.listRoot(ctx, root, false)
		if err != nil {
			return nil, err
		}
		for _, skill := range skills {
			// Deprecated entries are historical and do not shadow a lower active
			// scope. An active disabled entry does shadow it: disabling is an
			// explicit suppression, not a request to fall back.
			if skill.Skill.Status == SkillStatusDeprecated {
				continue
			}
			if _, exists := seen[skill.Skill.Name]; exists {
				continue
			}
			seen[skill.Skill.Name] = struct{}{}
			if skill.Skill.DisableModelInvocation {
				continue
			}
			out = append(out, skill)
		}
	}
	return out, nil
}

func (c *HomeCatalog) Resolve(ctx context.Context, name string, vc ViewContext) (*HomeCatalogSkill, error) {
	if err := skillNameValidationError(name, name); err != nil {
		return nil, fmt.Errorf("skills: resolve name: %w", err)
	}
	items, err := c.List(ctx, vc)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].Skill.Name == name {
			return &items[i], nil
		}
	}
	return nil, nil
}

func (c *HomeCatalog) ListByScope(ctx context.Context, scope, userID, agentID string) ([]HomeCatalogSkill, error) {
	return c.listRoot(ctx, HomeCatalogRoot{Scope: scope, UserID: userID, AgentID: agentID}, true)
}

func (c *HomeCatalog) ListAll(ctx context.Context) ([]HomeCatalogSkill, error) {
	if c.inventory == nil {
		return nil, errors.New("skills: Home catalog inventory is required for ListAll")
	}
	roots, err := c.inventory.ListRoots(ctx)
	if err != nil {
		return nil, fmt.Errorf("skills: list Home catalog inventory: %w", err)
	}
	out := make([]HomeCatalogSkill, 0)
	seen := make(map[string]struct{})
	for _, root := range roots {
		key, err := encodeFilesystemSkillID(root.Scope, root.UserID, root.AgentID, "inventory")
		if err != nil {
			return nil, fmt.Errorf("skills: invalid Home catalog inventory: %w", err)
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		items, err := c.listRoot(ctx, root, true)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Skill.ID < out[j].Skill.ID })
	return out, nil
}

func (c *HomeCatalog) LoadFile(ctx context.Context, id, filename string) (string, error) {
	if err := validateSkillTreePath(filename); err != nil {
		return "", fmt.Errorf("skills: invalid Skill file path: %w", err)
	}
	var content string
	err := c.useSkillByID(ctx, id, func(filesystem sandbox.Filesystem, descriptor FilesystemSkillDescriptor) error {
		data, err := readCatalogFile(ctx, filesystem, path.Join(descriptor.RevisionPath, filename), maxManagedFileBytes)
		if err != nil {
			return err
		}
		content = string(data)
		return nil
	})
	return content, err
}

func (c *HomeCatalog) ListFiles(ctx context.Context, id string) ([]string, error) {
	var files []string
	err := c.useSkillByID(ctx, id, func(filesystem sandbox.Filesystem, descriptor FilesystemSkillDescriptor) error {
		collected, err := catalogTreeFiles(ctx, filesystem, descriptor.RevisionPath, false)
		if err != nil {
			return err
		}
		files = collected
		return nil
	})
	return files, err
}

func (c *HomeCatalog) ListFilesWithContent(ctx context.Context, id string) (map[string]string, error) {
	files := map[string]string{}
	err := c.useSkillByID(ctx, id, func(filesystem sandbox.Filesystem, descriptor FilesystemSkillDescriptor) error {
		paths, err := catalogTreeFiles(ctx, filesystem, descriptor.RevisionPath, false)
		if err != nil {
			return err
		}
		var total int64
		for _, filename := range paths {
			remaining := maxManagedTreeBytes - total
			if remaining <= 0 {
				return errors.New("skills: catalog tree exceeds content limit")
			}
			limit := min(int64(maxManagedFileBytes), remaining)
			data, err := readCatalogFile(ctx, filesystem, path.Join(descriptor.RevisionPath, filename), limit)
			if err != nil {
				return err
			}
			if int64(len(data)) > remaining {
				return errors.New("skills: catalog tree exceeds content limit")
			}
			total += int64(len(data))
			files[filename] = string(data)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, err
}

// LoadManagedSnapshot opens the exact typed catalog once, inspects the direct
// entry once, and reads only the immutable revision selected by that inspection.
// It never follows the mutable direct link while loading content.
func (c *HomeCatalog) LoadManagedSnapshot(ctx context.Context, id string) (HomeManagedSkillSnapshot, error) {
	scope, userID, agentID, name, err := decodeFilesystemSkillID(id)
	if err != nil {
		return HomeManagedSkillSnapshot{}, err
	}
	coordinate := HomeCatalogRoot{Scope: scope, UserID: userID, AgentID: agentID}
	root, err := homeCatalogSkillRoot(coordinate)
	if err != nil {
		return HomeManagedSkillSnapshot{}, err
	}
	var out HomeManagedSkillSnapshot
	exists, err := c.homes.UseExistingSkillFilesystem(ctx, root, func(filesystem sandbox.Filesystem) error {
		inspector, ok := filesystem.(sandbox.ManagedSkillTargetInspector)
		if !ok {
			return errors.New("skills: filesystem does not support managed Skill inspection")
		}
		entry := path.Join(sandbox.PathWorkspace, name)
		target, err := inspector.InspectManagedSkillTarget(ctx, entry)
		if err != nil {
			return fmt.Errorf("skills: inspect managed Skill %q: %w", name, err)
		}
		if !target.Managed {
			if _, err := filesystem.Stat(ctx, entry); err != nil {
				return err
			}
			return ErrSkillNotMutable
		}
		revision := path.Join(sandbox.PathWorkspace, ".stella-revisions", name, target.Digest)
		captured, err := captureManagedRevision(ctx, filesystem, revision)
		if err != nil {
			return fmt.Errorf("skills: capture managed Skill %q: %w", name, err)
		}
		snapshot, err := managedSnapshotFromCapture(id, scope, userID, agentID, name, target.Digest, captured)
		if err != nil {
			return err
		}
		out = snapshot
		return nil
	})
	if err != nil {
		return HomeManagedSkillSnapshot{}, fmt.Errorf("skills: open Home catalog: %w", err)
	}
	if !exists {
		return HomeManagedSkillSnapshot{}, fs.ErrNotExist
	}
	return out, nil
}

// LoadManagedRevision reads one named retained immutable revision directly. It
// deliberately never inspects or follows the mutable direct catalog entry: a
// stale writer uses this to reconstruct the complete state it actually saw.
func (c *HomeCatalog) LoadManagedRevision(ctx context.Context, id, digest string) (HomeManagedSkillSnapshot, error) {
	if !validHomeSkillDigest(digest) {
		return HomeManagedSkillSnapshot{}, errors.New("skills: expected digest must be a lowercase SHA-256 digest")
	}
	scope, userID, agentID, name, err := decodeFilesystemSkillID(id)
	if err != nil {
		return HomeManagedSkillSnapshot{}, err
	}
	root, err := homeCatalogSkillRoot(HomeCatalogRoot{Scope: scope, UserID: userID, AgentID: agentID})
	if err != nil {
		return HomeManagedSkillSnapshot{}, err
	}
	var out HomeManagedSkillSnapshot
	exists, err := c.homes.UseExistingSkillFilesystem(ctx, root, func(filesystem sandbox.Filesystem) error {
		revision := path.Join(sandbox.PathWorkspace, ".stella-revisions", name, digest)
		captured, err := captureManagedRevision(ctx, filesystem, revision)
		if err != nil {
			return fmt.Errorf("skills: capture retained managed Skill %q: %w", name, err)
		}
		snapshot, err := managedSnapshotFromCapture(id, scope, userID, agentID, name, digest, captured)
		if err != nil {
			return fmt.Errorf("skills: decode retained managed Skill %q: %w", name, err)
		}
		out = snapshot
		return nil
	})
	if err != nil {
		return HomeManagedSkillSnapshot{}, fmt.Errorf("skills: open Home catalog retained revision: %w", err)
	}
	if !exists {
		return HomeManagedSkillSnapshot{}, fs.ErrNotExist
	}
	return out, nil
}

type capturedManagedRevision struct{ files []HomeSkillFile }

func homeSkillTreeEntries(files []HomeSkillFile) []skillTreeEntry {
	entries := make([]skillTreeEntry, len(files))
	for i, file := range files {
		entries[i] = skillTreeEntry(file)
	}
	return entries
}

// captureManagedRevision collects one bounded immutable-revision manifest then
// reads every manifest file once. The direct link has already been inspected;
// this function receives only its pinned revision path.
func captureManagedRevision(ctx context.Context, filesystem sandbox.Filesystem, root string) (capturedManagedRevision, error) {
	entriesSeen := 0
	manifest, err := collectManagedTree(ctx, filesystem, root, "", 0, &entriesSeen, nil)
	if err != nil {
		return capturedManagedRevision{}, err
	}
	if len(manifest) < 2 { // Avoid a negative capacity below and require both control files.
		return capturedManagedRevision{}, invalidManagedRevision(errors.New("managed revision lacks required control files"))
	}
	sort.Slice(manifest, func(i, j int) bool { return manifest[i].path < manifest[j].path })
	seen := make(map[string]struct{}, len(manifest))
	var total int64
	main, metadata := false, false
	for _, file := range manifest {
		if _, duplicate := seen[file.path]; duplicate {
			return capturedManagedRevision{}, invalidManagedRevision(fmt.Errorf("managed revision repeats file %q", file.path))
		}
		seen[file.path] = struct{}{}
		if file.size < 0 || file.size > maxManagedFileBytes || file.mode&^fs.FileMode(0o777) != 0 {
			return capturedManagedRevision{}, invalidManagedRevision(fmt.Errorf("managed revision file %q has invalid size or mode", file.path))
		}
		if total > maxManagedTreeBytes-file.size {
			return capturedManagedRevision{}, invalidManagedRevision(errors.New("managed revision exceeds content limit"))
		}
		total += file.size
		switch file.path {
		case MainFile:
			if file.mode != 0o644 {
				return capturedManagedRevision{}, invalidManagedRevision(errors.New("managed revision SKILL.md mode is not regular 0644"))
			}
			main = true
		case skillMetadataFile:
			if file.mode != 0o644 || file.size > maxCatalogMetadataBytes {
				return capturedManagedRevision{}, invalidManagedRevision(errors.New("managed revision metadata is not regular 0644 within catalog limit"))
			}
			metadata = true
		}
	}
	if !main || !metadata {
		return capturedManagedRevision{}, invalidManagedRevision(errors.New("managed revision lacks required control files"))
	}
	files := make([]HomeSkillFile, 0, len(manifest))
	for _, file := range manifest {
		data, err := readCapturedManagedFile(ctx, filesystem, file)
		if err != nil {
			return capturedManagedRevision{}, err
		}
		files = append(files, HomeSkillFile{Path: file.path, Content: data, Mode: file.mode})
	}
	return capturedManagedRevision{files: files}, nil
}

func readCapturedManagedFile(ctx context.Context, filesystem sandbox.Filesystem, file managedTreeFile) ([]byte, error) {
	limit := file.size
	if limit == 0 {
		limit = 1 // Filesystems require a positive ceiling even for empty regular files.
	}
	reader, info, err := filesystem.Read(ctx, file.absolute, sandbox.ReadOptions{MaxBytes: limit})
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
	if info.Size != file.size || info.Mode != file.mode || info.Mode&^fs.FileMode(0o777) != 0 || int64(len(data)) != file.size {
		return nil, errors.New("managed revision file changed during capture")
	}
	return data, nil
}

func managedSnapshotFromCapture(id, scope, userID, agentID, name, digest string, captured capturedManagedRevision) (HomeManagedSkillSnapshot, error) {
	var main, metadata []byte
	files := make([]HomeSkillFile, 0, len(captured.files)-1)
	for _, file := range captured.files {
		switch file.Path {
		case MainFile:
			main = file.Content
			files = append(files, file)
		case skillMetadataFile:
			metadata = file.Content
		default:
			files = append(files, file)
		}
	}
	if main == nil || metadata == nil {
		return HomeManagedSkillSnapshot{}, errors.New("skills: captured managed revision lacks required control files")
	}
	envelope, err := decodeSkillMetadataEnvelope(metadata)
	if err != nil {
		return HomeManagedSkillSnapshot{}, fmt.Errorf("skills: decode managed Skill metadata: %w", err)
	}
	canonicalMetadata, err := encodeSkillMetadataEnvelope(envelope)
	if err != nil || string(canonicalMetadata) != string(metadata) {
		return HomeManagedSkillSnapshot{}, errors.New("skills: managed Skill metadata is not canonical v1")
	}
	frontmatter, err := parseFrontmatter(string(main)) // main is bounded by maxManagedFileBytes above.
	if err != nil {
		return HomeManagedSkillSnapshot{}, fmt.Errorf("skills: parse %s frontmatter: %w", name, err)
	}
	if frontmatter.Name == "" {
		frontmatter.Name = name
	}
	if err := skillNameValidationError(frontmatter.Name, name); err != nil {
		return HomeManagedSkillSnapshot{}, fmt.Errorf("skills: %s frontmatter: %w", name, err)
	}
	if strings.TrimSpace(frontmatter.Description) == "" {
		return HomeManagedSkillSnapshot{}, fmt.Errorf("skills: %s frontmatter description is required", name)
	}
	metadataJSON, err := canonicalJSON(envelope.Metadata)
	if err != nil {
		return HomeManagedSkillSnapshot{}, fmt.Errorf("skills: encode managed Skill metadata: %w", err)
	}
	treeFiles := make([]skillTreeEntry, 0, len(files))
	for _, file := range files {
		treeFiles = append(treeFiles, skillTreeEntry(file))
	}
	got, err := digestSkillTree(skillTree{Metadata: envelope, Files: treeFiles})
	if err != nil {
		return HomeManagedSkillSnapshot{}, fmt.Errorf("skills: digest captured managed Skill %q: %w", name, err)
	}
	if got != digest {
		return HomeManagedSkillSnapshot{}, errors.New("skills: managed revision changed during capture")
	}
	return HomeManagedSkillSnapshot{Skill: Skill{ID: id, Scope: scope, UserID: userID, AgentID: agentID, Name: name, Description: frontmatter.Description, Status: envelope.Status, DisableModelInvocation: envelope.DisableModelInvocation, Metadata: metadataJSON, CreatedAt: envelope.CreatedAt, UpdatedAt: envelope.UpdatedAt, Version: envelope.LegacyLifecycleVersion, ContentDigest: digest}, ContentDigest: digest, Files: files}, nil
}

func (c *HomeCatalog) useSkillByID(ctx context.Context, id string, use func(sandbox.Filesystem, FilesystemSkillDescriptor) error) error {
	scope, userID, agentID, name, err := decodeFilesystemSkillID(id)
	if err != nil {
		return err
	}
	root := HomeCatalogRoot{Scope: scope, UserID: userID, AgentID: agentID}
	skillRoot, err := homeCatalogSkillRoot(root)
	if err != nil {
		return err
	}
	exists, err := c.homes.UseExistingSkillFilesystem(ctx, skillRoot, func(filesystem sandbox.Filesystem) error {
		snapshot, err := c.snapshot(ctx, filesystem, root)
		if err != nil {
			return err
		}
		for _, descriptor := range append(snapshot.Active, snapshot.Deprecated...) {
			if descriptor.Skill.Name == name {
				return use(filesystem, descriptor)
			}
		}
		return fs.ErrNotExist
	})
	if err != nil {
		return fmt.Errorf("skills: open Home catalog: %w", err)
	}
	if !exists {
		return fs.ErrNotExist
	}
	return nil
}

func (c *HomeCatalog) listRoot(ctx context.Context, coordinate HomeCatalogRoot, includeDeprecated bool) ([]HomeCatalogSkill, error) {
	items := []HomeCatalogSkill{}
	err := c.useRoot(ctx, coordinate, func(filesystem sandbox.Filesystem) error {
		snapshot, err := c.snapshot(ctx, filesystem, coordinate)
		if err != nil {
			return err
		}
		descriptors := snapshot.Active
		if includeDeprecated {
			descriptors = append(descriptors, snapshot.Deprecated...)
		}
		for _, descriptor := range descriptors {
			items = append(items, homeCatalogSkill(descriptor))
		}
		return nil
	})
	return items, err
}

func (c *HomeCatalog) useRoot(ctx context.Context, coordinate HomeCatalogRoot, use func(sandbox.Filesystem) error) error {
	root, err := homeCatalogSkillRoot(coordinate)
	if err != nil {
		return err
	}
	exists, err := c.homes.UseExistingSkillFilesystem(ctx, root, use)
	// A ready owner Home can predate its optional Skill catalog directory.
	// Registry opening reports that exact absence before invoking use; lists
	// treat it as an empty root, while ID-addressed reads keep fs.ErrNotExist.
	if err != nil {
		if !exists && errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("skills: open Home catalog: %w", err)
	}
	if !exists {
		return nil
	}
	return nil
}

func (c *HomeCatalog) snapshot(ctx context.Context, filesystem sandbox.Filesystem, coordinate HomeCatalogRoot) (FilesystemCatalogSnapshot, error) {
	root, err := filesystemCatalogRoot(coordinate.Scope, coordinate.UserID, coordinate.AgentID)
	if err != nil {
		return FilesystemCatalogSnapshot{}, err
	}
	return SnapshotFilesystemCatalog(ctx, filesystem, root)
}

func homeCatalogSkillRoot(coordinate HomeCatalogRoot) (*home.SkillRoot, error) {
	switch coordinate.Scope {
	case "system":
		return home.SystemSkillCatalog(), validateHomeCatalogRoot(coordinate, "", "")
	case "system_agent":
		if err := validateHomeCatalogRoot(coordinate, "", coordinate.AgentID); err != nil {
			return nil, err
		}
		return home.SystemAgentSkillCatalog(coordinate.AgentID)
	case "user":
		if err := validateHomeCatalogRoot(coordinate, coordinate.UserID, ""); err != nil {
			return nil, err
		}
		return home.UserSkillCatalog(coordinate.UserID)
	case "user_agent":
		if err := validateHomeCatalogRoot(coordinate, coordinate.UserID, coordinate.AgentID); err != nil {
			return nil, err
		}
		return home.UserAgentSkillCatalog(coordinate.UserID, coordinate.AgentID)
	default:
		return nil, errors.New("skills: invalid Home catalog scope")
	}
}

func validateHomeCatalogRoot(root HomeCatalogRoot, userID, agentID string) error {
	if root.UserID != userID || root.AgentID != agentID {
		return errors.New("skills: invalid Home catalog owners")
	}
	return nil
}

func applicableHomeCatalogRoot(root HomeCatalogRoot) bool {
	_, err := homeCatalogSkillRoot(root)
	return err == nil
}

func homeCatalogSkill(d FilesystemSkillDescriptor) HomeCatalogSkill {
	contentDigest := d.Skill.ContentDigest
	if d.Managed {
		// Keep the compatibility field and the canonical read model pinned to the
		// same descriptor digest; callers must never observe two revisions here.
		contentDigest = d.Digest
	}
	return HomeCatalogSkill{Skill: Skill{ID: d.Skill.ID, Scope: d.Skill.Scope, UserID: d.Skill.UserID, AgentID: d.Skill.AgentID, Name: d.Skill.Name, Description: d.Skill.Description, Status: d.Skill.Status, DisableModelInvocation: d.Skill.DisableModelInvocation, Metadata: d.Skill.Metadata, CreatedAt: d.Skill.CreatedAt, UpdatedAt: d.Skill.UpdatedAt, Version: d.Skill.Version, ContentDigest: contentDigest}, Digest: d.Digest, Managed: d.Managed}
}

func catalogTreeFiles(ctx context.Context, filesystem sandbox.Filesystem, root string, includeControl bool) ([]string, error) {
	seen := 0
	entries, err := collectManagedTree(ctx, filesystem, root, "", 0, &seen, nil)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(entries))
	var total int64
	for _, entry := range entries {
		// Control metadata has its own small read bound and is excluded exactly
		// as verifyManagedRevision excludes it from the content-tree budget.
		if entry.path != skillMetadataFile {
			if entry.size < 0 || entry.size > maxManagedTreeBytes-total {
				return nil, errors.New("skills: catalog tree exceeds content limit")
			}
			total += entry.size
		}
		if !includeControl && entry.path == skillMetadataFile {
			continue
		}
		files = append(files, entry.path)
	}
	sort.Strings(files)
	return files, nil
}
