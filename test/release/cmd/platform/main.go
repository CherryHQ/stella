// Command platform verifies Stella's build-once release candidates across the
// archive, native System, Docker, and Helm deployment boundaries.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	releasecontract "github.com/CherryHQ/stella/test/release"
)

type options struct {
	mode         string
	root         string
	manifestPath string
	platform     string
	stage        string
	output       string
	expected     string
}

func main() {
	opts := options{}
	flag.StringVar(&opts.mode, "mode", "", "operation: archives, extract, record, assert, or finalize")
	flag.StringVar(&opts.root, "root", ".", "repository root")
	flag.StringVar(&opts.manifestPath, "manifest", "", "candidate manifest path; defaults to the canonical Run directory")
	flag.StringVar(&opts.platform, "platform", "", "target platform as os/arch")
	flag.StringVar(&opts.stage, "stage", "", "stage name for record mode")
	flag.StringVar(&opts.output, "output", "", "extracted candidate binary path")
	flag.StringVar(&opts.expected, "expected", "", "comma-separated stages required by assert mode")
	flag.Parse()

	if err := run(opts, flag.Args(), time.Now().UTC()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(opts options, command []string, now time.Time) error {
	run, present, err := releasecontract.RunFromEnv()
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("STELLA_RELEASE_RUN_ID, STELLA_RELEASE_VERSION, and STELLA_RELEASE_COMMIT are required")
	}
	attempt, err := releaseAttempt()
	if err != nil {
		return err
	}
	root, err := filepath.Abs(opts.root)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	manifestPath := opts.manifestPath
	if manifestPath == "" {
		manifestPath = filepath.Join(releasecontract.RunDirectory(root, run.ID), "candidate.json")
	} else if !filepath.IsAbs(manifestPath) {
		manifestPath = filepath.Join(root, manifestPath)
	}

	switch opts.mode {
	case "archives":
		manifest, err := loadCandidate(root, manifestPath, run)
		if err != nil {
			return writeArchiveResult(root, run, attempt, now, archiveReport{}, err)
		}
		report, verifyErr := verifyArchiveMatrix(root, manifest)
		return writeArchiveResult(root, run, attempt, now, report, verifyErr)
	case "extract":
		manifest, err := loadCandidate(root, manifestPath, run)
		if err != nil {
			return err
		}
		platform, err := parsePlatform(opts.platform)
		if err != nil {
			return err
		}
		if opts.output == "" {
			return fmt.Errorf("--output is required for extract mode")
		}
		output := opts.output
		if !filepath.IsAbs(output) {
			output = filepath.Join(root, output)
		}
		if err := extractCandidateBinary(root, manifest, platform, output); err != nil {
			return err
		}
		fmt.Printf("candidate binary extracted: %s\n", output)
		return nil
	case "record":
		platform, err := parsePlatform(opts.platform)
		if err != nil {
			return err
		}
		if len(command) == 0 {
			return fmt.Errorf("record mode requires a command after --")
		}
		return recordStage(root, run, platform, opts.stage, attempt, now, func(log io.Writer) error {
			cmd := exec.Command(command[0], command[1:]...)
			cmd.Stdout = log
			cmd.Stderr = log
			cmd.Env = os.Environ()
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("stage command failed: %w", err)
			}
			return nil
		})
	case "assert":
		platform, err := parsePlatform(opts.platform)
		if err != nil {
			return err
		}
		expected := splitNonempty(opts.expected)
		if len(expected) == 0 {
			return fmt.Errorf("--expected is required for assert mode")
		}
		return assertStages(root, run, platform, attempt, expected)
	case "finalize":
		return finalizePlatformResults(root, run, attempt, now)
	default:
		return fmt.Errorf("unsupported platform mode %q", opts.mode)
	}
}

func loadCandidate(
	root string,
	manifestPath string,
	run releasecontract.Run,
) (releasecontract.CandidateManifest, error) {
	manifest, err := releasecontract.LoadCandidateManifest(manifestPath)
	if err != nil {
		return releasecontract.CandidateManifest{}, err
	}
	if manifest.Run != run {
		return releasecontract.CandidateManifest{}, fmt.Errorf("candidate manifest does not identify the current release Run")
	}
	if err := releasecontract.VerifyCandidateManifest(root, manifest, run); err != nil {
		return releasecontract.CandidateManifest{}, err
	}
	return manifest, nil
}

func parsePlatform(value string) (releasecontract.Platform, error) {
	goos, goarch, ok := strings.Cut(value, "/")
	platform := releasecontract.Platform{OS: goos, Arch: goarch}
	if !ok {
		return releasecontract.Platform{}, fmt.Errorf("platform %q must use os/arch", value)
	}
	if err := platform.Validate(); err != nil {
		return releasecontract.Platform{}, err
	}
	return platform, nil
}

func releaseAttempt() (int, error) {
	raw := strings.TrimSpace(os.Getenv("GITHUB_RUN_ATTEMPT"))
	if raw == "" {
		return 1, nil
	}
	attempt, err := strconv.Atoi(raw)
	if err != nil || attempt < 1 {
		return 0, fmt.Errorf("GITHUB_RUN_ATTEMPT must be a positive integer")
	}
	return attempt, nil
}

func splitNonempty(value string) []string {
	var out []string
	for item := range strings.SplitSeq(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
