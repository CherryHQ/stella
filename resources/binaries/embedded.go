package binaries

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Keep synchronized with xbergVersion in gen.go.
const xbergVersion = "1.0.14"

const shellEnvFilename = ".stella-shell-env"

// Every runtime Stella installs under $STELLA_HOME/bin obeys one permission
// contract, whether it is a single file (mise) or a versioned bundle directory
// (Xberg): anything reachable from bin/ must stay readable, and executables
// runnable, by any UID that has bin/ on PATH. The installing UID is not always
// the running one — the sandbox image takes its UID as a build arg — and the
// creating syscalls do not honor these modes on their own: os.MkdirTemp always
// creates 0700 and ignores umask, and OpenFile's mode is masked by umask. Both
// install paths therefore set the mode explicitly rather than trusting defaults,
// so neither can drift into being privately owned while the other works.
const (
	toolDirMode  = 0o755
	toolExecMode = 0o755
	toolDataMode = 0o644
)

//go:embed shell_env.sh
var shellEnv []byte

type ensureState struct {
	once sync.Once
	err  error
}

var (
	ensureMu     sync.Mutex
	ensureStates = make(map[string]*ensureState)
)

// EnsureTools extracts Stella's embedded runtimes to stellaHome/bin/. mise is a
// single compressed executable; Xberg is kept as a directory because its
// official Linux and macOS bundles include adjacent dynamic libraries.
// Already-extracted runtimes are skipped. Safe for concurrent calls.
func EnsureTools(stellaHome string) error {
	destDir := BinDir(stellaHome)

	ensureMu.Lock()
	state := ensureStates[destDir]
	if state == nil {
		state = &ensureState{}
		ensureStates[destDir] = state
	}
	ensureMu.Unlock()

	state.once.Do(func() {
		state.err = extractTools(destDir)
	})
	return state.err
}

// BinDir returns the tool binaries directory path.
func BinDir(stellaHome string) string {
	return filepath.Join(stellaHome, "bin")
}

// ToolPath returns the full path to a named tool binary, or empty if not embedded.
func ToolPath(stellaHome, name string) string {
	p := filepath.Join(BinDir(stellaHome), name)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// ToolNames returns the names of all embedded tools for the current platform.
func ToolNames() []string {
	entries, err := platformEntries()
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		name, ok := embeddedToolName(e.Name())
		if ok {
			names = append(names, name)
		}
	}
	return names
}

// VerifyTools checks that every tool present in the embedded FS was successfully
// extracted to stellaHome/bin. If the embedded FS has no tool archives (e.g. a
// dev build where pre-build dependency sync was not run), the check is skipped and nil is
// returned — the missing-binary error surfaces later when the tool is actually
// needed. Returns an error only when the FS is non-empty but one or more
// binaries are missing on disk after extraction.
func VerifyTools(stellaHome string) error {
	names := ToolNames()
	if len(names) == 0 {
		return nil // no tools embedded; skip verification
	}
	var missing []string
	for _, name := range names {
		if ToolPath(stellaHome, name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("embedded tools missing in %s after extraction: %s",
			BinDir(stellaHome), strings.Join(missing, ", "))
	}
	// Present is not the same as usable. Windows has no POSIX mode bits, so the
	// contract is only meaningful — and only enforceable — elsewhere.
	if runtime.GOOS == "windows" {
		return nil
	}
	for _, name := range names {
		if _, err := walkToolInstall(BinDir(stellaHome), name, requirePerm); err != nil {
			return err
		}
	}
	return nil
}

// walkToolInstall yields every path belonging to one installed runtime together
// with the mode the contract requires for it, so repair and verification can
// never disagree about what "correct" means. It reports false when the runtime
// is not installed yet; that is a skip, not an error.
func walkToolInstall(binDir, name string, fn func(path string, want os.FileMode) error) (bool, error) {
	// Compare resolved against resolved. A symlinked ancestor — /var → /private/var
	// on macOS, or an operator's symlinked STELLA_HOME — otherwise makes every
	// single-file runtime look like it lives in a bundle directory.
	root, err := filepath.EvalSymlinks(binDir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("resolve embedded tool dir %s: %w", binDir, err)
	}
	// A dangling launcher symlink resolves to ErrNotExist too, which is the same
	// "nothing installed here yet" case: extraction, not repair, will fix it.
	target, err := filepath.EvalSymlinks(filepath.Join(binDir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("resolve embedded tool %s: %w", name, err)
	}
	dir := filepath.Dir(target)
	if dir == root {
		// Single-file runtime (mise): the executable is the entire install.
		return true, fn(target, toolExecMode)
	}
	// Bundle runtime (Xberg): the dynamic linker reads the adjacent libraries
	// through this directory, so the directory and every file in it count.
	if err := fn(dir, toolDirMode); err != nil {
		return true, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true, fmt.Errorf("read embedded tool bundle %s: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		want := os.FileMode(toolDataMode)
		if path == target {
			want = toolExecMode
		}
		if err := fn(path, want); err != nil {
			return true, err
		}
	}
	return true, nil
}

// repairToolPermissions widens an install written by an earlier Stella that left
// paths owner-only. It runs on every startup because the archive fingerprint
// makes such an install byte-identical to a correct one, so the extraction fast
// path would skip it forever. Without this, tightening VerifyTools would turn a
// merely-degraded deployment into one that refuses to start.
func repairToolPermissions(binDir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	for _, name := range ToolNames() {
		_, err := walkToolInstall(binDir, name, func(path string, want os.FileMode) error {
			info, err := os.Lstat(path)
			if err != nil {
				return fmt.Errorf("stat embedded tool path %s: %w", path, err)
			}
			if info.Mode().Perm() == want {
				return nil
			}
			return os.Chmod(path, want)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func requirePerm(path string, want os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat embedded tool path %s: %w", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		return fmt.Errorf("embedded tool path %s has mode %o, want %o: a UID other than the installing one cannot use it", path, got, want)
	}
	return nil
}

func allToolsExtracted(destDir string, entries []fs.DirEntry) bool {
	for _, e := range entries {
		name, ok := embeddedToolName(e.Name())
		if !ok {
			continue
		}
		if _, err := os.Stat(filepath.Join(destDir, name)); err != nil {
			return false
		}
	}
	return true
}

func extractTools(destDir string) error {
	if err := os.MkdirAll(destDir, toolDirMode); err != nil {
		return fmt.Errorf("create bin dir: %w", err)
	}
	// This file is not part of the platform archive fingerprint. Always refresh
	// it so an upgrade that changes only shell startup behavior cannot be skipped
	// by an already-current embedded tool installation.
	shellEnvPath := filepath.Join(destDir, shellEnvFilename)
	if err := os.WriteFile(shellEnvPath, shellEnv, toolDataMode); err != nil {
		return fmt.Errorf("write managed shell environment: %w", err)
	}
	if err := os.Chmod(shellEnvPath, toolDataMode); err != nil {
		return fmt.Errorf("set managed shell environment mode: %w", err)
	}

	// Also not part of the archive fingerprint: the modes of an already-installed
	// runtime. Reassert them before the fast path below can skip extraction.
	if err := repairToolPermissions(destDir); err != nil {
		return fmt.Errorf("repair embedded tool permissions: %w", err)
	}

	entries, err := platformEntries()
	if err != nil {
		return fmt.Errorf("read embedded tools: %w", err)
	}

	fp := fingerprint()
	fpFile := filepath.Join(destDir, ".embedded-version")
	if old, err := os.ReadFile(fpFile); err == nil && string(old) == fp {
		if allToolsExtracted(destDir, entries) {
			return nil // already up to date
		}
	}

	for _, entry := range entries {
		name := entry.Name()
		if tool, ok := toolNameForEntry(name); ok {
			if err := extractGzip(toolsDir+"/"+name, filepath.Join(destDir, tool)); err != nil {
				return fmt.Errorf("extract %s: %w", tool, err)
			}
			continue
		}
		if name == xbergArchiveName() {
			if err := extractXbergBundle(toolsDir+"/"+name, destDir); err != nil {
				return fmt.Errorf("extract Xberg: %w", err)
			}
		}
	}

	return os.WriteFile(fpFile, []byte(fp), toolDataMode)
}

// fingerprint returns a quick identifier based on embedded file names and sizes.
// Changes when tool versions are bumped (different binary sizes).
func fingerprint() string {
	entries, err := platformEntries()
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, e := range entries {
		if info, err := e.Info(); err == nil {
			fmt.Fprintf(&b, "%s:%d,", e.Name(), info.Size())
		}
	}
	return b.String()
}

func platformEntries() ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(toolsFS, toolsDir)
	if err != nil {
		return nil, err
	}
	var filtered []fs.DirEntry
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if _, ok := embeddedToolName(entry.Name()); ok {
			filtered = append(filtered, entry)
		}
	}
	return filtered, nil
}

// infraTools lists standalone gzip binaries extracted to $STELLA_HOME/bin.
// Only mise belongs here: it bootstraps the install/shim machinery before any
// shim exists. Xberg is handled separately as a versioned runtime bundle;
// ordinary tools (gh, fd, rg, tap, lark-cli, ...) stay behind mise shims.
var infraTools = map[string]bool{"mise": true}

func xbergArchiveName() string { return "xberg-v" + xbergVersion + ".tar.gz" }

func embeddedToolName(entry string) (string, bool) {
	if entry == xbergArchiveName() {
		return "xberg", true
	}
	return toolNameForEntry(entry)
}

func toolNameForEntry(entry string) (string, bool) {
	if !strings.HasSuffix(entry, ".gz") {
		return "", false
	}
	name := strings.TrimSuffix(entry, ".gz")
	// On Windows the embedded entry is "mise.exe.gz"; infraTools keys are the
	// platform-neutral tool name, so strip a trailing ".exe" before the lookup.
	if !infraTools[strings.TrimSuffix(name, ".exe")] {
		return "", false
	}
	return name, true
}

func extractGzip(srcPath, destPath string) error {
	data, err := toolsFS.ReadFile(srcPath)
	if err != nil {
		return err
	}

	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer func() { _ = gr.Close() }()

	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, toolExecMode)
	if err != nil {
		return err
	}

	const maxBinarySize = 200 << 20 // 200 MB safety cap
	if _, err = io.Copy(f, io.LimitReader(gr, maxBinarySize)); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(destPath, toolExecMode)
}

func extractXbergBundle(srcPath, destDir string) error {
	data, err := toolsFS.ReadFile(srcPath)
	if err != nil {
		return err
	}
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer func() { _ = gr.Close() }()

	runtimeDir := filepath.Join(destDir, "xberg-v"+xbergVersion)
	tmpDir, err := os.MkdirTemp(destDir, ".xberg-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	// Set the mode on the staging dir so the atomic rename publishes a directory
	// that is already correct, never a briefly-private one.
	if err := os.Chmod(tmpDir, toolDirMode); err != nil {
		return err
	}

	const maxBundleSize = 300 << 20
	var written int64
	tr := tar.NewReader(gr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		name := filepath.Base(filepath.Clean(h.Name))
		if name == "." || name == string(filepath.Separator) {
			return fmt.Errorf("invalid bundle entry %q", h.Name)
		}
		if h.Size < 0 || written+h.Size > maxBundleSize {
			return fmt.Errorf("bundle exceeds %d bytes", maxBundleSize)
		}
		mode := os.FileMode(toolDataMode)
		if name == "xberg" {
			mode = toolExecMode
		}
		path := filepath.Join(tmpDir, name)
		out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyN(out, tr, h.Size)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if err := os.Chmod(path, mode); err != nil {
			return err
		}
		written += h.Size
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "xberg")); err != nil {
		return fmt.Errorf("bundle executable missing: %w", err)
	}
	if err := os.RemoveAll(runtimeDir); err != nil {
		return err
	}
	if err := os.Rename(tmpDir, runtimeDir); err != nil {
		return err
	}
	launcher := filepath.Join(destDir, "xberg")
	launcherTmp := launcher + ".tmp"
	_ = os.Remove(launcherTmp)
	if err := os.Symlink(filepath.Join(filepath.Base(runtimeDir), "xberg"), launcherTmp); err != nil {
		return err
	}
	return os.Rename(launcherTmp, launcher)
}
