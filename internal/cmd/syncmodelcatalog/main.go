// Command syncmodelcatalog refreshes the compact models.dev snapshot embedded
// in stellad. It is a build-time operation, not a server startup dependency.
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/CherryHQ/stella/internal/model/catalog"
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, catalog.DefaultURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch models.dev: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch models.dev: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read models.dev: %w", err)
	}
	payload, err := catalog.CompactGzip(raw)
	if err != nil {
		return err
	}
	path := filepath.Join("internal", "modelcatalog", "data", "models-dev.json.gz")
	tmp, err := os.CreateTemp(filepath.Dir(path), ".models-dev-*.gz")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	fmt.Printf("wrote %s (%d bytes)\n", path, len(payload))
	return nil
}
