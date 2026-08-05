package access

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestWorkspaceMoveRejectsExistingDestinationInOneCallback(t *testing.T) {
	svc, runtime, _, authority := newRuntimeTestService(t)
	filesystem := newWorkspaceFakeFilesystem()
	filesystem.files["/workspace/source.txt"] = []byte("source")
	filesystem.files["/workspace/destination.txt"] = []byte("destination")
	runtime.filesystem = filesystem
	access, err := svc.Begin(context.Background(), authority)
	if err != nil {
		t.Fatal(err)
	}
	_, err = access.MoveWorkspacePath(context.Background(), WorkspaceMoveInput{AgentID: "a1", SessionID: "s1", Scope: WorkspaceScopeAgent, Path: "source.txt", NewPath: "destination.txt"})
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("move error = %v, want ErrAlreadyExists", err)
	}
	if runtime.filesystemCalls != 1 || filesystem.renames != 0 {
		t.Fatalf("callbacks=%d renames=%d, want one callback and no rename", runtime.filesystemCalls, filesystem.renames)
	}
	if got := string(filesystem.files["/workspace/source.txt"]); got != "source" {
		t.Fatalf("source = %q, want preserved", got)
	}
	if got := string(filesystem.files["/workspace/destination.txt"]); got != "destination" {
		t.Fatalf("destination = %q, want preserved", got)
	}
}

func TestWorkspaceMoveMapsAtomicRenameConflict(t *testing.T) {
	svc, runtime, _, authority := newRuntimeTestService(t)
	filesystem := newWorkspaceFakeFilesystem()
	filesystem.files["/workspace/source.txt"] = []byte("source")
	filesystem.renameErr = syscall.EEXIST // destination appeared after the pre-Stat.
	runtime.filesystem = filesystem
	access, err := svc.Begin(context.Background(), authority)
	if err != nil {
		t.Fatal(err)
	}
	_, err = access.MoveWorkspacePath(context.Background(), WorkspaceMoveInput{AgentID: "a1", SessionID: "s1", Scope: WorkspaceScopeAgent, Path: "source.txt", NewPath: "destination.txt"})
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("move error = %v, want ErrAlreadyExists", err)
	}
	if filesystem.renames != 1 {
		t.Fatalf("renames=%d, want atomic rename attempt", filesystem.renames)
	}
	if got := string(filesystem.files["/workspace/source.txt"]); got != "source" {
		t.Fatalf("source = %q, want preserved", got)
	}
}

func TestWorkspaceListSortsProviderEntriesBeforeCounting(t *testing.T) {
	svc, runtime, _, authority := newRuntimeTestService(t)
	filesystem := newWorkspaceFakeFilesystem()
	filesystem.unsorted = true
	filesystem.files["/workspace/z.txt"] = []byte("z")
	filesystem.files["/workspace/a.txt"] = []byte("aa")
	filesystem.dirs["/workspace/middle"] = true
	filesystem.files["/workspace/middle/b.txt"] = []byte("bbb")
	runtime.filesystem = filesystem
	access, err := svc.Begin(context.Background(), authority)
	if err != nil {
		t.Fatal(err)
	}
	info, err := access.ListWorkspace(context.Background(), WorkspaceListInput{AgentID: "a1", SessionID: "s1", Scope: WorkspaceScopeAgent, Depth: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(info.Paths, ","), "a.txt,middle/,middle/b.txt,z.txt"; got != want {
		t.Fatalf("paths = %q, want %q", got, want)
	}
	if info.TotalFiles != 3 || info.TotalDirs != 1 || info.TotalBytes != 6 {
		t.Fatalf("totals = %+v, want files=3 dirs=1 bytes=6", info)
	}
}

func TestWorkspaceReadRejectsMalformedProviderFramingAndPropagatesClose(t *testing.T) {
	tests := []struct {
		name      string
		data      string
		info      pkgsandbox.FileInfo
		closeErr  error
		wantError error
	}{
		{"short metadata", "abc", pkgsandbox.FileInfo{Mode: 0o644, Size: 4}, nil, ErrInvalid},
		{"long metadata", "abcd", pkgsandbox.FileInfo{Mode: 0o644, Size: 3}, nil, ErrInvalid},
		{"negative size", "abc", pkgsandbox.FileInfo{Mode: 0o644, Size: -1}, nil, ErrInvalid},
		{"non regular", "abc", pkgsandbox.FileInfo{Mode: fs.ModeSymlink, Size: 3}, nil, ErrInvalid},
		{"directory", "", pkgsandbox.FileInfo{Mode: fs.ModeDir, IsDir: true}, nil, ErrIsDir},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, runtime, _, authority := newRuntimeTestService(t)
			filesystem := newWorkspaceFakeFilesystem()
			filesystem.files["/workspace/file.txt"] = []byte(tt.data)
			filesystem.readInfo = &tt.info
			filesystem.readCloseErr = tt.closeErr
			runtime.filesystem = filesystem
			access, err := svc.Begin(context.Background(), authority)
			if err != nil {
				t.Fatal(err)
			}
			result, err := access.ReadWorkspacePath(context.Background(), WorkspaceReadInput{AgentID: "a1", SessionID: "s1", Scope: WorkspaceScopeAgent, Path: "file.txt"})
			if !errors.Is(err, tt.wantError) || !emptyWorkspaceReadResult(result) {
				t.Fatalf("read = %#v, %v; want no result and %v", result, err, tt.wantError)
			}
		})
	}

	t.Run("close error", func(t *testing.T) {
		svc, runtime, _, authority := newRuntimeTestService(t)
		filesystem := newWorkspaceFakeFilesystem()
		filesystem.files["/workspace/file.txt"] = []byte("abc")
		closeErr := errors.New("provider close failed")
		filesystem.readCloseErr = closeErr
		runtime.filesystem = filesystem
		access, err := svc.Begin(context.Background(), authority)
		if err != nil {
			t.Fatal(err)
		}
		result, err := access.ReadWorkspacePath(context.Background(), WorkspaceReadInput{AgentID: "a1", SessionID: "s1", Scope: WorkspaceScopeAgent, Path: "file.txt"})
		if !errors.Is(err, closeErr) || !emptyWorkspaceReadResult(result) {
			t.Fatalf("read = %#v, %v; want close error and no result", result, err)
		}
	})
}

func emptyWorkspaceReadResult(result WorkspaceReadResult) bool {
	return result.Path == "" && result.Content == "" && result.Language == "" && !result.Raw && result.RawName == "" && result.RawMediaType == "" && len(result.RawContent) == 0 && result.RawModTime.IsZero()
}

func TestWorkspaceUploadUsesUniqueUUIDv7NamesForConcurrentSameName(t *testing.T) {
	svc, runtime, _, authority := newRuntimeTestService(t)
	filesystem := newWorkspaceFakeFilesystem()
	runtime.filesystem = filesystem
	access, err := svc.Begin(context.Background(), authority)
	if err != nil {
		t.Fatal(err)
	}
	const uploads = 16
	results := make(chan WorkspaceUploadResult, uploads)
	errs := make(chan error, uploads)
	var group sync.WaitGroup
	for range uploads {
		group.Go(func() {
			result, err := access.UploadWorkspacePath(context.Background(), WorkspaceUploadInput{AgentID: "a1", SessionID: "s1", Filename: "same-name.txt", Reader: strings.NewReader("payload"), Now: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)})
			results <- result
			errs <- err
		})
	}
	group.Wait()
	close(results)
	close(errs)
	seen := map[string]bool{}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for result := range results {
		if seen[result.RelativePath] {
			t.Fatalf("duplicate upload path %q", result.RelativePath)
		}
		seen[result.RelativePath] = true
	}
	if len(seen) != uploads || len(filesystem.files) != uploads || runtime.filesystemCalls != uploads {
		t.Fatalf("paths=%d files=%d callbacks=%d, want %d", len(seen), len(filesystem.files), runtime.filesystemCalls, uploads)
	}
}
