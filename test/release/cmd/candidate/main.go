// Command candidate creates and verifies Stella's build-once release candidate
// manifest.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	releasecontract "github.com/CherryHQ/stella/test/release"
)

func main() {
	mode := flag.String("mode", "verify", "operation: create or verify")
	root := flag.String("root", ".", "repository root")
	distDir := flag.String("dist", "dist", "GoReleaser dist directory, relative to root")
	digestDir := flag.String("digests", "docker-digests", "Docker digest directory, relative to root")
	manifestPath := flag.String("manifest", "", "candidate manifest path; defaults to the canonical Run directory")
	artifactName := flag.String("artifact-name", "", "Actions Artifact name containing GoReleaser candidates")
	artifactID := flag.String("artifact-id", "", "Actions Artifact ID containing GoReleaser candidates")
	imageName := flag.String("image", "ghcr.io/cherryhq/stella", "candidate image repository")
	flag.Parse()

	if err := run(
		*mode,
		*root,
		*distDir,
		*digestDir,
		*manifestPath,
		*artifactName,
		*artifactID,
		*imageName,
		time.Now(),
	); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(
	mode string,
	root string,
	distDir string,
	digestDir string,
	manifestPath string,
	artifactName string,
	artifactID string,
	imageName string,
	generatedAt time.Time,
) error {
	releaseRun, present, err := releasecontract.RunFromEnv()
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("STELLA_RELEASE_RUN_ID, STELLA_RELEASE_VERSION, and STELLA_RELEASE_COMMIT are required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	if manifestPath == "" {
		manifestPath = filepath.Join(releasecontract.RunDirectory(absRoot, releaseRun.ID), "candidate.json")
	} else {
		manifestPath = resolvePath(absRoot, manifestPath)
	}

	switch mode {
	case "create":
		manifest, err := releasecontract.BuildCandidateManifest(
			absRoot,
			resolvePath(absRoot, distDir),
			resolvePath(absRoot, digestDir),
			releaseRun,
			releasecontract.CandidateSource{ArtifactName: artifactName, ArtifactID: artifactID},
			imageName,
			generatedAt,
		)
		if err != nil {
			return err
		}
		if err := releasecontract.WriteCandidateManifest(manifestPath, manifest); err != nil {
			return err
		}
		fmt.Printf("candidate manifest created: %s\n", manifestPath)
		return nil
	case "verify":
		manifest, err := releasecontract.LoadCandidateManifest(manifestPath)
		if err != nil {
			return err
		}
		if artifactName != "" || artifactID != "" {
			if artifactName == "" || artifactID == "" {
				return fmt.Errorf("candidate artifact name and id must be verified together")
			}
			if manifest.Source.ArtifactName != artifactName || manifest.Source.ArtifactID != artifactID {
				return fmt.Errorf("candidate source Artifact does not match the expected name and id")
			}
		}
		if err := releasecontract.VerifyCandidateManifest(absRoot, manifest, releaseRun); err != nil {
			return err
		}
		fmt.Printf(
			"candidate verified: %d files, %d images, commit %s\n",
			len(manifest.Files),
			len(manifest.Images),
			manifest.Run.Commit,
		)
		return nil
	default:
		return fmt.Errorf("unsupported candidate mode %q", mode)
	}
}

func resolvePath(root, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(root, path)
}
