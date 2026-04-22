// Package cliwrap provisions thin POSIX shell wrapper scripts for plugin-owned
// CLIs under a user-owned bin directory. The wrappers rely on environment
// variables injected by the runner to locate the real binaries.
package cliwrap

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

// EnsureWrappers synchronizes wrapper scripts under binDir to exactly the given
// specs. The directory is dedicated to runner-managed wrappers, so stale files
// are removed on every call before the desired scripts are written.
func EnsureWrappers(binDir string, specs []pkgplugins.WrapperSpec) error {
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(binDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(binDir, entry.Name())); err != nil {
			return err
		}
	}

	sorted := append([]pkgplugins.WrapperSpec(nil), specs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, spec := range sorted {
		if err := writeExecutable(filepath.Join(binDir, spec.Name), wrapperScript(spec)); err != nil {
			return err
		}
	}
	return nil
}

// writeExecutable atomically writes content to path with executable permissions.
func writeExecutable(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o755)
}

func wrapperScript(spec pkgplugins.WrapperSpec) string {
	fallback := spec.Fallback
	if fallback == "" {
		fallback = spec.Name
	}
	return fmt.Sprintf("#!/bin/sh\nexec \"${%s:-%s}\" \"$@\"\n", spec.TargetEnvVar, fallback)
}
