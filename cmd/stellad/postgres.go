package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/db/pgruntime"
	"github.com/CherryHQ/stella/internal/platform/config"
)

func postgresCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "postgres",
		Usage:    "Manage the embedded PostgreSQL runtime",
		Category: "System",
		Subcommands: []*ucli.Command{
			postgresDownloadCommand(),
			postgresPruneCommand(),
		},
	}
}

func postgresPruneCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "prune",
		Usage: "Remove downloaded PostgreSQL runtimes this binary does not use",
		Description: "Every runtime version installs into its own directory and nothing removes the " +
			"siblings, so each upgrade leaves a few hundred megabytes behind. This lists what it " +
			"would remove and stops; pass --force to delete. Stop any stellad still serving from an " +
			"older runtime first — removing a directory a live server has open turns a disk-space " +
			"cleanup into an outage.",
		Flags: []ucli.Flag{
			&ucli.BoolFlag{Name: "force", Usage: "Remove the runtimes instead of listing them"},
			&ucli.BoolFlag{Name: "json", Usage: "Emit the result as JSON"},
		},
		Action: func(c *ucli.Context) error {
			return pruneRuntimes(os.Stdout, config.StellaHome(), c.Bool("force"), c.Bool("json"))
		},
	}
}

// prunedRuntime mirrors pgruntime.InstalledRuntime for --json consumers, which
// need a stable snake_case shape that does not move when the Go struct does.
type prunedRuntime struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

type pruneReport struct {
	Current  string          `json:"current"`
	Pruned   bool            `json:"pruned"`
	Runtimes []prunedRuntime `json:"runtimes"`
	Bytes    int64           `json:"bytes"`
}

func pruneRuntimes(out io.Writer, stellaHome string, force, asJSON bool) error {
	installed, err := pgruntime.InstalledRuntimes(stellaHome)
	if err != nil {
		return fmt.Errorf("postgres prune: %w", err)
	}

	report := pruneReport{Current: pgruntime.CurrentRuntimeDir(), Pruned: force}
	for _, rt := range installed {
		if rt.Current {
			continue
		}
		report.Runtimes = append(report.Runtimes, prunedRuntime{Name: rt.Name, Path: rt.Path, Bytes: rt.Bytes})
		report.Bytes += rt.Bytes
	}

	// Remove before reporting, so a partial failure never claims space it did
	// not reclaim. The first failure stops the run rather than continuing to
	// delete under whatever condition caused it.
	if force {
		for _, rt := range report.Runtimes {
			if err := os.RemoveAll(rt.Path); err != nil {
				return fmt.Errorf("postgres prune: remove %s: %w", rt.Path, err)
			}
		}
	}

	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if report.Runtimes == nil {
			report.Runtimes = []prunedRuntime{}
		}
		return enc.Encode(report)
	}
	writePruneReport(out, report)
	return nil
}

func writePruneReport(out io.Writer, report pruneReport) {
	if len(report.Runtimes) == 0 {
		fprintf(out, "No unused PostgreSQL runtimes. In use: %s\n", report.Current)
		return
	}
	if report.Pruned {
		for _, rt := range report.Runtimes {
			fprintf(out, "Removed %s (%s)\n", rt.Name, humanBytes(rt.Bytes))
		}
		fprintf(out, "Reclaimed %s.\n", humanBytes(report.Bytes))
		return
	}
	fprintf(out, "In use: %s\n\nUnused, safe to remove once no server is running from them:\n", report.Current)
	for _, rt := range report.Runtimes {
		fprintf(out, "  %s  %s\n", rt.Name, humanBytes(rt.Bytes))
	}
	fprintf(out, "\n%s would be reclaimed. Re-run with --force to remove them.\n", humanBytes(report.Bytes))
}

func postgresDownloadCommand() *ucli.Command {
	return &ucli.Command{
		Name: "download",
		// The command shipped as download-runtime; the suffix was redundant under
		// a domain whose whole job is the runtime. Keep the old name working —
		// it is in released docs, in error hints users have already seen, and in
		// whatever scripts they wrote around it.
		Aliases: []string{"download-runtime"},
		Usage:   "Download the embedded PostgreSQL runtime for this platform",
		Description: "Download and install the PostgreSQL runtime used when STELLA_DATABASE_URL is unset. " +
			"Set STELLA_DATABASE_URL instead if you run PostgreSQL yourself.",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "source", Usage: "Runtime source to download (Linux: bookworm, noble, trixie; macOS arm64: postgresapp)"},
			&ucli.StringFlag{Name: "repo", Usage: "Runtime release repository", Value: pgruntime.DefaultRuntimeRepo},
			&ucli.BoolFlag{Name: "force", Usage: "Replace an already installed runtime"},
		},
		Action: func(c *ucli.Context) error {
			source := c.String("source")
			if source == "" {
				var ok bool
				source, ok = pgruntime.DefaultRuntimeSource()
				if !ok {
					return fmt.Errorf("postgres download: no default runtime source for %s/%s. %s", runtime.GOOS, runtime.GOARCH, pgruntime.MissingRuntimeHint())
				}
			}
			root, err := downloadPostgresRuntime(c.Context, os.Stderr, config.StellaHome(), c.String("repo"), source, c.Bool("force"))
			if err != nil {
				return err
			}
			fmt.Printf("PostgreSQL runtime installed at %s\n", root)
			return nil
		},
	}
}

func downloadPostgresRuntime(ctx context.Context, out io.Writer, stellaHome, repo, source string, force bool) (string, error) {
	root := pgruntime.RuntimeRoot(stellaHome, source)
	if !force && postgresRuntimeAlreadyInstalled(root) {
		fprintf(out, "PostgreSQL runtime already installed at %s\n", root)
		return root, nil
	}

	assetName := pgruntime.RuntimeAssetName(pgruntime.RuntimeVersion, runtime.GOOS, runtime.GOARCH, source)
	assetURL := pgruntime.RuntimeAssetURL(repo, pgruntime.RuntimeVersion, runtime.GOOS, runtime.GOARCH, source)
	checksumURL := pgruntime.RuntimeChecksumURL(repo, pgruntime.RuntimeVersion, runtime.GOOS, runtime.GOARCH, source)

	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return "", fmt.Errorf("create PostgreSQL runtime parent: %w", err)
	}
	tmpDir, err := os.MkdirTemp(filepath.Dir(root), ".pg-runtime-download-*")
	if err != nil {
		return "", fmt.Errorf("create PostgreSQL runtime temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	archivePath := filepath.Join(tmpDir, assetName)
	fprintf(out, "Downloading %s\n", assetName)
	if err := downloadFile(ctx, out, assetURL, archivePath); err != nil {
		return "", err
	}

	fprintf(out, "Verifying checksum...\n")
	expected, err := fetchRuntimeChecksum(ctx, checksumURL, assetName)
	if err != nil {
		return "", err
	}
	actual, err := sha256File(archivePath)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(actual, expected) {
		return "", fmt.Errorf("checksum mismatch for %s: expected %s, got %s", assetName, expected, actual)
	}

	extractDir := filepath.Join(tmpDir, "runtime")
	fprintf(out, "Installing runtime to %s...\n", root)
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return "", fmt.Errorf("create PostgreSQL runtime extraction dir: %w", err)
	}
	if err := pgruntime.ExtractTarZstdFile(archivePath, extractDir); err != nil {
		return "", err
	}
	if err := os.RemoveAll(root); err != nil {
		return "", fmt.Errorf("replace PostgreSQL runtime: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return "", fmt.Errorf("create PostgreSQL runtime parent: %w", err)
	}
	if err := os.Rename(extractDir, root); err != nil {
		return "", fmt.Errorf("install PostgreSQL runtime: %w", err)
	}
	return root, nil
}

func fetchRuntimeChecksum(ctx context.Context, url, assetName string) (string, error) {
	resp, err := upgradeHTTPClient.R().
		SetContext(ctx).
		SetHeader("User-Agent", upgradeUserAgent).
		Get(url)
	if err != nil {
		return "", fmt.Errorf("fetch runtime checksum: %w", err)
	}
	if resp.StatusCode() != 200 {
		return "", fmt.Errorf("fetch runtime checksum: unexpected status %d", resp.StatusCode())
	}
	fields := strings.Fields(resp.String())
	if len(fields) == 0 {
		return "", fmt.Errorf("runtime checksum for %s is empty", assetName)
	}
	return fields[0], nil
}

func postgresRuntimeAlreadyInstalled(root string) bool {
	pgCtl := "pg_ctl"
	if runtime.GOOS == "windows" {
		pgCtl = "pg_ctl.exe"
	}
	if localFileExists(filepath.Join(root, "postgres", "bin", pgCtl)) {
		return true
	}
	if localFileExists(filepath.Join(root, "bin", pgCtl)) {
		return true
	}
	matches, err := filepath.Glob(filepath.Join(root, "postgres", "lib", "postgresql", "*", "bin", pgCtl))
	return err == nil && len(matches) > 0
}

func localFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
