package builddeps

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const tapVersion = "0.4.4"

func tapHostBinary() embeddedBinary {
	return embeddedBinary{
		Name:    "tap",
		Repo:    "vaayne/tap",
		Version: tapVersion,
		AssetTemplates: map[string]embeddedBinaryAsset{
			"darwin-amd64":  {File: "tap_{version}_darwin_amd64.tar.gz"},
			"darwin-arm64":  {File: "tap_{version}_darwin_arm64.tar.gz"},
			"linux-amd64":   {File: "tap_{version}_linux_amd64.tar.gz"},
			"linux-arm64":   {File: "tap_{version}_linux_arm64.tar.gz"},
			"windows-amd64": {File: "tap_{version}_windows_amd64.zip", BinaryName: "tap.exe"},
			"windows-arm64": {File: "tap_{version}_windows_arm64.zip", BinaryName: "tap.exe"},
		},
	}
}

func syncTapWebSkill(ctx context.Context, cfg Config) error {
	s := toolSyncer{client: http.DefaultClient, baseURL: "https://github.com"}
	platform := runtime.GOOS + "-" + runtime.GOARCH
	binPath, cleanup, err := s.fetchBinary(ctx, tapHostBinary(), platform)
	if err != nil {
		return fmt.Errorf("fetch tap binary: %w", err)
	}
	defer cleanup()
	stagingRoot, err := os.MkdirTemp("", "anna-tap-skill-*")
	if err != nil {
		return fmt.Errorf("create tap staging dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(stagingRoot) }()
	stagingDir := filepath.Join(stagingRoot, "tap-web")
	if err := syncTapWebSkillFromBinary(ctx, binPath, stagingDir); err != nil {
		return err
	}
	target := filepath.Join(cfg.WorkDir, "internal", "resources", "skills", "system", "tap-web")
	return AtomicReplaceDir(stagingDir, target)
}

func syncTapWebSkillFromBinary(ctx context.Context, binPath, destDir string) error {
	version, err := tapBinaryVersion(ctx, binPath)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, binPath, "skill", "install", "--path", destDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("install tap-web skill: %w\n%s", err, strings.TrimSpace(string(output)))
	}
	skillVersion, err := tapSkillVersion(filepath.Join(destDir, "SKILL.md"))
	if err != nil {
		return err
	}
	if normalizeSemver(skillVersion) != normalizeSemver(version) {
		return fmt.Errorf("tap-web skill version %q does not match tap binary version %q", skillVersion, version)
	}
	return nil
}

func tapBinaryVersion(ctx context.Context, binPath string) (string, error) {
	cmd := exec.CommandContext(ctx, binPath, "--version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("read tap version: %w", err)
	}
	version := semverRE.FindString(string(output))
	if version == "" {
		return "", fmt.Errorf("parse tap version from output %q", strings.TrimSpace(string(output)))
	}
	return version, nil
}

func tapSkillVersion(skillPath string) (string, error) {
	raw, err := os.ReadFile(skillPath)
	if err != nil {
		return "", fmt.Errorf("read tap skill metadata: %w", err)
	}
	meta, ok := parseTapSkillFrontmatter(string(raw))
	if !ok {
		return "", fmt.Errorf("parse tap skill frontmatter: missing or invalid")
	}
	rawMeta, _ := meta["metadata"].(map[string]any)
	if rawMeta == nil {
		return "", fmt.Errorf("tap skill metadata missing version")
	}
	version, _ := rawMeta["version"].(string)
	if version == "" {
		return "", fmt.Errorf("tap skill metadata missing version")
	}
	return version, nil
}

var semverRE = regexp.MustCompile(`v?\d+\.\d+\.\d+`)

func normalizeSemver(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

func parseTapSkillFrontmatter(content string) (map[string]any, bool) {
	meta := make(map[string]any)
	if !unmarshalYAMLFrontmatter(content, &meta) {
		return nil, false
	}
	return meta, true
}
