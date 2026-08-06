package skills

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"

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
	if err != nil {
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
