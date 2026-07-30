//go:build linux

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	releasecontract "github.com/CherryHQ/stella/test/release"
)

func writeBrowserResults(
	root string,
	runIdentity releasecontract.Run,
	attempt int,
	outcomes []scenarioOutcome,
) error {
	if len(outcomes) != len(browserScenarios) {
		return fmt.Errorf("browser runner produced %d outcomes, want %d", len(outcomes), len(browserScenarios))
	}
	runDir := releasecontract.RunDirectory(root, runIdentity.ID)
	var writeErr error
	for _, outcome := range outcomes {
		artifacts, artifactErr := artifactsForPaths(root, runDir, outcome.Paths)
		status := outcome.Status
		reason := outcome.Reason
		if artifactErr != nil {
			status = releasecontract.StatusProductFailure
			reason = appendReason(reason, "artifact validation failed: "+oneLine(artifactErr.Error()))
			writeErr = errors.Join(writeErr, artifactErr)
		}
		result := releasecontract.Result{
			SchemaVersion: releasecontract.SchemaVersion,
			Run:           runIdentity,
			Platforms: []releasecontract.Platform{{
				OS: "linux", Arch: "amd64",
			}},
			CapabilityID: outcome.Definition.CapabilityID,
			ScenarioID:   outcome.Definition.ScenarioID,
			Runner: releasecontract.Runner{
				Kind: releasecontract.RunnerBrowser,
				Name: "playwright-release-browser",
			},
			Attempt:    attempt,
			StartedAt:  outcome.StartedAt.UTC(),
			FinishedAt: outcome.FinishedAt.UTC(),
			Status:     status,
			Reason:     oneLine(reason),
			Artifacts:  artifacts,
		}
		if _, err := releasecontract.WriteResult(runDir, result); err != nil {
			writeErr = errors.Join(writeErr, fmt.Errorf("write %s result: %w", outcome.Definition.ScenarioID, err))
			continue
		}
		if err := releasecontract.ValidateArtifactFiles(root, result); err != nil {
			writeErr = errors.Join(writeErr, fmt.Errorf("validate %s artifacts: %w", outcome.Definition.ScenarioID, err))
		}
	}
	return writeErr
}

func artifactsForPaths(root, runDir string, paths []string) ([]releasecontract.Artifact, error) {
	var artifacts []releasecontract.Artifact
	var artifactErr error
	for _, path := range uniqueSorted(paths) {
		if !filepath.IsAbs(path) {
			artifactErr = errors.Join(artifactErr, fmt.Errorf("artifact path %s is not absolute", path))
			continue
		}
		path = filepath.Clean(path)
		if err := ensurePathBelow(runDir, path); err != nil {
			artifactErr = errors.Join(artifactErr, err)
			continue
		}
		info, err := os.Lstat(path)
		if err != nil {
			artifactErr = errors.Join(artifactErr, fmt.Errorf("inspect artifact %s: %w", path, err))
			continue
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			artifactErr = errors.Join(artifactErr, fmt.Errorf("artifact %s must be a regular non-symlink file", path))
			continue
		}
		digest, err := sha256File(path)
		if err != nil {
			artifactErr = errors.Join(artifactErr, err)
			continue
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			artifactErr = errors.Join(artifactErr, err)
			continue
		}
		artifacts = append(artifacts, releasecontract.Artifact{
			Kind:   browserArtifactKind(path),
			Path:   filepath.ToSlash(relative),
			SHA256: digest,
		})
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Path < artifacts[j].Path })
	return artifacts, artifactErr
}

func browserArtifactKind(path string) string {
	name := strings.ToLower(filepath.Base(path))
	switch {
	case strings.HasPrefix(name, "stellad-"):
		return "server-log"
	case strings.HasPrefix(name, "playwright-report-"):
		return "playwright-report"
	case strings.HasPrefix(name, "playwright-") && strings.HasSuffix(name, ".log"):
		return "runner-log"
	case strings.HasPrefix(name, "fake-anthropic-summary-"):
		return "fake-provider-report"
	case name == "trace.zip":
		return "trace"
	case strings.Contains(name, "screenshot") || strings.HasSuffix(name, ".png"):
		return "screenshot"
	case strings.Contains(name, "browser-console"):
		return "browser-console"
	case strings.Contains(name, "network-summary"):
		return "network-summary"
	case strings.HasPrefix(name, "redaction-error"):
		return "redaction-report"
	default:
		return "test-attachment"
	}
}

func scanBrowserArtifacts(artifactRoot string, probes map[string]string) error {
	rawNames := strings.TrimSpace(releasecontract.SecretNamesFromEnv())
	var names []string
	for item := range strings.SplitSeq(rawNames, ",") {
		if name := strings.TrimSpace(item); name != "" {
			names = append(names, name)
		}
	}
	values, err := releasecontract.SecretValuesFromEnv(names)
	if err != nil {
		return fmt.Errorf("resolve release secrets for artifact scan: %w", err)
	}
	for name, value := range probes {
		if value != "" {
			values[name] = value
		}
	}
	if len(values) == 0 {
		return nil
	}
	if err := releasecontract.ScanForSecrets(artifactRoot, values); err != nil {
		return fmt.Errorf("scan browser artifacts for release secrets: %w", err)
	}
	return nil
}

func replaceUnsafeArtifacts(runDir, artifactRoot string, scanErr error) (string, error) {
	expected := filepath.Clean(filepath.Join(runDir, "artifacts", "browser"))
	if filepath.Clean(artifactRoot) != expected {
		return "", fmt.Errorf("refuse to replace unexpected artifact directory %s", artifactRoot)
	}
	if err := ensurePathBelow(runDir, expected); err != nil {
		return "", err
	}
	// Remove only this runner's exact per-Run artifact directory. The result
	// directory and every other runner's evidence remain untouched.
	if err := os.RemoveAll(expected); err != nil {
		return "", fmt.Errorf("remove unsafe browser artifacts: %w", err)
	}
	if err := os.MkdirAll(expected, 0o755); err != nil {
		return "", fmt.Errorf("recreate browser artifact directory: %w", err)
	}
	path := filepath.Join(expected, "redaction-error.txt")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	_, writeErr := fmt.Fprintf(file, "Browser artifact scan failed; unsafe diagnostics were removed.\nReason: %s\n", oneLine(scanErr.Error()))
	closeErr := file.Close()
	return path, errors.Join(writeErr, closeErr)
}

func writeExclusiveJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}

func existingPaths(paths ...string) []string {
	var existing []string
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err == nil && info.Mode().IsRegular() {
			existing = append(existing, path)
		}
	}
	return uniqueSorted(existing)
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func appendReason(current, next string) string {
	if current == "" {
		return next
	}
	return current + "; " + next
}
