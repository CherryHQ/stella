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
)

// ResultFilename returns the deterministic filename for one Scenario attempt.
// Attempt files are append-only so a retry cannot erase the earlier outcome.
func ResultFilename(result Result) string {
	return fmt.Sprintf("%s-%s-a%03d.json", result.Run.ID, result.ScenarioID, result.Attempt)
}

// WriteResult validates and installs one append-only JSON result below the
// current Run directory. An existing attempt is never replaced.
func WriteResult(runDir string, result Result) (string, error) {
	if err := result.Validate(); err != nil {
		return "", fmt.Errorf("validate release result: %w", err)
	}
	if filepath.Base(filepath.Clean(runDir)) != result.Run.ID {
		return "", fmt.Errorf("run directory %q does not match run id %q", runDir, result.Run.ID)
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode release result: %w", err)
	}
	data = append(data, '\n')

	resultsDir := filepath.Join(runDir, "results")
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		return "", fmt.Errorf("create release results directory: %w", err)
	}
	target := filepath.Join(resultsDir, ResultFilename(result))
	temp, err := os.CreateTemp(resultsDir, ".result-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temporary release result: %w", err)
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()

	if err := temp.Chmod(0o644); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("set release result permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("write temporary release result: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("sync temporary release result: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("close temporary release result: %w", err)
	}

	// A hard-link install provides no-replace semantics: concurrent or repeated
	// writers cannot silently erase the first recorded attempt.
	if err := os.Link(tempPath, target); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("release result attempt already exists: %s", target)
		}
		return "", fmt.Errorf("install release result: %w", err)
	}
	return target, nil
}

// LoadResult decodes one result strictly and rejects unknown or trailing JSON.
func LoadResult(path string) (_ Result, returnErr error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Result{}, fmt.Errorf("inspect release result %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return Result{}, fmt.Errorf("release result %s must be a regular file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return Result{}, fmt.Errorf("open release result %s: %w", path, err)
	}
	defer func() {
		if err := file.Close(); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("close release result %s: %w", path, err)
		}
	}()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var result Result
	if err := decoder.Decode(&result); err != nil {
		return Result{}, fmt.Errorf("decode release result %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Result{}, fmt.Errorf("decode release result %s: multiple JSON values are not allowed", path)
		}
		return Result{}, fmt.Errorf("decode release result %s trailing content: %w", path, err)
	}
	if err := result.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate release result %s: %w", path, err)
	}
	return result, nil
}

// LoadResults loads every append-only result in deterministic filename order.
// A missing results directory is treated as zero results so aggregation can
// report every Scenario as missing instead of failing before the gate report.
func LoadResults(resultsDir string) ([]Result, error) {
	entries, err := os.ReadDir(resultsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read release results directory: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	results := make([]Result, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("release results directory contains non-file entry %s", entry.Name())
		}
		if filepath.Ext(entry.Name()) != ".json" {
			return nil, fmt.Errorf("release results directory contains unexpected file %s", entry.Name())
		}
		result, err := LoadResult(filepath.Join(resultsDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		if expected := ResultFilename(result); entry.Name() != expected {
			return nil, fmt.Errorf("release result filename %s must be %s", entry.Name(), expected)
		}
		results = append(results, result)
	}
	return results, nil
}
