package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/CherryHQ/stella/pkg/httpclient"
)

// bootstrapUpgrade downloads the latest release archive and installs both
// stella and stellad into the same directory as the running binary. This is
// a one-time migration path for pre-split users who ran "stella upgrade"
// and got the new thin CLI without stellad.
func bootstrapUpgrade(ctx context.Context) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return fmt.Errorf("resolve symlink: %w", err)
	}
	installDir := filepath.Dir(self)

	fmt.Println("stellad not found — bootstrapping dual-binary install...")

	client := httpclient.New()
	apiURL := "https://api.github.com/repos/CherryHQ/stella/releases/latest"

	type asset struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	}
	type release struct {
		TagName string  `json:"tag_name"`
		Assets  []asset `json:"assets"`
	}

	var rel release
	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Accept", "application/vnd.github+json").
		SetHeader("User-Agent", "stella-bootstrap").
		SetResult(&rel).
		Get(apiURL)
	if err != nil {
		return fmt.Errorf("fetch latest release: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return fmt.Errorf("fetch latest release: HTTP %d", resp.StatusCode())
	}

	goos, goarch := runtime.GOOS, runtime.GOARCH
	suffix := "_" + goos + "_" + goarch
	var found asset
	for _, a := range rel.Assets {
		if strings.HasSuffix(a.Name, suffix+".tar.gz") || strings.HasSuffix(a.Name, suffix+".zip") {
			found = a
			break
		}
	}
	if found.URL == "" {
		return fmt.Errorf("no release asset for %s/%s", goos, goarch)
	}

	tmpDir, err := os.MkdirTemp("", "stella-bootstrap-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	archivePath := filepath.Join(tmpDir, found.Name)
	dlResp, err := client.R().
		SetContext(ctx).
		SetHeader("User-Agent", "stella-bootstrap").
		SetOutput(archivePath).
		Get(found.URL)
	if err != nil {
		return fmt.Errorf("download archive: %w", err)
	}
	if dlResp.StatusCode() != http.StatusOK {
		return fmt.Errorf("download archive: HTTP %d", dlResp.StatusCode())
	}

	bins := bootstrapBinNames(goos)
	for _, binName := range bins {
		extracted, err := bootstrapExtract(archivePath, tmpDir, binName)
		if err != nil {
			return fmt.Errorf("extract %s: %w", binName, err)
		}
		target := filepath.Join(installDir, binName)
		if err := bootstrapInstall(extracted, target, goos != "windows"); err != nil {
			return fmt.Errorf("install %s: %w", binName, err)
		}
		fmt.Printf("  installed %s\n", target)
	}

	fmt.Printf("\nUpgraded to %s. Run 'stellad upgrade' for future upgrades.\n", rel.TagName)
	return nil
}

func bootstrapBinNames(goos string) []string {
	if goos == "windows" {
		return []string{"stella.exe", "stellad.exe"}
	}
	return []string{"stella", "stellad"}
}

func bootstrapExtract(archivePath, destDir, binName string) (string, error) {
	if strings.HasSuffix(archivePath, ".tar.gz") {
		return bootstrapExtractTarGz(archivePath, destDir, binName)
	}
	return bootstrapExtractZip(archivePath, destDir, binName)
}

func bootstrapExtractTarGz(archivePath, destDir, binName string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != binName {
			continue
		}
		outPath := filepath.Join(destDir, binName)
		out, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(out, tr)
		closeErr := out.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		return outPath, nil
	}
	return "", fmt.Errorf("%s not found in archive", binName)
}

func bootstrapExtractZip(archivePath, destDir, binName string) (string, error) {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()

	for _, f := range r.File {
		if filepath.Base(f.Name) != binName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		outPath := filepath.Join(destDir, binName)
		out, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			_ = rc.Close()
			return "", err
		}
		_, copyErr := io.Copy(out, rc)
		closeErr := out.Close()
		_ = rc.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		return outPath, nil
	}
	return "", fmt.Errorf("%s not found in archive", binName)
}

func bootstrapInstall(srcPath, targetPath string, executable bool) error {
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	tmpPath := targetPath + ".tmp"
	mode := os.FileMode(0o644)
	if executable {
		mode = 0o755
	}
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if executable {
		_ = os.Chmod(tmpPath, 0o755)
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}
