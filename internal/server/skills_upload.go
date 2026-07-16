package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/CherryHQ/stella/internal/skills"
)

const (
	maxSkillUploadZipBytes      = 32 << 20
	maxSkillUploadExpandedBytes = 32 << 20
)

type uploadedSkill struct {
	name                   string
	description            string
	disableModelInvocation bool
	metadata               json.RawMessage
	files                  map[string]string
}

type uploadedSkillFrontmatter struct {
	Name                   string `yaml:"name"`
	Description            string `yaml:"description"`
	CreatedAt              string `yaml:"created-at"`
	DisableModelInvocation bool   `yaml:"disable-model-invocation"`
}

func (s *Server) uploadAgentSkill(w http.ResponseWriter, r *http.Request, agentID string) {
	up, code, msg, err := parseUploadedSkill(r)
	if code != 0 {
		if err != nil {
			s.log.Warn("skill upload rejected", "agent_id", agentID, "status", code, "message", msg, "error", err)
		}
		writeError(w, code, msg)
		return
	}
	scope := r.FormValue("scope")
	if scope == "" {
		scope = defaultAgentSkillScope(r.Context())
	}
	userID, code, msg := s.agentSkillWriteScope(r.Context(), agentID, scope)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	sk := skills.Skill{
		Scope:                  scope,
		Name:                   up.name,
		Description:            up.description,
		Status:                 skills.SkillStatusActive,
		DisableModelInvocation: up.disableModelInvocation,
		Metadata:               up.metadata,
	}
	sk.UserID, sk.AgentID = skillScopeOwner(scope, userID, agentID)
	skillID, err := s.skillStore().Create(r.Context(), sk, up.files)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusCreated, map[string]string{"id": skillID, "name": up.name})
}

func parseUploadedSkill(r *http.Request) (*uploadedSkill, int, string, error) {
	r.Body = http.MaxBytesReader(nil, r.Body, maxSkillUploadZipBytes+(1<<20))
	if err := r.ParseMultipartForm(maxSkillUploadZipBytes); err != nil {
		return nil, http.StatusBadRequest, "invalid multipart form", err
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	file, header, err := r.FormFile("file")
	if err != nil {
		return nil, http.StatusBadRequest, "file is required", err
	}
	defer func() { _ = file.Close() }()
	up, err := readUploadedSkillArchive(file, header)
	if err != nil {
		return nil, http.StatusBadRequest, "invalid skill archive", err
	}
	return up, 0, "", nil
}

func readUploadedSkillArchive(file multipart.File, header *multipart.FileHeader) (*uploadedSkill, error) {
	if header == nil || !strings.EqualFold(filepath.Ext(header.Filename), ".zip") {
		return nil, fmt.Errorf("file must be a .zip archive")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSkillUploadZipBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	if int64(len(data)) > maxSkillUploadZipBytes {
		return nil, fmt.Errorf("archive exceeds %d MiB", maxSkillUploadZipBytes>>20)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("invalid zip archive: %w", err)
	}

	entries := make(map[string]*zip.File)
	roots := map[string]struct{}{}
	for _, f := range zr.File {
		name, skip, err := normalizeUploadedSkillPath(f.Name)
		if err != nil {
			return nil, err
		}
		if skip || f.FileInfo().IsDir() {
			continue
		}
		entries[name] = f
		if path.Base(name) == skills.MainFile {
			roots[path.Dir(name)] = struct{}{}
		}
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("archive must contain exactly one skill folder with SKILL.md")
	}
	if len(roots) > 1 {
		return nil, fmt.Errorf("archive must contain exactly one skill folder with SKILL.md")
	}

	var root string
	for candidate := range roots {
		root = candidate
	}
	if root == "." || root == "" {
		return nil, fmt.Errorf("archive must contain a skill folder with SKILL.md")
	}

	files := make(map[string]string, len(entries))
	prefix := root + "/"
	var expanded int64
	for name, entry := range entries {
		if !strings.HasPrefix(name, prefix) {
			return nil, fmt.Errorf("archive must contain only one skill folder")
		}
		rel := strings.TrimPrefix(name, prefix)
		if rel == "" || strings.Contains(rel, "../") || strings.HasPrefix(rel, "/") {
			return nil, fmt.Errorf("invalid archive path %q", name)
		}
		content, n, err := readZipEntry(entry, maxSkillUploadExpandedBytes-expanded)
		if err != nil {
			return nil, err
		}
		expanded += n
		if expanded > maxSkillUploadExpandedBytes {
			return nil, fmt.Errorf("archive contents exceed %d MiB", maxSkillUploadExpandedBytes>>20)
		}
		files[rel] = content
	}

	mainContent := files[skills.MainFile]
	if mainContent == "" {
		return nil, fmt.Errorf("archive is missing SKILL.md")
	}
	fm, err := parseUploadedSkillFrontmatter(mainContent)
	if err != nil {
		return nil, fmt.Errorf("parse SKILL.md: %w", err)
	}
	name := strings.TrimSpace(fm.Name)
	if name == "" {
		name = path.Base(root)
	}
	if errs := skills.ValidateSkillName(name, path.Base(root)); len(errs) > 0 {
		return nil, fmt.Errorf("invalid skill name %q: %s", name, strings.Join(errs, "; "))
	}
	createdAt := strings.TrimSpace(fm.CreatedAt)
	if createdAt == "" {
		createdAt = time.Now().UTC().Format(time.RFC3339)
	}
	meta, err := json.Marshal(map[string]string{"created-at": createdAt})
	if err != nil {
		return nil, fmt.Errorf("marshal metadata: %w", err)
	}
	return &uploadedSkill{
		name:                   name,
		description:            fm.Description,
		disableModelInvocation: fm.DisableModelInvocation,
		metadata:               meta,
		files:                  files,
	}, nil
}

func normalizeUploadedSkillPath(name string) (string, bool, error) {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	name = strings.TrimPrefix(name, "./")
	if name == "" {
		return "", true, nil
	}
	if strings.HasPrefix(name, "__MACOSX/") || path.Base(name) == ".DS_Store" {
		return "", true, nil
	}
	clean := path.Clean(name)
	if clean == "." {
		return "", true, nil
	}
	if strings.HasPrefix(clean, "../") || clean == ".." || strings.HasPrefix(clean, "/") {
		return "", false, fmt.Errorf("invalid archive path %q", name)
	}
	if hasHiddenUploadedSkillDirectorySegment(clean) {
		return "", true, nil
	}
	return clean, false, nil
}

func hasHiddenUploadedSkillDirectorySegment(name string) bool {
	parts := strings.Split(name, "/")
	for _, part := range parts[:len(parts)-1] {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

func readZipEntry(entry *zip.File, remaining int64) (string, int64, error) {
	if remaining <= 0 {
		return "", 0, fmt.Errorf("archive contents exceed %d MiB", maxSkillUploadExpandedBytes>>20)
	}
	rc, err := entry.Open()
	if err != nil {
		return "", 0, fmt.Errorf("read %q: %w", entry.Name, err)
	}
	defer func() { _ = rc.Close() }()
	limit := remaining + 1
	data, err := io.ReadAll(io.LimitReader(rc, limit))
	if err != nil {
		return "", 0, fmt.Errorf("read %q: %w", entry.Name, err)
	}
	if int64(len(data)) > remaining {
		return "", 0, fmt.Errorf("archive contents exceed %d MiB", maxSkillUploadExpandedBytes>>20)
	}
	return string(data), int64(len(data)), nil
}

func parseUploadedSkillFrontmatter(content string) (uploadedSkillFrontmatter, error) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	if !strings.HasPrefix(content, "---\n") {
		return uploadedSkillFrontmatter{}, fmt.Errorf("no frontmatter")
	}
	rest := strings.TrimPrefix(content, "---\n")
	yamlStr, _, ok := strings.Cut(rest, "\n---")
	if !ok {
		return uploadedSkillFrontmatter{}, fmt.Errorf("no closing frontmatter delimiter")
	}
	var fm uploadedSkillFrontmatter
	if err := yaml.Unmarshal([]byte(yamlStr), &fm); err != nil {
		return uploadedSkillFrontmatter{}, fmt.Errorf("invalid yaml: %w", err)
	}
	return fm, nil
}
