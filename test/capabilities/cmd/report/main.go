//go:build capability

// Command report validates Stella's capability inventory and writes the current
// coverage and gap reports.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	// Populate the exact builtin catalog linked into stellad.
	_ "github.com/CherryHQ/stella/plugins/builtin"

	"github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/test/capabilities"
)

func main() {
	root := flag.String("root", ".", "repository root")
	manifestPath := flag.String("manifest", "test/capabilities.yaml", "capability manifest path, relative to root")
	outputDir := flag.String("output", "dist/test-results/capabilities", "report directory, relative to root")
	flag.Parse()

	if err := run(*root, *manifestPath, *outputDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(root, manifestPath, outputDir string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	manifest, err := capabilities.LoadManifest(filepath.Join(absRoot, manifestPath))
	if err != nil {
		return err
	}
	surfaces, err := capabilities.CollectRepositorySurfaces(absRoot, plugins.Names())
	if err != nil {
		return err
	}
	if err := capabilities.Validate(absRoot, manifest, surfaces); err != nil {
		return err
	}
	metrics, err := capabilities.CollectTestMetrics(absRoot)
	if err != nil {
		return err
	}
	checkout, err := gitCommit(absRoot)
	if err != nil {
		return err
	}
	report := capabilities.BuildReport(manifest, surfaces, metrics, checkout, time.Now())
	jsonPath, markdownPath, err := capabilities.WriteReport(filepath.Join(absRoot, outputDir), report)
	if err != nil {
		return err
	}
	fmt.Printf("capability inventory valid: %d capabilities, %d scenarios, %d gaps\n", report.Summary.Capabilities, report.Summary.Scenarios, len(report.Gaps))
	fmt.Printf("JSON report: %s\nMarkdown report: %s\n", jsonPath, markdownPath)
	return nil
}

func gitCommit(root string) (string, error) {
	command := exec.Command("git", "rev-parse", "HEAD")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("resolve checkout commit: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}
