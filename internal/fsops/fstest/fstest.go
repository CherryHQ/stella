// Package fstest holds the provider-neutral conformance suite for the sandbox
// Filesystem boundary. Every provider (the in-process fsops library, the local
// and none sessions, and the real Docker adapter) is expected to satisfy the
// identical guarantees, so the checks live here once and are driven through the
// sandbox.Filesystem interface alone.
//
// The suite never touches host coordinates: it verifies state by reading back
// through the Filesystem, and it plants boundary-probing symlinks only through
// the provider-supplied Harness hook. This keeps it runnable inside a container
// where no host path is reachable.
package fstest

import (
	"bytes"
	"context"
	"errors"
	"io"
	iofs "io/fs"
	"runtime"
	"strconv"
	"strings"
	"testing"

	sandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func mode(perm uint32) iofs.FileMode { return iofs.FileMode(perm) }

// Harness supplies the provider-specific handles the suite needs without
// exposing host coordinates. Providers wire these from their own primitives
// (a host os.Root, an in-container exec, ...).
type Harness struct {
	// FS is the filesystem under test. It must expose a read-write mount rooted
	// at sandbox.PathWorkspace.
	FS sandbox.Filesystem

	// InjectSymlink plants a symlink at the workspace-relative name pointing at
	// the given target verbatim, using provider-native means (host os.Symlink,
	// in-container ln, ...). The parent directory already exists when it is
	// called. A nil hook skips the symlink-policy check; supply it wherever the
	// provider can.
	InjectSymlink func(name, target string) error

	// ReadOnlyPath, when non-empty, is a canonical path under a read-only mount;
	// the suite asserts writes to it are rejected. Empty skips that check.
	ReadOnlyPath string
}

// Run exercises every provider-neutral Filesystem guarantee against h.FS.
func Run(t *testing.T, h Harness) {
	t.Helper()
	ctx := context.Background()
	fs := h.FS

	t.Run("MkdirStatList", func(t *testing.T) {
		mkdir(t, fs, "/workspace/tree", 0o750)
		writeFile(t, fs, "/workspace/tree/a", "alpha", 0o644)
		writeFile(t, fs, "/workspace/tree/b", "beta", 0o644)
		info, err := fs.Stat(ctx, "/workspace/tree")
		if err != nil || !info.IsDir {
			t.Fatalf("stat dir = %#v, %v", info, err)
		}
		fileInfo, err := fs.Stat(ctx, "/workspace/tree/a")
		if err != nil || fileInfo.IsDir || fileInfo.Size != 5 {
			t.Fatalf("stat file = %#v, %v", fileInfo, err)
		}
		entries, err := fs.List(ctx, "/workspace/tree")
		if err != nil {
			t.Fatal(err)
		}
		names := map[string]bool{}
		for _, e := range entries {
			names[e.Name] = true
		}
		if len(entries) != 2 || !names["a"] || !names["b"] {
			t.Fatalf("list = %#v", entries)
		}
	})

	t.Run("PermissionPreservation", func(t *testing.T) {
		mkdir(t, fs, "/workspace/perm", 0o755)
		writeFile(t, fs, "/workspace/perm/f", "old", 0o640)
		// An overwrite that declares no Perm must not alter the existing mode.
		writeFile(t, fs, "/workspace/perm/f", "new", 0)
		info, err := fs.Stat(ctx, "/workspace/perm/f")
		if err != nil || info.Mode.Perm() != 0o640 {
			t.Fatalf("mode = %v (%v), want 0640", info.Mode.Perm(), err)
		}
		if got := readAll(t, fs, "/workspace/perm/f", 16); got != "new" {
			t.Fatalf("content = %q, want new", got)
		}
	})

	t.Run("BoundedReadAndReadLimit", func(t *testing.T) {
		mkdir(t, fs, "/workspace/read", 0o755)
		writeFile(t, fs, "/workspace/read/f", "abcdef", 0o644)
		// MaxBytes == size streams the whole file with no limit error.
		if got := readAll(t, fs, "/workspace/read/f", 6); got != "abcdef" {
			t.Fatalf("exact read = %q", got)
		}
		// MaxBytes < size truncates and reports ErrReadLimit after the bytes.
		r, _, err := fs.Read(ctx, "/workspace/read/f", sandbox.ReadOptions{MaxBytes: 2})
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(r)
		_ = r.Close()
		if string(data) != "ab" || !errors.Is(readErr, sandbox.ErrReadLimit) {
			t.Fatalf("bounded read = %q, %v", data, readErr)
		}
	})

	t.Run("RenameRemove", func(t *testing.T) {
		mkdir(t, fs, "/workspace/mv", 0o755)
		writeFile(t, fs, "/workspace/mv/a", "x", 0o644)
		if !supportsNoReplaceRename() {
			err := fs.Rename(ctx, "/workspace/mv/a", "/workspace/mv/b")
			if err == nil || !strings.Contains(err.Error(), "atomic no-replace rename is unsupported") {
				t.Fatalf("unsupported rename = %v, want clear no-replace rejection", err)
			}
			return
		}
		if err := fs.Rename(ctx, "/workspace/mv/a", "/workspace/mv/b"); err != nil {
			t.Fatal(err)
		}
		if _, err := fs.Stat(ctx, "/workspace/mv/a"); err == nil {
			t.Fatal("stale name still present after rename")
		}
		if _, err := fs.Stat(ctx, "/workspace/mv/b"); err != nil {
			t.Fatalf("renamed target missing: %v", err)
		}
		if err := fs.Remove(ctx, "/workspace/mv/b", false); err != nil {
			t.Fatal(err)
		}
		if _, err := fs.Stat(ctx, "/workspace/mv/b"); err == nil {
			t.Fatal("removed file still present")
		}
	})

	t.Run("RenameNeverReplacesDestination", func(t *testing.T) {
		if !supportsNoReplaceRename() {
			t.Skip("atomic no-replace rename is unsupported on this platform")
		}
		mkdir(t, fs, "/workspace/no-replace", 0o755)
		writeFile(t, fs, "/workspace/no-replace/source", "source", 0o644)
		writeFile(t, fs, "/workspace/no-replace/destination", "destination", 0o644)
		err := fs.Rename(ctx, "/workspace/no-replace/source", "/workspace/no-replace/destination")
		if !errors.Is(err, iofs.ErrExist) {
			t.Fatalf("rename existing destination = %v, want iofs.ErrExist", err)
		}
		if got := readAll(t, fs, "/workspace/no-replace/source", 16); got != "source" {
			t.Fatalf("source after rejected rename = %q, want preserved source", got)
		}
		if got := readAll(t, fs, "/workspace/no-replace/destination", 16); got != "destination" {
			t.Fatalf("destination after rejected rename = %q, want preserved destination", got)
		}
	})

	t.Run("RenameFileAndDirectoryToAbsentDestination", func(t *testing.T) {
		if !supportsNoReplaceRename() {
			t.Skip("atomic no-replace rename is unsupported on this platform")
		}
		mkdir(t, fs, "/workspace/rename-absent", 0o755)
		writeFile(t, fs, "/workspace/rename-absent/file-source", "file", 0o644)
		if err := fs.Rename(ctx, "/workspace/rename-absent/file-source", "/workspace/rename-absent/file-destination"); err != nil {
			t.Fatalf("rename file to absent destination: %v", err)
		}
		mkdir(t, fs, "/workspace/rename-absent/directory-source", 0o755)
		writeFile(t, fs, "/workspace/rename-absent/directory-source/child", "child", 0o644)
		if err := fs.Rename(ctx, "/workspace/rename-absent/directory-source", "/workspace/rename-absent/directory-destination"); err != nil {
			t.Fatalf("rename directory to absent destination: %v", err)
		}
		if _, err := fs.Stat(ctx, "/workspace/rename-absent/directory-source"); !errors.Is(err, iofs.ErrNotExist) {
			t.Fatalf("source directory after rename = %v, want iofs.ErrNotExist", err)
		}
		if got := readAll(t, fs, "/workspace/rename-absent/directory-destination/child", 16); got != "child" {
			t.Fatalf("moved directory child = %q, want child", got)
		}
	})

	t.Run("ConcurrentDestinationCreatorNeverGetsOverwritten", func(t *testing.T) {
		if !supportsNoReplaceRename() {
			t.Skip("atomic no-replace rename is unsupported on this platform")
		}
		mkdir(t, fs, "/workspace/rename-race", 0o755)
		// The two operations start together. Exactly one owns the destination;
		// every result is checked, so this remains a regression test regardless
		// of scheduler choice. The pre-created-destination case above separately
		// makes the creator-wins path deterministic.
		for attempt := range 128 {
			suffix := strconv.Itoa(attempt)
			source := "/workspace/rename-race/source-" + suffix
			destination := "/workspace/rename-race/destination-" + suffix
			writeFile(t, fs, source, "source", 0o644)
			start := make(chan struct{})
			renamed := make(chan error, 1)
			created := make(chan error, 1)
			go func() {
				<-start
				renamed <- fs.Rename(ctx, source, destination)
			}()
			go func() {
				<-start
				created <- fs.Mkdir(ctx, destination, 0o755)
			}()
			close(start)
			renameErr, createErr := <-renamed, <-created
			switch {
			case createErr == nil && errors.Is(renameErr, iofs.ErrExist):
				info, err := fs.Stat(ctx, destination)
				if err != nil || !info.IsDir {
					t.Fatalf("attempt %d creator destination = %#v, %v; want directory", attempt, info, err)
				}
				if got := readAll(t, fs, source, 16); got != "source" {
					t.Fatalf("attempt %d source after creator won = %q, want preserved source", attempt, got)
				}
			case renameErr == nil && createErr != nil:
				if got := readAll(t, fs, destination, 16); got != "source" {
					t.Fatalf("attempt %d destination after rename won = %q, want source", attempt, got)
				}
				if _, err := fs.Stat(ctx, source); !errors.Is(err, iofs.ErrNotExist) {
					t.Fatalf("attempt %d source after rename won = %v, want iofs.ErrNotExist", attempt, err)
				}
			default:
				t.Fatalf("attempt %d rename=%v creator=%v, want exactly one success", attempt, renameErr, createErr)
			}
		}
	})

	t.Run("ContainmentEscape", func(t *testing.T) {
		for _, p := range []string{"/workspace/../escape", "/workspace/./../escape"} {
			if _, err := fs.Stat(ctx, p); err == nil {
				t.Fatalf("accepted containment escape %q", p)
			}
		}
	})

	t.Run("MissingIsNotExist", func(t *testing.T) {
		if _, err := fs.Stat(ctx, "/workspace/absent"); !errors.Is(err, iofs.ErrNotExist) {
			t.Fatalf("stat missing = %v, want iofs.ErrNotExist", err)
		}
		if _, _, err := fs.Read(ctx, "/workspace/absent", sandbox.ReadOptions{MaxBytes: 16}); !errors.Is(err, iofs.ErrNotExist) {
			t.Fatalf("read missing = %v, want iofs.ErrNotExist", err)
		}
	})

	t.Run("SymlinkPolicy", func(t *testing.T) {
		if h.InjectSymlink == nil {
			t.Skip("provider cannot inject a symlink")
		}
		mkdir(t, fs, "/workspace/sym", 0o755)
		// An escaping symlink (absolute target outside every mount) must fail
		// closed on access, matching os.Root containment — the security property
		// is enforced at use, not by a racy pre-check.
		if err := h.InjectSymlink("sym/escape", "/etc"); err != nil {
			t.Fatalf("inject escaping symlink: %v", err)
		}
		if _, err := fs.Stat(ctx, "/workspace/sym/escape"); err == nil {
			t.Fatal("stat traversed an escaping symlink")
		}
		if _, _, err := fs.Read(ctx, "/workspace/sym/escape", sandbox.ReadOptions{MaxBytes: 16}); err == nil {
			t.Fatal("read traversed an escaping symlink")
		}
		// A symlink contained within the mount resolves under ordinary POSIX
		// semantics; the boundary does not blanket-reject every symlink.
		writeFile(t, fs, "/workspace/sym/target", "linked", 0o644)
		if err := h.InjectSymlink("sym/alias", "target"); err != nil {
			t.Fatalf("inject contained symlink: %v", err)
		}
		info, err := fs.Stat(ctx, "/workspace/sym/alias")
		if err != nil || info.IsDir {
			t.Fatalf("contained symlink did not resolve: %#v, %v", info, err)
		}
		if got := readAll(t, fs, "/workspace/sym/alias", 16); got != "linked" {
			t.Fatalf("contained symlink read = %q, want linked", got)
		}
	})

	t.Run("LargeStreamingUpload", func(t *testing.T) {
		mkdir(t, fs, "/workspace/big", 0o755)
		const size = 1<<20 + 1
		payload := strings.Repeat("z", size)
		uploadFile(t, fs, "/workspace/big/blob", payload, 0o644)
		info, err := fs.Stat(ctx, "/workspace/big/blob")
		if err != nil || info.Size != int64(size) {
			t.Fatalf("large upload = %#v, %v", info, err)
		}
		if got := readAll(t, fs, "/workspace/big/blob", size); len(got) != size || got != payload {
			t.Fatalf("large read = %d bytes", len(got))
		}
	})

	t.Run("ConcurrentWrite", func(t *testing.T) {
		// The boundary promises ordinary POSIX write semantics, not transactional
		// whole-writer atomicity. We assert only what POSIX guarantees: every
		// concurrent write completes once with no framework-level retry, and the
		// result stays contained and readable through the boundary, made of bytes
		// some writer actually sent. Any POSIX-valid interleaving is accepted; we
		// do not require the file to equal one writer's payload exactly.
		mkdir(t, fs, "/workspace/race", 0o755)
		writers := []string{strings.Repeat("a", 4096), strings.Repeat("b", 4096)}
		writeFile(t, fs, "/workspace/race/f", writers[0], 0o644)
		done := make(chan error, len(writers))
		for _, payload := range writers {
			go func(payload string) {
				length := int64(len(payload))
				done <- fs.Write(ctx, "/workspace/race/f", strings.NewReader(payload), sandbox.WriteOptions{ContentLength: &length})
			}(payload)
		}
		for range writers {
			if err := <-done; err != nil {
				t.Fatalf("concurrent write: %v", err)
			}
		}
		got := readAll(t, fs, "/workspace/race/f", 1<<20)
		// A single O_TRUNC writer emits at most 4096 bytes at offset 0; a longer
		// file would mean an append/retry/double-write, an empty one a lost write.
		if len(got) == 0 || len(got) > 4096 {
			t.Fatalf("concurrent write produced %d bytes, outside the POSIX-valid range", len(got))
		}
		for i := 0; i < len(got); i++ {
			if got[i] != 'a' && got[i] != 'b' {
				t.Fatalf("byte %d = %q was not written by any writer", i, got[i])
			}
		}
	})

	t.Run("ReadOnlyRejection", func(t *testing.T) {
		if h.ReadOnlyPath == "" {
			t.Skip("provider exposes no read-only mount")
		}
		length := int64(2)
		err := fs.Write(ctx, h.ReadOnlyPath, strings.NewReader("no"), sandbox.WriteOptions{ContentLength: &length})
		if err == nil {
			t.Fatalf("write to read-only path %q was accepted", h.ReadOnlyPath)
		}
		if sandbox.IsOutcomeUnknown(err) {
			t.Fatalf("read-only rejection must be definite, got outcome-unknown: %v", err)
		}
	})
}

func supportsNoReplaceRename() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "linux"
}

func mkdir(t *testing.T, fs sandbox.Filesystem, p string, perm uint32) {
	t.Helper()
	if err := fs.Mkdir(context.Background(), p, mode(perm)); err != nil {
		t.Fatalf("mkdir %q: %v", p, err)
	}
}

func writeFile(t *testing.T, fs sandbox.Filesystem, p, data string, perm uint32) {
	t.Helper()
	length := int64(len(data))
	if err := fs.Write(context.Background(), p, strings.NewReader(data), sandbox.WriteOptions{Perm: mode(perm), ContentLength: &length}); err != nil {
		t.Fatalf("write %q: %v", p, err)
	}
}

func uploadFile(t *testing.T, fs sandbox.Filesystem, p, data string, perm uint32) {
	t.Helper()
	length := int64(len(data))
	if err := fs.Upload(context.Background(), p, strings.NewReader(data), sandbox.WriteOptions{Perm: mode(perm), ContentLength: &length}); err != nil {
		t.Fatalf("upload %q: %v", p, err)
	}
}

func readAll(t *testing.T, fs sandbox.Filesystem, p string, maxBytes int64) string {
	t.Helper()
	r, _, err := fs.Read(context.Background(), p, sandbox.ReadOptions{MaxBytes: maxBytes})
	if err != nil {
		t.Fatalf("read %q: %v", p, err)
	}
	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	_ = r.Close()
	if err != nil {
		t.Fatalf("read body %q: %v", p, err)
	}
	return buf.String()
}
