package resources

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	maxBuiltinSkills      = 128
	maxBuiltinFiles       = 4096
	maxBuiltinFilesPerKey = 1024
	maxBuiltinBytes       = 32 << 20
	maxBuiltinBytesPerKey = 8 << 20
)

// BuiltinSkillFile describes one immutable file relative to a skill root.
type BuiltinSkillFile struct {
	Path   string      `json:"path"`
	Digest string      `json:"digest"`
	Size   int64       `json:"size"`
	Mode   fs.FileMode `json:"mode"`
}

// BuiltinSkillDescriptor is the release-owned identity and complete file list
// for one builtin skill. Metadata retains nested frontmatter fields verbatim.
type BuiltinSkillDescriptor struct {
	Ref                    string             `json:"ref"`
	APIID                  string             `json:"api_id"`
	Name                   string             `json:"name"`
	Description            string             `json:"description"`
	Tags                   []string           `json:"tags"`
	DisableModelInvocation bool               `json:"disable_model_invocation"`
	Metadata               map[string]any     `json:"metadata"`
	Root                   string             `json:"root"`
	Files                  []BuiltinSkillFile `json:"files"`
	Digest                 string             `json:"digest"`
}

// BuiltinManifest is the generated, deterministic description of every
// release-owned builtin skill. Revision changes when any catalog byte or mode changes.
type BuiltinManifest struct {
	Revision string                   `json:"revision"`
	Skills   []BuiltinSkillDescriptor `json:"skills"`
}

// GenerateBuiltinManifest validates sourceRoot and returns its deterministic
// manifest. sourceRoot is the on-disk resources/skills directory, not a user path.
func GenerateBuiltinManifest(sourceRoot string) (BuiltinManifest, error) {
	info, err := os.Lstat(sourceRoot)
	if err != nil {
		return BuiltinManifest{}, fmt.Errorf("stat builtin skills root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return BuiltinManifest{}, fmt.Errorf("builtin skills root must be a directory: %s", sourceRoot)
	}

	var manifest BuiltinManifest
	var totalFiles int
	var totalBytes int64
	if err := discoverBuiltinSkills(sourceRoot, ".", &manifest.Skills, &totalFiles, &totalBytes); err != nil {
		return BuiltinManifest{}, err
	}
	if len(manifest.Skills) == 0 {
		return BuiltinManifest{}, fmt.Errorf("builtin skills root contains no SKILL.md: %s", sourceRoot)
	}
	if len(manifest.Skills) > maxBuiltinSkills || totalFiles > maxBuiltinFiles || totalBytes > maxBuiltinBytes {
		return BuiltinManifest{}, fmt.Errorf("builtin bundle exceeds ceilings: skills=%d/%d files=%d/%d bytes=%d/%d", len(manifest.Skills), maxBuiltinSkills, totalFiles, maxBuiltinFiles, totalBytes, maxBuiltinBytes)
	}
	sort.Slice(manifest.Skills, func(i, j int) bool { return manifest.Skills[i].Name < manifest.Skills[j].Name })
	seen := make(map[string]struct{}, len(manifest.Skills))
	for _, skill := range manifest.Skills {
		if _, ok := seen[skill.Name]; ok {
			return BuiltinManifest{}, fmt.Errorf("duplicate builtin skill name %q", skill.Name)
		}
		seen[skill.Name] = struct{}{}
	}

	revisionInput, err := json.Marshal(struct {
		Skills []BuiltinSkillDescriptor `json:"skills"`
	}{manifest.Skills})
	if err != nil {
		return BuiltinManifest{}, fmt.Errorf("encode builtin manifest: %w", err)
	}
	sum := sha256.Sum256(revisionInput)
	manifest.Revision = hex.EncodeToString(sum[:])
	return manifest, nil
}

func discoverBuiltinSkills(sourceRoot, relative string, skills *[]BuiltinSkillDescriptor, totalFiles *int, totalBytes *int64) error {
	dir := filepath.Join(sourceRoot, filepath.FromSlash(relative))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read builtin directory %q: %w", relative, err)
	}
	hasSkill := false
	for _, entry := range entries {
		if entry.Name() == "SKILL.md" {
			hasSkill = true
			break
		}
	}
	if hasSkill {
		skill, files, bytes, err := scanBuiltinSkill(sourceRoot, relative)
		if err != nil {
			return err
		}
		*skills = append(*skills, skill)
		*totalFiles += files
		*totalBytes += bytes
		return nil
	}

	for _, entry := range entries {
		child := path.Join(relative, entry.Name())
		if !canonicalBuiltinPath(child) {
			return fmt.Errorf("non-canonical builtin path %q", child)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("builtin path %q is a symlink", child)
		}
		if !entry.IsDir() {
			return fmt.Errorf("builtin path %q is not inside a skill root", child)
		}
		if err := discoverBuiltinSkills(sourceRoot, child, skills, totalFiles, totalBytes); err != nil {
			return err
		}
	}
	return nil
}

func scanBuiltinSkill(sourceRoot, root string) (BuiltinSkillDescriptor, int, int64, error) {
	if !canonicalBuiltinPath(root) {
		return BuiltinSkillDescriptor{}, 0, 0, fmt.Errorf("non-canonical builtin skill root %q", root)
	}
	var files []BuiltinSkillFile
	var totalBytes int64
	err := filepath.WalkDir(filepath.Join(sourceRoot, filepath.FromSlash(root)), func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(filepath.Join(sourceRoot, filepath.FromSlash(root)), filename)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !canonicalBuiltinPath(rel) {
			return fmt.Errorf("non-canonical builtin file path %q in %q", rel, root)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("builtin path %q/%q is a symlink", root, rel)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("builtin path %q/%q has unsupported type %s", root, rel, info.Mode().Type())
		}
		mode := info.Mode().Perm()
		if mode != 0o644 && mode != 0o755 {
			return fmt.Errorf("builtin path %q/%q has unsupported mode %04o", root, rel, mode)
		}
		if info.Size() > maxBuiltinBytesPerKey || totalBytes+info.Size() > maxBuiltinBytesPerKey {
			return fmt.Errorf("builtin skill %q exceeds %d byte ceiling", root, maxBuiltinBytesPerKey)
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		files = append(files, BuiltinSkillFile{Path: rel, Digest: hex.EncodeToString(sum[:]), Size: int64(len(data)), Mode: mode})
		totalBytes += int64(len(data))
		if len(files) > maxBuiltinFilesPerKey {
			return fmt.Errorf("builtin skill %q exceeds %d file ceiling", root, maxBuiltinFilesPerKey)
		}
		return nil
	})
	if err != nil {
		return BuiltinSkillDescriptor{}, 0, 0, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	skillFile := filepath.Join(sourceRoot, filepath.FromSlash(root), "SKILL.md")
	raw, err := os.ReadFile(skillFile)
	if err != nil {
		return BuiltinSkillDescriptor{}, 0, 0, fmt.Errorf("read %s: %w", root, err)
	}
	resource, err := parseResource(KindSkill, path.Base(root), string(raw))
	if err != nil {
		return BuiltinSkillDescriptor{}, 0, 0, fmt.Errorf("parse builtin skill %q: %w", root, err)
	}
	metadata, err := skillMetadata(string(raw))
	if err != nil {
		return BuiltinSkillDescriptor{}, 0, 0, fmt.Errorf("parse metadata for builtin skill %q: %w", root, err)
	}
	if resource.Name != path.Base(root) {
		return BuiltinSkillDescriptor{}, 0, 0, fmt.Errorf("builtin skill root %q does not match frontmatter name %q", root, resource.Name)
	}
	digest := builtinSkillDigest(files)
	return BuiltinSkillDescriptor{
		Ref:                    "builtin:" + resource.Name,
		APIID:                  "builtin-" + resource.Name,
		Name:                   resource.Name,
		Description:            resource.Description,
		Tags:                   append([]string(nil), resource.Tags...),
		DisableModelInvocation: boolMetadata(metadata, "disable_model_invocation"),
		Metadata:               resource.Metadata,
		Root:                   root,
		Files:                  files,
		Digest:                 digest,
	}, len(files), totalBytes, nil
}

func skillMetadata(raw string) (map[string]any, error) {
	normalized := strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	block, _, err := splitFrontmatter(normalized)
	if err != nil {
		return nil, err
	}
	metadata := map[string]any{}
	if block != "" {
		if err := yaml.Unmarshal([]byte(block), &metadata); err != nil {
			return nil, err
		}
	}
	return metadata, nil
}

func boolMetadata(metadata map[string]any, key string) bool {
	v, _ := metadata[key].(bool)
	return v
}

func builtinSkillDigest(files []BuiltinSkillFile) string {
	h := sha256.New()
	for _, file := range files {
		_, _ = fmt.Fprintf(h, "%s\x00%04o\x00%d\x00%s\n", file.Path, file.Mode.Perm(), file.Size, file.Digest)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func canonicalBuiltinPath(p string) bool {
	return p != "" && p != "." && !strings.Contains(p, "\\") && !strings.HasPrefix(p, "/") && path.Clean(p) == p && p != ".." && !strings.HasPrefix(p, "../")
}

func renderBuiltinManifest(manifest BuiltinManifest) ([]byte, error) {
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("encode builtin manifest: %w", err)
	}
	return []byte("// Code generated by internal/tools/generatebuiltinmanifest; DO NOT EDIT.\n\npackage resources\n\nconst builtinManifestJSON = " + strconv.Quote(string(encoded)) + "\n"), nil
}

// WriteBuiltinManifest writes the generated source only when its bytes differ,
// keeping generation checks and source mtimes deterministic.
func WriteBuiltinManifest(sourceRoot, output string) error {
	manifest, err := GenerateBuiltinManifest(sourceRoot)
	if err != nil {
		return err
	}
	rendered, err := renderBuiltinManifest(manifest)
	if err != nil {
		return err
	}
	if current, err := os.ReadFile(output); err == nil && string(current) == string(rendered) {
		return nil
	}
	return os.WriteFile(output, rendered, 0o644)
}

// GeneratedBuiltinManifest returns the manifest compiled into this binary.
func GeneratedBuiltinManifest() (BuiltinManifest, error) {
	var manifest BuiltinManifest
	if err := json.Unmarshal([]byte(builtinManifestJSON), &manifest); err != nil {
		return BuiltinManifest{}, fmt.Errorf("decode generated builtin manifest: %w", err)
	}
	return manifest, validateBuiltinManifest(manifest)
}

func validateBuiltinManifest(manifest BuiltinManifest) error {
	if len(manifest.Revision) != sha256.Size*2 {
		return fmt.Errorf("invalid builtin bundle revision %q", manifest.Revision)
	}
	if _, err := hex.DecodeString(manifest.Revision); err != nil {
		return fmt.Errorf("invalid builtin bundle revision %q: %w", manifest.Revision, err)
	}
	if len(manifest.Skills) == 0 || len(manifest.Skills) > maxBuiltinSkills {
		return fmt.Errorf("invalid builtin skill count %d", len(manifest.Skills))
	}
	seenNames := make(map[string]struct{}, len(manifest.Skills))
	seenRoots := make(map[string]struct{}, len(manifest.Skills))
	seenFiles := make(map[string]struct{})
	var totalFiles int
	var totalBytes int64
	for _, skill := range manifest.Skills {
		if skill.Ref != "builtin:"+skill.Name || skill.APIID != "builtin-"+skill.Name || !canonicalBuiltinPath(skill.Name) || strings.Contains(skill.Name, "/") || !canonicalBuiltinPath(skill.Root) || path.Base(skill.Root) != skill.Name || len(skill.Files) == 0 || len(skill.Files) > maxBuiltinFilesPerKey || len(skill.Digest) != sha256.Size*2 {
			return fmt.Errorf("invalid builtin skill descriptor %q", skill.Name)
		}
		if _, err := hex.DecodeString(skill.Digest); err != nil {
			return fmt.Errorf("invalid builtin skill descriptor %q: %w", skill.Name, err)
		}
		if _, exists := seenNames[skill.Name]; exists {
			return fmt.Errorf("duplicate builtin skill name %q", skill.Name)
		}
		if _, exists := seenRoots[skill.Root]; exists {
			return fmt.Errorf("duplicate builtin skill root %q", skill.Root)
		}
		seenNames[skill.Name] = struct{}{}
		seenRoots[skill.Root] = struct{}{}
		var skillBytes int64
		for _, file := range skill.Files {
			bundlePath := pathForBundle(skill.Root, file.Path)
			if !canonicalBuiltinPath(file.Path) || (file.Mode.Perm() != 0o644 && file.Mode.Perm() != 0o755) || file.Size < 0 || len(file.Digest) != sha256.Size*2 {
				return fmt.Errorf("invalid builtin file descriptor %q/%q", skill.Name, file.Path)
			}
			if _, err := hex.DecodeString(file.Digest); err != nil {
				return fmt.Errorf("invalid builtin file descriptor %q/%q: %w", skill.Name, file.Path, err)
			}
			if _, exists := seenFiles[bundlePath]; exists {
				return fmt.Errorf("duplicate builtin file path %q", bundlePath)
			}
			seenFiles[bundlePath] = struct{}{}
			skillBytes += file.Size
			totalFiles++
		}
		if skillBytes > maxBuiltinBytesPerKey {
			return fmt.Errorf("builtin skill %q exceeds %d byte ceiling", skill.Name, maxBuiltinBytesPerKey)
		}
		totalBytes += skillBytes
		if builtinSkillDigest(skill.Files) != skill.Digest {
			return fmt.Errorf("builtin skill descriptor %q has invalid digest", skill.Name)
		}
	}
	if totalFiles > maxBuiltinFiles || totalBytes > maxBuiltinBytes {
		return fmt.Errorf("builtin bundle exceeds ceilings: files=%d/%d bytes=%d/%d", totalFiles, maxBuiltinFiles, totalBytes, maxBuiltinBytes)
	}
	revisionInput, err := json.Marshal(struct {
		Skills []BuiltinSkillDescriptor `json:"skills"`
	}{manifest.Skills})
	if err != nil {
		return err
	}
	sum := sha256.Sum256(revisionInput)
	if manifest.Revision != hex.EncodeToString(sum[:]) {
		return fmt.Errorf("builtin manifest revision does not match descriptors")
	}
	return nil
}

func manifestSourceMode(mode fs.FileMode) fs.FileMode { return mode.Perm() }
