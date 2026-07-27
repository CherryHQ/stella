package release

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// CandidateSchemaVersion is the current build-once candidate manifest
	// schema.
	CandidateSchemaVersion = 1
)

const (
	candidateArchive        = "archive"
	candidateLinuxPackage   = "linux_package"
	candidateChecksum       = "checksum"
	candidateHomebrew       = "homebrew_formula"
	candidateMetadata       = "metadata"
	defaultCandidateImage   = "ghcr.io/cherryhq/stella"
	goreleaserArtifactsFile = "artifacts.json"
	goreleaserMetadataFile  = "metadata.json"
)

// CandidateSource identifies the immutable Actions Artifact that stores the
// GoReleaser candidate files.
type CandidateSource struct {
	ArtifactName string `json:"artifact_name"`
	ArtifactID   string `json:"artifact_id"`
}

// CandidateFile records one file that tests and Promotion must consume without
// rebuilding.
type CandidateFile struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	OS     string `json:"os,omitempty"`
	Arch   string `json:"arch,omitempty"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// CandidateImage records one untagged, platform-specific image pushed by
// digest during candidate construction.
type CandidateImage struct {
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Digest   string `json:"digest"`
}

// CandidateManifest binds every releasable file and image digest to one
// immutable Run identity.
type CandidateManifest struct {
	SchemaVersion int              `json:"schema_version"`
	GeneratedAt   time.Time        `json:"generated_at"`
	Run           Run              `json:"run"`
	Source        CandidateSource  `json:"source"`
	Files         []CandidateFile  `json:"files"`
	Images        []CandidateImage `json:"images"`
}

type goreleaserArtifact struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Type   string `json:"type"`
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
}

type goreleaserMetadata struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// BuildCandidateManifest reads GoReleaser metadata and Docker digest files,
// verifies the expected release matrix, and hashes every candidate file.
func BuildCandidateManifest(
	repositoryRoot string,
	distDir string,
	digestDir string,
	run Run,
	source CandidateSource,
	imageName string,
	generatedAt time.Time,
) (CandidateManifest, error) {
	if err := run.Validate(); err != nil {
		return CandidateManifest{}, fmt.Errorf("validate candidate run: %w", err)
	}
	if strings.TrimSpace(source.ArtifactName) == "" || strings.TrimSpace(source.ArtifactID) == "" {
		return CandidateManifest{}, fmt.Errorf("candidate source artifact name and id are required")
	}
	if strings.TrimSpace(imageName) == "" {
		imageName = defaultCandidateImage
	}

	metadataPath := filepath.Join(distDir, goreleaserMetadataFile)
	metadata, err := loadGoReleaserMetadata(metadataPath)
	if err != nil {
		return CandidateManifest{}, err
	}
	if metadata.Commit != run.Commit {
		return CandidateManifest{}, fmt.Errorf(
			"GoReleaser metadata commit %s does not match candidate commit %s",
			metadata.Commit,
			run.Commit,
		)
	}
	if metadata.Version != strings.TrimPrefix(run.Version, "v") {
		return CandidateManifest{}, fmt.Errorf(
			"GoReleaser metadata version %s does not match candidate version %s",
			metadata.Version,
			run.Version,
		)
	}

	artifactsPath := filepath.Join(distDir, goreleaserArtifactsFile)
	artifacts, err := loadGoReleaserArtifacts(artifactsPath)
	if err != nil {
		return CandidateManifest{}, err
	}
	manifest := CandidateManifest{
		SchemaVersion: CandidateSchemaVersion,
		GeneratedAt:   generatedAt.UTC(),
		Run:           run,
		Source:        source,
	}
	for _, path := range []string{artifactsPath, metadataPath} {
		file, err := newCandidateFile(repositoryRoot, candidateMetadata, "", "", path)
		if err != nil {
			return CandidateManifest{}, err
		}
		manifest.Files = append(manifest.Files, file)
	}
	for _, artifact := range artifacts {
		kind, include := candidateKind(artifact.Type)
		if !include {
			continue
		}
		file, err := newCandidateFile(repositoryRoot, kind, artifact.GOOS, artifact.GOARCH, artifact.Path)
		if err != nil {
			return CandidateManifest{}, err
		}
		if file.Name != artifact.Name {
			return CandidateManifest{}, fmt.Errorf(
				"GoReleaser artifact name %s does not match path %s",
				artifact.Name,
				artifact.Path,
			)
		}
		manifest.Files = append(manifest.Files, file)
	}
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		digestPath := filepath.Join(digestDir, strings.ReplaceAll(platform, "/", "-")+".digest")
		data, err := os.ReadFile(digestPath)
		if err != nil {
			return CandidateManifest{}, fmt.Errorf("read Docker digest for %s: %w", platform, err)
		}
		manifest.Images = append(manifest.Images, CandidateImage{
			Name:     imageName,
			Platform: platform,
			Digest:   strings.TrimSpace(string(data)),
		})
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	sort.Slice(manifest.Images, func(i, j int) bool { return manifest.Images[i].Platform < manifest.Images[j].Platform })
	if err := manifest.Validate(); err != nil {
		return CandidateManifest{}, err
	}
	return manifest, nil
}

// Validate checks the candidate matrix and immutable identity without reading
// candidate files from disk.
func (m CandidateManifest) Validate() error {
	if m.SchemaVersion != CandidateSchemaVersion {
		return fmt.Errorf("candidate schema_version must be %d, got %d", CandidateSchemaVersion, m.SchemaVersion)
	}
	if err := m.Run.Validate(); err != nil {
		return err
	}
	if err := validateTimestamp("candidate.generated_at", m.GeneratedAt); err != nil {
		return err
	}
	if strings.TrimSpace(m.Source.ArtifactName) == "" || strings.TrimSpace(m.Source.ArtifactID) == "" {
		return fmt.Errorf("candidate source artifact name and id are required")
	}

	expectedArchives := map[string]bool{
		"darwin/amd64":  false,
		"darwin/arm64":  false,
		"linux/amd64":   false,
		"linux/arm64":   false,
		"windows/amd64": false,
		"windows/arm64": false,
	}
	expectedPackages := map[string]bool{
		"linux/amd64/.deb": false,
		"linux/amd64/.rpm": false,
		"linux/arm64/.deb": false,
		"linux/arm64/.rpm": false,
	}
	seenPaths := make(map[string]struct{}, len(m.Files))
	counts := map[string]int{}
	for i, file := range m.Files {
		if err := validateCandidateFile(m.Run.ID, file); err != nil {
			return fmt.Errorf("candidate files[%d]: %w", i, err)
		}
		if _, exists := seenPaths[file.Path]; exists {
			return fmt.Errorf("candidate file path %s is repeated", file.Path)
		}
		seenPaths[file.Path] = struct{}{}
		counts[file.Kind]++
		switch file.Kind {
		case candidateArchive:
			key := file.OS + "/" + file.Arch
			if _, expected := expectedArchives[key]; !expected {
				return fmt.Errorf("unexpected candidate archive platform %s", key)
			}
			if expectedArchives[key] {
				return fmt.Errorf("candidate archive platform %s is repeated", key)
			}
			expectedArchives[key] = true
		case candidateLinuxPackage:
			key := file.OS + "/" + file.Arch + "/" + filepath.Ext(file.Name)
			if _, expected := expectedPackages[key]; !expected {
				return fmt.Errorf("unexpected candidate Linux package %s", key)
			}
			if expectedPackages[key] {
				return fmt.Errorf("candidate Linux package %s is repeated", key)
			}
			expectedPackages[key] = true
		}
	}
	for platform, present := range expectedArchives {
		if !present {
			return fmt.Errorf("candidate archive for %s is missing", platform)
		}
	}
	for target, present := range expectedPackages {
		if !present {
			return fmt.Errorf("candidate Linux package %s is missing", target)
		}
	}
	if counts[candidateChecksum] != 1 || counts[candidateHomebrew] != 1 || counts[candidateMetadata] != 2 {
		return fmt.Errorf(
			"candidate support files must contain one checksum, one Homebrew formula, and two metadata files",
		)
	}

	expectedImages := map[string]bool{"linux/amd64": false, "linux/arm64": false}
	for i, image := range m.Images {
		if strings.TrimSpace(image.Name) == "" {
			return fmt.Errorf("candidate images[%d].name is required", i)
		}
		if _, expected := expectedImages[image.Platform]; !expected {
			return fmt.Errorf("unexpected candidate image platform %s", image.Platform)
		}
		if expectedImages[image.Platform] {
			return fmt.Errorf("candidate image platform %s is repeated", image.Platform)
		}
		if !strings.HasPrefix(image.Digest, "sha256:") ||
			!sha256Pattern.MatchString(strings.TrimPrefix(image.Digest, "sha256:")) {
			return fmt.Errorf("candidate image %s has invalid digest", image.Platform)
		}
		expectedImages[image.Platform] = true
	}
	for platform, present := range expectedImages {
		if !present {
			return fmt.Errorf("candidate image for %s is missing", platform)
		}
	}
	return nil
}

// VerifyCandidateManifest recomputes every file digest and ensures the
// manifest belongs to the expected immutable Run.
func VerifyCandidateManifest(repositoryRoot string, manifest CandidateManifest, expected Run) error {
	if err := expected.Validate(); err != nil {
		return fmt.Errorf("validate expected candidate run: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	if manifest.Run != expected {
		return fmt.Errorf(
			"candidate manifest identifies %s@%s (%s), want %s@%s (%s)",
			manifest.Run.Version,
			manifest.Run.Commit,
			manifest.Run.ID,
			expected.Version,
			expected.Commit,
			expected.ID,
		)
	}
	var metadataPath string
	for _, candidate := range manifest.Files {
		nativePath := filepath.FromSlash(candidate.Path)
		if err := rejectSymlinkComponents(repositoryRoot, nativePath); err != nil {
			return fmt.Errorf("verify candidate file %s: %w", candidate.Path, err)
		}
		fullPath := filepath.Join(repositoryRoot, nativePath)
		info, err := os.Lstat(fullPath)
		if err != nil {
			return fmt.Errorf("inspect candidate file %s: %w", candidate.Path, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("candidate file %s must be regular", candidate.Path)
		}
		if info.Size() != candidate.Size {
			return fmt.Errorf("candidate file %s size changed", candidate.Path)
		}
		sum, err := fileSHA256(fullPath)
		if err != nil {
			return fmt.Errorf("hash candidate file %s: %w", candidate.Path, err)
		}
		if sum != candidate.SHA256 {
			return fmt.Errorf("candidate file %s SHA-256 changed", candidate.Path)
		}
		if candidate.Kind == candidateMetadata && candidate.Name == goreleaserMetadataFile {
			metadataPath = fullPath
		}
	}
	if metadataPath == "" {
		return fmt.Errorf("candidate GoReleaser metadata file is missing")
	}
	metadata, err := loadGoReleaserMetadata(metadataPath)
	if err != nil {
		return err
	}
	if metadata.Commit != expected.Commit || metadata.Version != strings.TrimPrefix(expected.Version, "v") {
		return fmt.Errorf("candidate GoReleaser metadata does not match the expected Run")
	}
	return nil
}

// WriteCandidateManifest installs one immutable candidate manifest without
// replacing an existing file for the same Run.
func WriteCandidateManifest(path string, manifest CandidateManifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode candidate manifest: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create candidate manifest directory: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".candidate-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary candidate manifest: %w", err)
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(0o644); err != nil {
		_ = temp.Close()
		return fmt.Errorf("set candidate manifest permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write candidate manifest: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync candidate manifest: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close candidate manifest: %w", err)
	}
	if err := os.Link(tempPath, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("candidate manifest already exists: %s", path)
		}
		return fmt.Errorf("install candidate manifest: %w", err)
	}
	return nil
}

// LoadCandidateManifest strictly decodes one candidate manifest.
func LoadCandidateManifest(path string) (_ CandidateManifest, returnErr error) {
	info, err := os.Lstat(path)
	if err != nil {
		return CandidateManifest{}, fmt.Errorf("inspect candidate manifest: %w", err)
	}
	if !info.Mode().IsRegular() {
		return CandidateManifest{}, fmt.Errorf("candidate manifest must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return CandidateManifest{}, fmt.Errorf("open candidate manifest: %w", err)
	}
	defer func() {
		if err := file.Close(); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("close candidate manifest: %w", err)
		}
	}()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var manifest CandidateManifest
	if err := decoder.Decode(&manifest); err != nil {
		return CandidateManifest{}, fmt.Errorf("decode candidate manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return CandidateManifest{}, fmt.Errorf("candidate manifest contains multiple JSON values")
		}
		return CandidateManifest{}, fmt.Errorf("decode candidate manifest trailing content: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return CandidateManifest{}, err
	}
	return manifest, nil
}

func candidateKind(goreleaserType string) (string, bool) {
	switch goreleaserType {
	case "Archive":
		return candidateArchive, true
	case "Linux Package":
		return candidateLinuxPackage, true
	case "Checksum":
		return candidateChecksum, true
	case "Homebrew Formula":
		return candidateHomebrew, true
	default:
		return "", false
	}
}

func newCandidateFile(repositoryRoot, kind, goos, goarch, path string) (CandidateFile, error) {
	fullPath := path
	if !filepath.IsAbs(fullPath) {
		fullPath = filepath.Join(repositoryRoot, filepath.FromSlash(fullPath))
	}
	relative, err := filepath.Rel(repositoryRoot, fullPath)
	if err != nil {
		return CandidateFile{}, fmt.Errorf("resolve candidate path %s: %w", path, err)
	}
	relative = filepath.ToSlash(relative)
	if relative == ".." || strings.HasPrefix(relative, "../") {
		return CandidateFile{}, fmt.Errorf("candidate path %s leaves the repository", path)
	}
	if err := rejectSymlinkComponents(repositoryRoot, filepath.FromSlash(relative)); err != nil {
		return CandidateFile{}, fmt.Errorf("validate candidate path %s: %w", relative, err)
	}
	info, err := os.Lstat(fullPath)
	if err != nil {
		return CandidateFile{}, fmt.Errorf("inspect candidate file %s: %w", relative, err)
	}
	if !info.Mode().IsRegular() {
		return CandidateFile{}, fmt.Errorf("candidate file %s must be regular", relative)
	}
	sum, err := fileSHA256(fullPath)
	if err != nil {
		return CandidateFile{}, fmt.Errorf("hash candidate file %s: %w", relative, err)
	}
	return CandidateFile{
		Kind:   kind,
		Name:   filepath.Base(relative),
		Path:   relative,
		OS:     goos,
		Arch:   goarch,
		Size:   info.Size(),
		SHA256: sum,
	}, nil
}

func validateCandidateFile(runID string, file CandidateFile) error {
	switch file.Kind {
	case candidateArchive, candidateLinuxPackage, candidateChecksum, candidateHomebrew, candidateMetadata:
	default:
		return fmt.Errorf("unknown kind %q", file.Kind)
	}
	if strings.TrimSpace(file.Name) == "" || file.Name != filepath.Base(file.Path) {
		return fmt.Errorf("name %q must match path %q", file.Name, file.Path)
	}
	if file.Path == "" || strings.Contains(file.Path, `\`) {
		return fmt.Errorf("path must be a repository-relative slash path")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(file.Path)))
	if clean != file.Path || !strings.HasPrefix(clean, "dist/") {
		return fmt.Errorf("path %q must stay below dist", file.Path)
	}
	if strings.HasPrefix(clean, RunRelativeDir(runID)+"/") {
		return fmt.Errorf("candidate input %q cannot be a generated Run report", file.Path)
	}
	if file.Size < 0 {
		return fmt.Errorf("file %s has invalid size", file.Path)
	}
	if !sha256Pattern.MatchString(file.SHA256) {
		return fmt.Errorf("file %s has invalid SHA-256", file.Path)
	}
	if file.Kind == candidateArchive || file.Kind == candidateLinuxPackage {
		if err := (Platform{OS: file.OS, Arch: file.Arch}).Validate(); err != nil {
			return err
		}
	} else if file.OS != "" || file.Arch != "" {
		return fmt.Errorf("support file %s cannot claim a platform", file.Path)
	}
	return nil
}

func loadGoReleaserArtifacts(path string) ([]goreleaserArtifact, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read GoReleaser artifacts metadata: %w", err)
	}
	var artifacts []goreleaserArtifact
	if err := json.Unmarshal(data, &artifacts); err != nil {
		return nil, fmt.Errorf("decode GoReleaser artifacts metadata: %w", err)
	}
	return artifacts, nil
}

func loadGoReleaserMetadata(path string) (goreleaserMetadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return goreleaserMetadata{}, fmt.Errorf("read GoReleaser metadata: %w", err)
	}
	var metadata goreleaserMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return goreleaserMetadata{}, fmt.Errorf("decode GoReleaser metadata: %w", err)
	}
	if metadata.Version == "" || metadata.Commit == "" {
		return goreleaserMetadata{}, fmt.Errorf("GoReleaser metadata requires version and commit")
	}
	return metadata, nil
}
