package skills

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/home"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

const (
	ProjectSnapshotMaxFiles    = 512
	ProjectSnapshotMaxFileSize = int64(1 << 20)
	ProjectSnapshotMaxBytes    = int64(16 << 20)
	ProjectSnapshotMaxEntries  = 4096
	ProjectSnapshotMaxDepth    = 64
	projectSnapshotListLimit   = 1024
)

var ErrProjectSnapshotLimit = errors.New("skills: project snapshot limit exceeded")

// ProjectSnapshot is an immutable, bounded copy of project Skills. It contains
// logical sandbox identities and bytes only; it never retains a root capability
// or a provider/host pathname.
type ProjectSnapshot struct {
	logicalRoot string
	skills      []pkgplugins.Skill
	dirs        map[string]string
	files       map[string]string
}

// SnapshotProjectSkills consumes an already-authorized agent-workspace root.
// projectPath must be its canonical relative ProjectDescriptor.Path.
func SnapshotProjectSkills(ctx context.Context, root home.RootOperations, projectPath string) (*ProjectSnapshot, error) {
	if root == nil {
		return nil, errors.New("skills: project root is required")
	}
	projectPath, err := canonicalProjectPath(projectPath)
	if err != nil {
		return nil, err
	}
	s := &ProjectSnapshot{logicalRoot: path.Join("/workspace", projectPath, ".agents/skills"), dirs: map[string]string{}, files: map[string]string{}}
	base := path.Join(projectPath, ".agents/skills")
	if projectPath == "." {
		base = ".agents/skills"
	}
	fileCount, entryCount := 0, 0
	var total int64
	var walk func(string, int) error
	walk = func(dir string, depth int) error {
		if depth > ProjectSnapshotMaxDepth {
			return ErrProjectSnapshotLimit
		}
		entries, err := root.List(ctx, dir, home.ListOptions{Limit: projectSnapshotListLimit})
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			entryCount++
			if entryCount > ProjectSnapshotMaxEntries {
				return ErrProjectSnapshotLimit
			}
			name := entry.Name()
			if name == "" || name == "." || strings.Contains(name, "/") {
				return fs.ErrPermission
			}
			child := path.Join(dir, name)
			if entry.IsDir() {
				if strings.HasPrefix(name, ".") || name == "node_modules" {
					continue
				}
				if err := walk(child, depth+1); err != nil {
					return err
				}
				continue
			}
			fileCount++
			if fileCount > ProjectSnapshotMaxFiles {
				return ErrProjectSnapshotLimit
			}
			var data bytes.Buffer
			if err := root.Read(ctx, child, &data, home.ReadOptions{MaxBytes: ProjectSnapshotMaxFileSize}); err != nil {
				return err
			}
			total += int64(data.Len())
			if total > ProjectSnapshotMaxBytes {
				return ErrProjectSnapshotLimit
			}
			rel, ok := strings.CutPrefix(child, base+"/")
			if !ok || !canonicalRelative(rel) {
				return fs.ErrPermission
			}
			s.files[rel] = data.String()
		}
		return nil
	}
	if err := walk(base, 0); err != nil {
		return nil, fmt.Errorf("snapshot project skills: %w", err)
	}
	for file, data := range s.files {
		if path.Base(file) != pkgplugins.SkillMainFile {
			continue
		}
		dir := path.Dir(file)
		name := path.Base(dir)
		fm, err := parseFrontmatter(data)
		if err != nil || strings.TrimSpace(fm.Description) == "" {
			continue
		}
		skillName := strings.TrimSpace(fm.Name)
		if skillName == "" {
			skillName = name
		}
		if skillName != name {
			continue
		}
		s.skills = append(s.skills, pkgplugins.Skill{ID: "project:" + dir, Scope: "project", Name: name, Description: fm.Description, Status: SkillStatusActive, DisableModelInvocation: fm.DisableModelInvocation, CreatedAt: time.Time{}})
		s.dirs[name] = dir
	}
	sort.Slice(s.skills, func(i, j int) bool { return s.skills[i].Name < s.skills[j].Name })
	return s, nil
}

func canonicalProjectPath(p string) (string, error) {
	if p == "" {
		return "", errors.New("skills: project path is required")
	}
	if strings.Contains(p, `\`) || !canonicalRelativeAllowDot(p) {
		return "", fs.ErrPermission
	}
	return p, nil
}
func canonicalRelative(p string) bool { return p != "" && p != "." && canonicalRelativeAllowDot(p) }
func canonicalRelativeAllowDot(p string) bool {
	return !path.IsAbs(p) && path.Clean(p) == p && p != ".." && !strings.HasPrefix(p, "../")
}

func (s *ProjectSnapshot) list() ([]pkgplugins.Skill, map[string]string) {
	if s == nil {
		return nil, nil
	}
	return append([]pkgplugins.Skill(nil), s.skills...), s.dirs
}

func (s *ProjectSnapshot) load(name, file string) (string, string, error) {
	dir, ok := s.dirs[name]
	if !ok {
		return "", "", fs.ErrNotExist
	}
	if file == "" {
		file = pkgplugins.SkillMainFile
	}
	if !canonicalRelative(file) {
		return "", "", fs.ErrPermission
	}
	data, ok := s.files[path.Join(dir, file)]
	if !ok {
		return "", "", fs.ErrNotExist
	}
	return data, path.Join(s.logicalRoot, dir), nil
}

func (s *ProjectSnapshot) listFiles(name string) ([]string, string, error) {
	dir, ok := s.dirs[name]
	if !ok {
		return nil, "", fs.ErrNotExist
	}
	prefix := dir + "/"
	var out []string
	for file := range s.files {
		if rel, ok := strings.CutPrefix(file, prefix); ok {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out, path.Join(s.logicalRoot, dir), nil
}
