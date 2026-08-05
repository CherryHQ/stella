package skills

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
	"time"
)

func validSkillMetadataEnvelope() skillMetadataEnvelope {
	return skillMetadataEnvelope{
		Status:                 SkillStatusActive,
		Metadata:               map[string]any{"created_by": "reflect", "source": map[string]any{"install": json.Number("9007199254740993")}},
		CreatedAt:              time.Date(2026, 8, 2, 3, 4, 5, 0, time.UTC),
		UpdatedAt:              time.Date(2026, 8, 2, 3, 4, 6, 0, time.UTC),
		LegacyLifecycleVersion: 1,
	}
}

func validSkillTree() skillTree {
	return skillTree{
		Metadata: validSkillMetadataEnvelope(),
		Files: []skillTreeEntry{
			{Path: MainFile, Content: []byte("---\nname: test\ndescription: test\n---\n"), Mode: 0o644},
			{Path: "bin/run", Content: []byte{0, 0xff, 'x'}, Mode: 0o755},
		},
	}
}

func TestSkillMetadataEnvelopeDefaults(t *testing.T) {
	got := defaultSkillMetadataEnvelope()
	if got.Status != SkillStatusActive || got.DisableModelInvocation || got.Metadata == nil || len(got.Metadata) != 0 || !got.CreatedAt.IsZero() || !got.UpdatedAt.IsZero() || got.LegacyLifecycleVersion != 1 {
		t.Fatalf("unexpected defaults: %#v", got)
	}
}

func TestSkillMetadataEnvelopeCanonicalizesNestedObjectsAndNumbers(t *testing.T) {
	first := []byte(`{"schema_version":1,"status":"active","disable_model_invocation":false,"metadata":{"z":{"b":9007199254740993,"a":[{"y":2,"x":1}]},"a":"source"},"created_at":"2026-08-02T03:04:05Z","updated_at":"2026-08-02T03:04:06Z","legacy_lifecycle_version":1}`)
	second := []byte(`{"updated_at":"2026-08-02T03:04:06Z","metadata":{"a":"source","z":{"a":[{"x":1,"y":2}],"b":9007199254740993}},"status":"active","legacy_lifecycle_version":1,"created_at":"2026-08-02T03:04:05Z","disable_model_invocation":false,"schema_version":1}`)

	one, err := decodeSkillMetadataEnvelope(first)
	if err != nil {
		t.Fatal(err)
	}
	two, err := decodeSkillMetadataEnvelope(second)
	if err != nil {
		t.Fatal(err)
	}
	encodedOne, err := encodeSkillMetadataEnvelope(one)
	if err != nil {
		t.Fatal(err)
	}
	encodedTwo, err := encodeSkillMetadataEnvelope(two)
	if err != nil {
		t.Fatal(err)
	}
	if string(encodedOne) != string(encodedTwo) {
		t.Fatalf("equivalent metadata encoded differently:\n%s\n%s", encodedOne, encodedTwo)
	}
	if !strings.Contains(string(encodedOne), `9.007199254740993e15`) {
		t.Fatalf("large integer lost precision: %s", encodedOne)
	}
	if !strings.HasSuffix(string(encodedOne), "\n") || strings.Contains(string(encodedOne), "\n\n") {
		t.Fatalf("not compact newline-terminated JSON: %q", encodedOne)
	}
}

func TestCanonicalJSONNormalizesEquivalentNumbers(t *testing.T) {
	spellings := []string{"1", "1.0", "10e-1", "0.100e1"}
	var wantEnvelope, wantDigest string
	for _, spelling := range spellings {
		t.Run(spelling, func(t *testing.T) {
			envelope := validSkillMetadataEnvelope()
			envelope.Metadata = map[string]any{"number": json.Number(spelling)}
			encoded, err := encodeSkillMetadataEnvelope(envelope)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := digestSkillTree(skillTree{Metadata: envelope, Files: validSkillTree().Files})
			if err != nil {
				t.Fatal(err)
			}
			if wantEnvelope == "" {
				wantEnvelope, wantDigest = string(encoded), digest
				return
			}
			if string(encoded) != wantEnvelope {
				t.Fatalf("%s encoded differently:\n%s\n%s", spelling, encoded, wantEnvelope)
			}
			if digest != wantDigest {
				t.Fatalf("%s produced a different digest: %s != %s", spelling, digest, wantDigest)
			}
		})
	}
}

func TestNormalizeJSONNumberDoesNotExpandLargeExponents(t *testing.T) {
	const exponent = "1000000000000000000000000"
	cases := map[string]string{
		"positive": "1e" + exponent,
		"negative": "-1e-" + exponent,
		"zero":     "-0e" + exponent,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := normalizeJSONNumber(input)
			if err != nil {
				t.Fatal(err)
			}
			want := input
			if name == "zero" {
				want = "0"
			}
			if got != want {
				t.Fatalf("normalizeJSONNumber(%q) = %q, want %q", input, got, want)
			}
			if len(got) > len(input) {
				t.Fatalf("normalized exponent expanded: %d > %d", len(got), len(input))
			}
		})
	}
}

func TestDecodeSkillMetadataEnvelopeRejectsTrailingBytes(t *testing.T) {
	base := `{"schema_version":1,"status":"active","disable_model_invocation":false,"metadata":{},"created_at":"2026-08-02T03:04:05Z","updated_at":"2026-08-02T03:04:06Z","legacy_lifecycle_version":1}`
	for name, suffix := range map[string]string{
		"closing array":  "]",
		"closing object": "}",
		"scalar":         " true",
		"object":         " {}",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeSkillMetadataEnvelope([]byte(base + suffix)); err == nil {
				t.Fatal("expected trailing bytes to be rejected")
			}
		})
	}
	if _, err := decodeSkillMetadataEnvelope([]byte(base + " \t\n")); err != nil {
		t.Fatalf("whitespace-only suffix rejected: %v", err)
	}
}

func TestDecodeSkillMetadataEnvelopeRejectsInvalidInput(t *testing.T) {
	base := `{"schema_version":1,"status":"active","disable_model_invocation":false,"metadata":{},"created_at":"2026-08-02T03:04:05Z","updated_at":"2026-08-02T03:04:06Z","legacy_lifecycle_version":1}`
	cases := map[string]string{
		"unknown field":       strings.Replace(base, `"schema_version":1,`, `"schema_version":1,"extra":true,`, 1),
		"duplicate top key":   strings.Replace(base, `"status":"active",`, `"status":"active","status":"active",`, 1),
		"duplicate nested":    strings.Replace(base, `"metadata":{},`, `"metadata":{"a":1,"a":2},`, 1),
		"malformed":           `{`,
		"unknown version":     strings.Replace(base, `"schema_version":1`, `"schema_version":2`, 1),
		"unknown status":      strings.Replace(base, `"status":"active"`, `"status":"removed"`, 1),
		"non UTC":             strings.Replace(base, `2026-08-02T03:04:05Z`, `2026-08-02T04:04:05+01:00`, 1),
		"naive timestamp":     strings.Replace(base, `2026-08-02T03:04:05Z`, `2026-08-02T03:04:05`, 1),
		"updated before":      strings.Replace(base, `2026-08-02T03:04:06Z`, `2026-08-02T03:04:04Z`, 1),
		"metadata null":       strings.Replace(base, `"metadata":{}`, `"metadata":null`, 1),
		"metadata array":      strings.Replace(base, `"metadata":{}`, `"metadata":[]`, 1),
		"metadata scalar":     strings.Replace(base, `"metadata":{}`, `"metadata":1`, 1),
		"nonpositive version": strings.Replace(base, `"legacy_lifecycle_version":1`, `"legacy_lifecycle_version":0`, 1),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeSkillMetadataEnvelope([]byte(input)); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestEncodeSkillMetadataEnvelopeValidatesBeforeWriting(t *testing.T) {
	cases := map[string]func(*skillMetadataEnvelope){
		"unknown status": func(envelope *skillMetadataEnvelope) { envelope.Status = "unknown" },
		"nil metadata":   func(envelope *skillMetadataEnvelope) { envelope.Metadata = nil },
		"nonpositive lifecycle": func(envelope *skillMetadataEnvelope) {
			envelope.LegacyLifecycleVersion = 0
		},
		"non UTC timestamp": func(envelope *skillMetadataEnvelope) {
			envelope.CreatedAt = envelope.CreatedAt.In(time.FixedZone("offset", 3600))
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			envelope := validSkillMetadataEnvelope()
			change(&envelope)
			if _, err := encodeSkillMetadataEnvelope(envelope); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestDigestSkillTreeStableAndSensitive(t *testing.T) {
	tree := validSkillTree()
	first, err := digestSkillTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	reordered := tree
	reordered.Files = []skillTreeEntry{tree.Files[1], tree.Files[0]}
	second, err := digestSkillTree(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("file order changed digest: %s != %s", first, second)
	}
	const golden = "5349c8ced4e65232bc0aa82f98ae9f2e7c73586ca969de693b4c4c5d440e30d1"
	if first != golden {
		t.Fatalf("digest = %q, want %q", first, golden)
	}
	if len(first) != 64 || first != strings.ToLower(first) {
		t.Fatalf("digest is not lowercase SHA-256: %q", first)
	}

	changes := map[string]func(*skillTree){
		"binary content": func(tree *skillTree) { tree.Files[1].Content[1] = 0xfe },
		"mode":           func(tree *skillTree) { tree.Files[1].Mode = 0o644 },
		"metadata":       func(tree *skillTree) { tree.Metadata.Metadata["created_by"] = "manual" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			changed := validSkillTree()
			change(&changed)
			got, err := digestSkillTree(changed)
			if err != nil {
				t.Fatal(err)
			}
			if got == first {
				t.Fatal("one semantic change did not change digest")
			}
		})
	}
}

func TestDigestSkillTreeRejectsInvalidFiles(t *testing.T) {
	cases := map[string]func(*skillTree){
		"empty path":               func(tree *skillTree) { tree.Files[1].Path = "" },
		"absolute path":            func(tree *skillTree) { tree.Files[1].Path = "/file" },
		"noncanonical path":        func(tree *skillTree) { tree.Files[1].Path = "dir/../file" },
		"backslash path":           func(tree *skillTree) { tree.Files[1].Path = `dir\file` },
		"NUL path":                 func(tree *skillTree) { tree.Files[1].Path = "dir\x00file" },
		"traversal":                func(tree *skillTree) { tree.Files[1].Path = "../file" },
		"duplicate canonical path": func(tree *skillTree) { tree.Files[1].Path = MainFile },
		"directory":                func(tree *skillTree) { tree.Files[1].Mode = fs.ModeDir | 0o755 },
		"symlink":                  func(tree *skillTree) { tree.Files[1].Mode = fs.ModeSymlink | 0o777 },
		"device":                   func(tree *skillTree) { tree.Files[1].Mode = fs.ModeDevice | 0o600 },
		"missing SKILL.md":         func(tree *skillTree) { tree.Files[0].Path = "README.md" },
		"empty SKILL.md":           func(tree *skillTree) { tree.Files[0].Content = nil },
		"caller metadata file":     func(tree *skillTree) { tree.Files[1].Path = skillMetadataFile },
		"reserved revisions":       func(tree *skillTree) { tree.Files[1].Path = ".stella-revisions/revision/file" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			tree := validSkillTree()
			change(&tree)
			if _, err := digestSkillTree(tree); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}
