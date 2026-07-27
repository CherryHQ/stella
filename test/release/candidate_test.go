package release

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildAndVerifyCandidateManifest(t *testing.T) {
	root := t.TempDir()
	distDir, digestDir, run := writeCandidateFixture(t, root)
	manifest, err := BuildCandidateManifest(
		root,
		distDir,
		digestDir,
		run,
		CandidateSource{ArtifactName: "candidate-goreleaser", ArtifactID: "123"},
		defaultCandidateImage,
		time.Date(2026, time.July, 27, 10, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 14 || len(manifest.Images) != 2 {
		t.Fatalf("unexpected candidate shape: files=%d images=%d", len(manifest.Files), len(manifest.Images))
	}
	if err := VerifyCandidateManifest(root, manifest, run); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(root, filepath.FromSlash(candidatePath(run, "linux", "amd64", ".tar.gz")))
	if err := os.WriteFile(archive, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyCandidateManifest(root, manifest, run); err == nil ||
		(!strings.Contains(err.Error(), "size changed") && !strings.Contains(err.Error(), "SHA-256 changed")) {
		t.Fatalf("expected candidate tamper detection, got %v", err)
	}
}

func TestBuildCandidateManifestRejectsDifferentCommit(t *testing.T) {
	root := t.TempDir()
	distDir, digestDir, run := writeCandidateFixture(t, root)
	run.Commit = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"

	_, err := BuildCandidateManifest(
		root,
		distDir,
		digestDir,
		run,
		CandidateSource{ArtifactName: "candidate-goreleaser", ArtifactID: "123"},
		defaultCandidateImage,
		time.Now(),
	)
	if err == nil || !strings.Contains(err.Error(), "metadata commit") {
		t.Fatalf("expected commit mismatch, got %v", err)
	}
}

func TestCandidateManifestRequiresCompleteArchiveMatrix(t *testing.T) {
	root := t.TempDir()
	distDir, digestDir, run := writeCandidateFixture(t, root)
	artifactsPath := filepath.Join(distDir, goreleaserArtifactsFile)
	artifacts := readArtifactFixture(t, artifactsPath)
	artifacts = artifacts[1:]
	writeJSONFixture(t, artifactsPath, artifacts)

	_, err := BuildCandidateManifest(
		root,
		distDir,
		digestDir,
		run,
		CandidateSource{ArtifactName: "candidate-goreleaser", ArtifactID: "123"},
		defaultCandidateImage,
		time.Now(),
	)
	if err == nil || !strings.Contains(err.Error(), "candidate archive for") {
		t.Fatalf("expected incomplete matrix error, got %v", err)
	}
}

func TestWriteCandidateManifestDoesNotReplaceExistingIdentity(t *testing.T) {
	root := t.TempDir()
	distDir, digestDir, run := writeCandidateFixture(t, root)
	manifest, err := BuildCandidateManifest(
		root,
		distDir,
		digestDir,
		run,
		CandidateSource{ArtifactName: "candidate-goreleaser", ArtifactID: "123"},
		defaultCandidateImage,
		time.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, RunRelativeDir(run.ID), "candidate.json")
	if err := WriteCandidateManifest(path, manifest); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCandidateManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Run != run {
		t.Fatalf("loaded Run = %+v, want %+v", loaded.Run, run)
	}
	if err := WriteCandidateManifest(path, manifest); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected no-replace error, got %v", err)
	}
}

func writeCandidateFixture(t *testing.T, root string) (string, string, Run) {
	t.Helper()
	run := Run{
		ID:      "candidate-fixture",
		Version: "v1.2.3",
		Commit:  "0123456789abcdef0123456789abcdef01234567",
	}
	distDir := filepath.Join(root, "dist")
	digestDir := filepath.Join(root, "docker-digests")
	if err := os.MkdirAll(filepath.Join(distDir, "homebrew", "Formula"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(digestDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var artifacts []goreleaserArtifact
	for _, platform := range []struct {
		goos   string
		goarch string
	}{
		{goos: "darwin", goarch: "amd64"},
		{goos: "darwin", goarch: "arm64"},
		{goos: "linux", goarch: "amd64"},
		{goos: "linux", goarch: "arm64"},
		{goos: "windows", goarch: "amd64"},
		{goos: "windows", goarch: "arm64"},
	} {
		path := candidatePath(run, platform.goos, platform.goarch, ".tar.gz")
		writeCandidateContent(t, root, path)
		artifacts = append(artifacts, goreleaserArtifact{
			Name:   filepath.Base(path),
			Path:   path,
			Type:   "Archive",
			GOOS:   platform.goos,
			GOARCH: platform.goarch,
		})
	}
	for _, platform := range []struct {
		goarch string
		ext    string
	}{
		{goarch: "amd64", ext: ".deb"},
		{goarch: "amd64", ext: ".rpm"},
		{goarch: "arm64", ext: ".deb"},
		{goarch: "arm64", ext: ".rpm"},
	} {
		path := candidatePath(run, "linux", platform.goarch, platform.ext)
		writeCandidateContent(t, root, path)
		artifacts = append(artifacts, goreleaserArtifact{
			Name:   filepath.Base(path),
			Path:   path,
			Type:   "Linux Package",
			GOOS:   "linux",
			GOARCH: platform.goarch,
		})
	}
	checksumPath := filepath.ToSlash(filepath.Join("dist", "stella_1.2.3_checksums.txt"))
	writeCandidateContent(t, root, checksumPath)
	artifacts = append(artifacts, goreleaserArtifact{
		Name: filepath.Base(checksumPath),
		Path: checksumPath,
		Type: "Checksum",
	})
	formulaPath := filepath.ToSlash(filepath.Join("dist", "homebrew", "Formula", "stella.rb"))
	writeCandidateContent(t, root, formulaPath)
	artifacts = append(artifacts, goreleaserArtifact{
		Name: filepath.Base(formulaPath),
		Path: formulaPath,
		Type: "Homebrew Formula",
	})
	writeJSONFixture(t, filepath.Join(distDir, goreleaserArtifactsFile), artifacts)
	writeJSONFixture(t, filepath.Join(distDir, goreleaserMetadataFile), goreleaserMetadata{
		Version: strings.TrimPrefix(run.Version, "v"),
		Commit:  run.Commit,
	})
	for _, image := range []struct {
		platform string
		digest   string
	}{
		{platform: "linux-amd64", digest: strings.Repeat("a", 64)},
		{platform: "linux-arm64", digest: strings.Repeat("b", 64)},
	} {
		if err := os.WriteFile(
			filepath.Join(digestDir, image.platform+".digest"),
			[]byte("sha256:"+image.digest+"\n"),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
	}
	return distDir, digestDir, run
}

func candidatePath(run Run, goos, goarch, extension string) string {
	return filepath.ToSlash(filepath.Join(
		"dist",
		"stella_"+strings.TrimPrefix(run.Version, "v")+"_"+goos+"_"+goarch+extension,
	))
}

func writeCandidateContent(t *testing.T, root, relative string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture "+relative), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readArtifactFixture(t *testing.T, path string) []goreleaserArtifact {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var artifacts []goreleaserArtifact
	if err := json.Unmarshal(data, &artifacts); err != nil {
		t.Fatal(err)
	}
	return artifacts
}
