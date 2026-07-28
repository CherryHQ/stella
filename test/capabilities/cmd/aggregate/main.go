//go:build capability

// Command aggregate validates release results and writes the Promotion gate
// report for one immutable candidate.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	// Populate the exact builtin catalog linked into stellad before validating
	// the capability manifest's plugin surface.
	_ "github.com/CherryHQ/stella/plugins/builtin"

	"github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/test/capabilities"
	releasecontract "github.com/CherryHQ/stella/test/release"
)

func main() {
	root := flag.String("root", ".", "repository root")
	manifestPath := flag.String("manifest", "test/capabilities.yaml", "capability manifest path, relative to root")
	runID := flag.String("run-id", "", "release Run ID; defaults to STELLA_RELEASE_RUN_ID")
	version := flag.String("version", "", "candidate version; defaults to STELLA_RELEASE_VERSION")
	commit := flag.String("commit", "", "candidate commit; defaults to STELLA_RELEASE_COMMIT")
	secretNames := flag.String(
		"secret-envs",
		"",
		"comma-separated secret environment variable names to scan for",
	)
	reportOnly := flag.Bool(
		"report-only",
		false,
		"write the current gate report without failing on release blockers",
	)
	flag.Parse()

	target := releasecontract.Run{ID: *runID, Version: *version, Commit: *commit}
	if target.ID == "" && target.Version == "" && target.Commit == "" {
		var present bool
		var err error
		target, present, err = releasecontract.RunFromEnv()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if !present {
			fmt.Fprintln(os.Stderr, "release metadata is required through flags or STELLA_RELEASE_* variables")
			os.Exit(1)
		}
	}
	if *secretNames == "" {
		*secretNames = releasecontract.SecretNamesFromEnv()
	}
	report, jsonPath, markdownPath, err := run(
		*root,
		*manifestPath,
		target,
		parseSecretNames(*secretNames),
		time.Now(),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf(
		"release gate: allowed=%t scenarios=%d blockers=%d stale_results=%d\n",
		report.ReleaseAllowed,
		report.Summary.Scenarios,
		report.Summary.Blockers,
		report.Summary.IgnoredStaleResults,
	)
	fmt.Printf("JSON report: %s\nMarkdown report: %s\n", jsonPath, markdownPath)
	if !report.ReleaseAllowed && !*reportOnly {
		os.Exit(2)
	}
}

func run(
	root string,
	manifestPath string,
	target releasecontract.Run,
	secretNames []string,
	generatedAt time.Time,
) (capabilities.GateReport, string, string, error) {
	if err := target.Validate(); err != nil {
		return capabilities.GateReport{}, "", "", fmt.Errorf("validate release target: %w", err)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return capabilities.GateReport{}, "", "", fmt.Errorf("resolve repository root: %w", err)
	}
	manifest, err := capabilities.LoadManifest(filepath.Join(absRoot, manifestPath))
	if err != nil {
		return capabilities.GateReport{}, "", "", err
	}
	surfaces, err := capabilities.CollectRepositorySurfaces(absRoot, plugins.Names())
	if err != nil {
		return capabilities.GateReport{}, "", "", err
	}
	if err := capabilities.Validate(absRoot, manifest, surfaces); err != nil {
		return capabilities.GateReport{}, "", "", err
	}

	runDir := releasecontract.RunDirectory(absRoot, target.ID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return capabilities.GateReport{}, "", "", fmt.Errorf("create release run directory: %w", err)
	}
	secrets, err := releasecontract.PresentSecretValuesFromEnv(secretNames)
	if err != nil {
		return capabilities.GateReport{}, "", "", err
	}
	if err := releasecontract.ScanForSecrets(runDir, secrets); err != nil {
		return capabilities.GateReport{}, "", "", err
	}
	results, err := releasecontract.LoadResults(filepath.Join(runDir, "results"))
	if err != nil {
		return capabilities.GateReport{}, "", "", err
	}
	report, err := capabilities.BuildGateReport(absRoot, manifest, target, results, generatedAt)
	if err != nil {
		return capabilities.GateReport{}, "", "", err
	}
	jsonPath, markdownPath, err := capabilities.WriteGateReport(runDir, report, secrets)
	if err != nil {
		return capabilities.GateReport{}, "", "", err
	}
	return report, jsonPath, markdownPath, nil
}

func parseSecretNames(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\r' || r == '\n'
	})
}
