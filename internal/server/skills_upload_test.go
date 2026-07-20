package server

import (
	"archive/zip"
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type uploadArchiveFile struct {
	*bytes.Reader
}

func (uploadArchiveFile) Close() error { return nil }

func TestReadUploadedSkillArchiveAcceptsSkillAtArchiveRoot(t *testing.T) {
	archive := makeUploadArchive(t, map[string]string{
		"SKILL.md":          "---\nname: root-skill\ndescription: Root-level user skill\n---\n# Root Skill\n",
		"references/api.md": "notes",
	})

	up, err := readUploadedSkillArchive(
		uploadArchiveFile{Reader: bytes.NewReader(archive)},
		&multipart.FileHeader{Filename: "root-skill.zip"},
	)
	if err != nil {
		t.Fatalf("read root-level skill archive: %v", err)
	}
	if up.name != "root-skill" {
		t.Fatalf("skill name = %q, want root-skill", up.name)
	}
	if up.files["SKILL.md"] == "" || up.files["references/api.md"] != "notes" {
		t.Fatalf("uploaded files = %#v, want root-relative skill files", up.files)
	}
}

func TestParseUploadedSkillReturnsActionableArchiveError(t *testing.T) {
	archive := makeUploadArchive(t, map[string]string{
		"references/api.md": "notes",
	})
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "missing-skill-md.zip")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(archive); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/skills/upload", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	_, code, msg, parseErr := parseUploadedSkill(req)
	if code != http.StatusBadRequest || parseErr == nil {
		t.Fatalf("parse result = code %d, err %v; want 400 with error", code, parseErr)
	}
	if !strings.Contains(msg, "archive must contain SKILL.md") {
		t.Fatalf("client message = %q, want actionable missing SKILL.md reason", msg)
	}
}

func makeUploadArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		entry, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %q: %v", name, err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatalf("write zip entry %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func TestNormalizeUploadedSkillPathSkipsHiddenDirectories(t *testing.T) {
	paths := []string{
		"my-skill/.git/config",
		"my-skill/.git/objects/e2/f087be6e17ae5f0a8dbdf7fb208b77731ec41a",
		".git/config",
		"my-skill/.data/attachments/gmail_refund/591/image.png",
	}
	for _, input := range paths {
		name, skip, err := normalizeUploadedSkillPath(input)
		if err != nil {
			t.Fatalf("normalizeUploadedSkillPath(%q) error = %v", input, err)
		}
		if !skip {
			t.Fatalf("normalizeUploadedSkillPath(%q) skip = false, name = %q", input, name)
		}
	}
}

func TestNormalizeUploadedSkillPathDoesNotSkipAllowedDotOrGitNamedFiles(t *testing.T) {
	paths := []string{
		"my-skill/docs/git-notes.md",
		"my-skill/.env",
	}
	for _, input := range paths {
		name, skip, err := normalizeUploadedSkillPath(input)
		if err != nil {
			t.Fatalf("normalizeUploadedSkillPath(%q) error = %v", input, err)
		}
		if skip {
			t.Fatalf("normalizeUploadedSkillPath(%q) skipped allowed file", input)
		}
		if name != input {
			t.Fatalf("normalizeUploadedSkillPath(%q) name = %q", input, name)
		}
	}
}
