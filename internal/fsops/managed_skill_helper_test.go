package fsops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

func TestServePublishesNestedManagedSkillFromSortedManifestAndBody(t *testing.T) {
	root := t.TempDir()
	files := managedSkillFiles("body")
	digest, err := sandbox.DigestManagedSkillTreeV1(files)
	if err != nil {
		t.Fatal(err)
	}
	req := publishHelperRequest("nested/catalog", "skill", digest, files)
	var out bytes.Buffer
	if err := Serve(context.Background(), root, io.MultiReader(bytes.NewReader(req), strings.NewReader("{}\nbodyblob")), &out); err != nil {
		t.Fatal(err)
	}
	response, err := DecodeResponse(&out, KindMutation)
	if err != nil || response.Error != "" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	for _, file := range files {
		got, err := os.ReadFile(filepath.Join(root, "nested/catalog", ".stella-revisions", "skill", digest, filepath.FromSlash(file.Path)))
		if err != nil {
			t.Fatal(err)
		}
		stream, _ := file.Open()
		want, _ := io.ReadAll(stream)
		_ = stream.Close()
		if !bytes.Equal(got, want) {
			t.Fatalf("%s=%q want %q", file.Path, got, want)
		}
	}
}

func TestServeRejectsUnsortedOrDuplicateManifestBeforeBody(t *testing.T) {
	for _, tc := range []struct {
		name     string
		manifest []ManagedSkillWireFile
	}{
		{"unsorted", []ManagedSkillWireFile{{Path: "SKILL.md", Mode: 0o644, Length: 4}, {Path: ".stella-skill.json", Mode: 0o644, Length: 3}}},
		{"duplicate", []ManagedSkillWireFile{{Path: ".stella-skill.json", Mode: 0o644, Length: 3}, {Path: ".stella-skill.json", Mode: 0o644, Length: 3}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(Request{Version: ProtocolVersion, Operation: "publish_managed_skill", Path: "skill", CatalogRoot: ".", Digest: strings.Repeat("a", 64), Files: tc.manifest, BodyLength: 6})
			if err != nil {
				t.Fatal(err)
			}
			var req bytes.Buffer
			if err := writeFrame(&req, payload); err != nil {
				t.Fatal(err)
			}
			if err := Serve(context.Background(), t.TempDir(), io.MultiReader(&req, strings.NewReader("ignored")), io.Discard); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}

func TestServeShortAndExtraBodiesNeverTrustTarget(t *testing.T) {
	files := managedSkillFiles("body")
	digest, err := sandbox.DigestManagedSkillTreeV1(files)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, body string }{{"short", "{}\nbody"}, {"extra", "{}\nbodyblobX"}} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			req := publishHelperRequest("nested/catalog", "skill", digest, files)
			var out bytes.Buffer
			if err := Serve(context.Background(), root, io.MultiReader(bytes.NewReader(req), strings.NewReader(tc.body)), &out); err != nil {
				t.Fatal(err)
			}
			response, err := DecodeResponse(&out, KindMutation)
			if err != nil {
				t.Fatal(err)
			}
			if response.Error == "" {
				t.Fatalf("%s body accepted", tc.name)
			}
			if _, err := os.Lstat(filepath.Join(root, "nested/catalog", "skill")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s trusted target exists: %v", tc.name, err)
			}
		})
	}
}

func TestOutcomeUnknownErrorCodeRoundTrips(t *testing.T) {
	original := errors.Join(errors.New("published"), sandbox.ErrOutcomeUnknown)
	response := Response{Version: ProtocolVersion, Kind: KindMutation, Error: original.Error(), ErrorCode: classifyErrorCode(original)}
	var frame bytes.Buffer
	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(&frame, payload); err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeResponse(&frame, KindMutation)
	if err != nil {
		t.Fatal(err)
	}
	if !sandbox.IsOutcomeUnknown(ResponseError(decoded)) {
		t.Fatalf("round trip lost outcome unknown: %v", ResponseError(decoded))
	}
}

func publishHelperRequest(root, name, digest string, files []sandbox.ManagedSkillTreeEntry) []byte {
	wire := make([]ManagedSkillWireFile, 0, len(files))
	var total int64
	for _, file := range files {
		wire = append(wire, ManagedSkillWireFile{Path: file.Path, Mode: file.Mode, Length: file.Length})
		total += file.Length
	}
	encoded, err := EncodeRequest(Request{Version: ProtocolVersion, Operation: "publish_managed_skill", Path: name, CatalogRoot: root, Digest: digest, Files: wire, BodyLength: total})
	if err != nil {
		panic(err)
	}
	return encoded
}
