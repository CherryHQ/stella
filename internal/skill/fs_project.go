package skill

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/platform/home"
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
	skills []Skill
	dirs   map[string]string
	files  map[string]string
	modes  map[string]fs.FileMode
}

type immutableSkillFile struct {
	path    string
	content []byte
	mode    fs.FileMode
}

type immutableSkillProjection struct {
	kind   string
	id     string
	digest string
	files  []immutableSkillFile
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
	s := &ProjectSnapshot{
		dirs:  map[string]string{},
		files: map[string]string{},
		modes: map[string]fs.FileMode{},
	}
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
			info, err := entry.Info()
			if err != nil {
				return err
			}
			mode, err := immutableProjectionMode(info.Mode())
			if err != nil {
				return err
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
			s.modes[rel] = mode
		}
		return nil
	}
	if err := walk(base, 0); err != nil {
		return nil, fmt.Errorf("snapshot project skills: %w", err)
	}
	for file, data := range s.files {
		if path.Base(file) != MainFile {
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
		s.skills = append(s.skills, Skill{ID: "project:" + dir, Scope: "project", Name: name, Description: fm.Description, Status: SkillStatusActive, DisableModelInvocation: fm.DisableModelInvocation, CreatedAt: time.Time{}})
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

func (s *ProjectSnapshot) list() []Skill {
	if s == nil {
		return nil
	}
	return append([]Skill(nil), s.skills...)
}

func (s *ProjectSnapshot) load(name, file string) (string, error) {
	dir, ok := s.dirs[name]
	if !ok {
		return "", fs.ErrNotExist
	}
	if file == "" {
		file = MainFile
	}
	if !canonicalRelative(file) {
		return "", fs.ErrPermission
	}
	data, ok := s.files[path.Join(dir, file)]
	if !ok {
		return "", fs.ErrNotExist
	}
	return data, nil
}

func (s *ProjectSnapshot) listFiles(name string) ([]string, error) {
	dir, ok := s.dirs[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	prefix := dir + "/"
	var out []string
	for file := range s.files {
		if rel, ok := strings.CutPrefix(file, prefix); ok {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *ProjectSnapshot) immutableProjection(name string) (immutableSkillProjection, error) {
	dir, ok := s.dirs[name]
	if !ok {
		return immutableSkillProjection{}, fs.ErrNotExist
	}
	prefix := dir + "/"
	files := make([]immutableSkillFile, 0)
	for filename, content := range s.files {
		relative, ok := strings.CutPrefix(filename, prefix)
		if !ok {
			continue
		}
		mode, ok := s.modes[filename]
		if !ok {
			return immutableSkillProjection{}, fmt.Errorf("project skill %q file %q has no captured mode", name, relative)
		}
		files = append(files, immutableSkillFile{path: relative, content: []byte(content), mode: mode})
	}
	digest, err := immutableProjectionDigest(files)
	if err != nil {
		return immutableSkillProjection{}, err
	}
	return immutableSkillProjection{kind: "project", id: name, digest: digest, files: files}, nil
}

func immutableProjectionMode(mode fs.FileMode) (fs.FileMode, error) {
	if !mode.IsRegular() {
		return 0, fmt.Errorf("skills: immutable projection source has unsupported mode %s", mode.Type())
	}
	return 0o444 | mode.Perm()&0o111, nil
}

func immutableProjectionDigest(files []immutableSkillFile) (string, error) {
	files = append([]immutableSkillFile(nil), files...)
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	if len(files) == 0 {
		return "", errors.New("skills: immutable projection is empty")
	}
	seen := make(map[string]struct{}, len(files))
	hasMain := false
	hash := sha256.New()
	_, _ = hash.Write([]byte("stella-immutable-skill-v1\x00"))
	for _, file := range files {
		if !fs.ValidPath(file.path) || file.path == "." || file.mode != 0o444 && file.mode != 0o555 {
			return "", fmt.Errorf("skills: invalid immutable projection file %q", file.path)
		}
		if _, duplicate := seen[file.path]; duplicate {
			return "", fmt.Errorf("skills: duplicate immutable projection file %q", file.path)
		}
		seen[file.path] = struct{}{}
		hasMain = hasMain || file.path == MainFile
		writeDigestField(hash, []byte(file.path))
		var number [8]byte
		binary.BigEndian.PutUint64(number[:], uint64(file.mode))
		_, _ = hash.Write(number[:])
		binary.BigEndian.PutUint64(number[:], uint64(len(file.content)))
		_, _ = hash.Write(number[:])
		_, _ = hash.Write(file.content)
	}
	if !hasMain {
		return "", fmt.Errorf("skills: immutable projection is missing %s", MainFile)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
