package skills

import (
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"
)

func testManagedSkill() Skill {
	return Skill{
		ID: "skill-1", Scope: "user_agent", UserID: "user-1", AgentID: "agent-1",
		Name: "review-notes", Description: "Review notes", Status: SkillStatusActive,
		Metadata:  json.RawMessage(`{"source":{"revision":1},"created_by":"reflect"}`),
		CreatedAt: time.Date(2026, 8, 12, 1, 2, 3, 0, time.UTC),
		UpdatedAt: time.Date(2026, 8, 12, 2, 3, 4, 0, time.UTC), Version: 7,
	}
}

func testRevisionFiles() []revisionFile {
	return []revisionFile{
		{Path: MainFile, Mode: 0o644, Content: []byte("---\nname: review-notes\n---\n")},
		{Path: "scripts/check", Mode: 0o755, Content: []byte{0, 0xff, 'x'}},
	}
}

func TestManagedSkillManifestIsCanonicalAndStrict(t *testing.T) {
	skill := testManagedSkill()
	encoded, err := canonicalManifest(skill)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeCanonicalManifest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !sameSkillIdentity(decoded, skill) || decoded.Version != skill.Version || string(decoded.Metadata) != `{"created_by":"reflect","source":{"revision":1}}` {
		t.Fatalf("decoded manifest = %#v", decoded)
	}
	for name, input := range map[string][]byte{
		"trailing value": append(append([]byte(nil), encoded...), []byte(` {}`)...),
		"unknown field":  []byte(strings.Replace(string(encoded), `"agent_id"`, `"extra":true,"agent_id"`, 1)),
		"duplicate":      []byte(strings.Replace(string(encoded), `"agent_id"`, `"agent_id":"other","agent_id"`, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeCanonicalManifest(input); !errors.Is(err, ErrInvalidSkillRevision) {
				t.Fatalf("decode error = %v", err)
			}
		})
	}
}

func TestManagedSkillDigestIsStableAndSensitive(t *testing.T) {
	manifest, err := canonicalManifest(testManagedSkill())
	if err != nil {
		t.Fatal(err)
	}
	files := testRevisionFiles()
	want, err := digestRevision(manifest, files)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := digestRevision(manifest, []revisionFile{files[1], files[0]}); err != nil || got != want {
		t.Fatalf("reordered digest = %q, %v; want %q", got, err, want)
	}
	changed := testRevisionFiles()
	changed[1].Content = []byte("different")
	if got, err := digestRevision(manifest, changed); err != nil || got == want {
		t.Fatalf("changed digest = %q, %v", got, err)
	}
}

func TestManagedSkillRevisionRejectsUnsafeOrUnboundedTrees(t *testing.T) {
	for name, change := range map[string]func([]revisionFile){
		"absolute":     func(files []revisionFile) { files[1].Path = "/escape" },
		"traversal":    func(files []revisionFile) { files[1].Path = "../escape" },
		"backslash":    func(files []revisionFile) { files[1].Path = `dir\file` },
		"reserved":     func(files []revisionFile) { files[1].Path = ".stella-secret" },
		"duplicate":    func(files []revisionFile) { files[1].Path = MainFile },
		"symlink":      func(files []revisionFile) { files[1].Mode = fs.ModeSymlink | 0o777 },
		"missing main": func(files []revisionFile) { files[0].Path = "README.md" },
	} {
		t.Run(name, func(t *testing.T) {
			files := testRevisionFiles()
			change(files)
			if _, err := validateRevisionFiles(files); err == nil {
				t.Fatal("unsafe revision accepted")
			}
		})
	}
	large := testRevisionFiles()
	large[1].Content = make([]byte, MaxManagedSkillAggregateBytes)
	if _, err := validateRevisionFiles(large); !errors.Is(err, ErrSkillLimit) {
		t.Fatalf("aggregate limit = %v", err)
	}
}
