// Package processpath resolves a process name against the PATH environment that
// will actually be given to the child. os/exec.LookPath uses the host process
// environment, which is wrong when a turn replaces PATH. Executable extension
// handling remains the standard library's host-platform behavior.
package processpath

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrPathUnavailable reports that the supplied environment did not contain a
// PATH entry. Callers may preserve legacy host lookup for overlay mode only in
// this case; a present PATH must remain authoritative, including when a name
// is missing from it.
var ErrPathUnavailable = errors.New("PATH is not set")

// Resolve returns an executable path for name using PATH from env. Names that
// already contain a path separator are passed through for the child to report
// the final filesystem error, matching os/exec.Command's behavior.
func Resolve(name string, env []string) (string, error) {
	if hasExplicitPath(name) {
		return name, nil
	}
	pathValue, found := environmentValue(env, "PATH")
	if !found {
		return "", &exec.Error{Name: name, Err: ErrPathUnavailable}
	}
	for _, dir := range filepath.SplitList(pathValue) {
		if dir == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, name)
		// filepath.Join(".", name) drops the dot. Keep a separator so
		// exec.LookPath cannot silently consult the host PATH.
		if !filepath.IsAbs(candidate) && !strings.ContainsRune(candidate, os.PathSeparator) {
			candidate = "." + string(os.PathSeparator) + candidate
		}
		resolved, err := exec.LookPath(candidate)
		if err != nil {
			if errors.Is(err, exec.ErrDot) {
				return resolved, err
			}
			continue
		}
		if !filepath.IsAbs(resolved) {
			return resolved, &exec.Error{Name: name, Err: exec.ErrDot}
		}
		return resolved, nil
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

func hasExplicitPath(name string) bool {
	if strings.ContainsRune(name, os.PathSeparator) {
		return true
	}
	return runtime.GOOS == "windows" && strings.ContainsAny(name, `/\\:`)
}

// HasPath reports whether env contains a PATH key, including an explicitly
// empty PATH.
func HasPath(env []string) bool {
	_, found := environmentValue(env, "PATH")
	return found
}

func environmentValue(env []string, key string) (string, bool) {
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if ok && (name == key || runtime.GOOS == "windows" && strings.EqualFold(name, key)) {
			return value, true
		}
	}
	return "", false
}
