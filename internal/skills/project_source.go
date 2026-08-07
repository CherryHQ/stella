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
	"time"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

// projectSkillSource hides the project tier's storage coordinate from resolution.
// The host implementation preserves the legacy API; runners use only filesystemProjectSource.
type projectSkillSource interface {
	list(context.Context) ([]pkgplugins.Skill, map[string]string, error)
	load(context.Context, string, string) (string, error)
}

type hostProjectSource struct{ root string }

func (s hostProjectSource) list(context.Context) ([]pkgplugins.Skill, map[string]string, error) {
	return ListProjectSkills(s.root)
}

func (hostProjectSource) load(_ context.Context, dir, file string) (string, error) {
	return loadProjectSkillFile(dir, file)
}

type filesystemProjectSource struct {
	filesystem sandbox.Filesystem
	root       string
}

func newFilesystemProjectSource(filesystem sandbox.Filesystem, root string) (filesystemProjectSource, error) {
	if filesystem == nil {
		return filesystemProjectSource{}, errors.New("skills: filesystem is required")
	}
	if root == "" {
		return filesystemProjectSource{filesystem: filesystem}, nil
	}
	if !sandbox.IsCanonicalFilesystemPath(root) || (root != sandbox.PathWorkspace && !strings.HasPrefix(root, sandbox.PathWorkspace+"/")) {
		return filesystemProjectSource{}, fmt.Errorf("skills: project root %q is not under /workspace", root)
	}
	return filesystemProjectSource{filesystem: filesystem, root: root}, nil
}

func validProjectEntry(e sandbox.DirEntry) error {
	if e.Name == "" || e.Name == "." || e.Name == ".." || strings.ContainsAny(e.Name, "/\\\x00") {
		return fmt.Errorf("invalid project skill entry %q", e.Name)
	}
	if e.Mode&fs.ModeSymlink != 0 || e.IsDir != e.Mode.IsDir() || (!e.IsDir && !e.Mode.IsRegular()) {
		return fmt.Errorf("project skill entry %q is not a regular file or directory", e.Name)
	}
	return nil
}

func (s filesystemProjectSource) list(ctx context.Context) ([]pkgplugins.Skill, map[string]string, error) {
	if s.root == "" {
		return nil, nil, nil
	}
	base := path.Join(s.root, ".agents", "skills")
	var out []pkgplugins.Skill
	dirs := make(map[string]string)
	var walk func(string, bool) error
	walk = func(directory string, top bool) error {
		entries, err := s.filesystem.List(ctx, directory)
		if err != nil {
			if top && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
		for _, entry := range entries {
			if err := validProjectEntry(entry); err != nil {
				return err
			}
			if !entry.IsDir {
				continue
			}
			if strings.HasPrefix(entry.Name, ".") || entry.Name == "node_modules" {
				continue
			}
			dir := path.Join(directory, entry.Name)
			data, err := readProjectSkillFile(ctx, s.filesystem, path.Join(dir, pkgplugins.SkillMainFile))
			if err != nil {
				if !errors.Is(err, fs.ErrNotExist) {
					return err
				}
				if err := walk(dir, false); err != nil {
					return err
				}
				continue // a missing SKILL.md is a container, not a skill
			}
			fm, err := parseFrontmatter(data)
			if err != nil {
				continue
			}
			name := strings.TrimSpace(fm.Name)
			if name == "" {
				name = entry.Name
			}
			if strings.TrimSpace(fm.Description) == "" || name != entry.Name {
				continue
			}
			out = append(out, pkgplugins.Skill{ID: "project:" + dir, Scope: "project", Name: name, Description: fm.Description, Status: SkillStatusActive, DisableModelInvocation: fm.DisableModelInvocation, CreatedAt: time.Time{}})
			dirs[name] = dir
		}
		return nil
	}
	if err := walk(base, true); err != nil {
		return nil, nil, err
	}
	return out, dirs, nil
}

func readProjectSkillFile(ctx context.Context, filesystem sandbox.Filesystem, filename string) (string, error) {
	reader, info, err := filesystem.Read(ctx, filename, sandbox.ReadOptions{MaxBytes: maxCatalogSkillBytes})
	if err != nil {
		return "", err
	}
	if reader == nil {
		return "", errors.New("skills: filesystem returned a nil reader")
	}
	data, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		return "", errors.Join(readErr, closeErr)
	}
	if info.Mode&fs.ModeSymlink != 0 || !info.Mode.IsRegular() {
		return "", errors.New("skills: project Skill file is not regular")
	}
	if int64(len(data)) > maxCatalogSkillBytes {
		return "", sandbox.ErrReadLimit
	}
	if int64(len(data)) != info.Size {
		return "", errors.New("skills: project Skill file changed during read")
	}
	return string(data), nil
}

func (s filesystemProjectSource) load(ctx context.Context, dir, file string) (string, error) {
	if err := validateSkillTreePath(file); err != nil {
		return "", err
	}
	if dir == "" || !strings.HasPrefix(dir, s.root+"/") || !sandbox.IsCanonicalFilesystemPath(dir) {
		return "", errors.New("skills: invalid project skill directory")
	}
	return readProjectSkillFile(ctx, s.filesystem, path.Join(dir, file))
}
