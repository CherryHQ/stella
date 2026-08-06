package fsops

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

func TestUnpublishManagedSkillRetainsRevisionAndFilesystemUsesCatalogRoot(t *testing.T) {
	r, directory := managedSkillTestRoot(t)
	managedSkillRevision(t, directory, "skill", managedDigestA, "retained")
	if err := r.SwapManagedSkillTarget(context.Background(), "skill", managedDigestA); err != nil {
		t.Fatal(err)
	}
	filesystem, err := NewFilesystem([]Mount{{Path: sandbox.PathWorkspace, Directory: directory}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = filesystem.Close() })
	if err := filesystem.UnpublishManagedSkill(context.Background(), "/workspace", "skill", managedDigestA); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(directory, "skill")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("direct selection after unpublish = %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, managedSkillRevisionPath("skill", managedDigestA))); err != nil {
		t.Fatalf("retained revision = %v", err)
	}
}

func TestUnpublishManagedSkillConflictsWithoutChangingDirectEntry(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Root, string)
	}{
		{"absent", func(*Root, string) {}},
		{"stale", func(r *Root, directory string) {
			managedSkillRevision(t, directory, "skill", managedDigestA, "old")
			if err := r.SwapManagedSkillTarget(context.Background(), "skill", managedDigestA); err != nil {
				t.Fatal(err)
			}
		}},
		{"ordinary file", func(_ *Root, directory string) {
			if err := os.WriteFile(filepath.Join(directory, "skill"), []byte("ordinary"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"ordinary directory", func(_ *Root, directory string) {
			if err := os.Mkdir(filepath.Join(directory, "skill"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"foreign link", func(_ *Root, directory string) {
			if err := os.Symlink(".stella-revisions/other/"+managedDigestA, filepath.Join(directory, "skill")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, directory := managedSkillTestRoot(t)
			tc.setup(r, directory)
			before, err := os.Lstat(filepath.Join(directory, "skill"))
			if err != nil && tc.name != "absent" {
				t.Fatal(err)
			}
			err = r.UnpublishManagedSkillAt(context.Background(), ".", "skill", managedDigestB)
			if !errors.Is(err, sandbox.ErrManagedSkillConflict) || sandbox.IsOutcomeUnknown(err) {
				t.Fatalf("unpublish error = %v, want definite conflict", err)
			}
			after, afterErr := os.Lstat(filepath.Join(directory, "skill"))
			if tc.name == "absent" {
				if !errors.Is(afterErr, fs.ErrNotExist) {
					t.Fatalf("absent entry changed: %v", afterErr)
				}
				return
			}
			if afterErr != nil || after.Mode() != before.Mode() {
				t.Fatalf("conflict changed direct entry: before=%v after=%v/%v", before.Mode(), after, afterErr)
			}
		})
	}
}

func TestUnpublishManagedSkillPostUnlinkFailuresAreOutcomeUnknown(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Root, string, context.CancelFunc)
	}{
		{"cancellation", func(r *Root, _ string, cancel context.CancelFunc) { r.afterManagedSkillUnlink = cancel }},
		{"verification replacement", func(r *Root, directory string, _ context.CancelFunc) {
			r.afterManagedSkillUnlink = func() {
				if err := os.WriteFile(filepath.Join(directory, "skill"), []byte("replacement"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"sync", func(r *Root, _ string, _ context.CancelFunc) {
			r.syncManagedDirectoryError = func(string) error { return errors.New("sync failed") }
		}},
		{"catalog close", func(r *Root, _ string, _ context.CancelFunc) {
			r.closeManagedSkillCatalog = func(*Root) error { return errors.New("catalog close failed") }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, directory := managedSkillTestRoot(t)
			managedSkillRevision(t, directory, "skill", managedDigestA, "retained")
			if err := r.SwapManagedSkillTarget(context.Background(), "skill", managedDigestA); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tc.setup(r, directory, cancel)
			if err := r.UnpublishManagedSkillAt(ctx, ".", "skill", managedDigestA); !sandbox.IsOutcomeUnknown(err) {
				t.Fatalf("unpublish error = %v, want outcome unknown", err)
			}
			if _, err := os.Stat(filepath.Join(directory, managedSkillRevisionPath("skill", managedDigestA))); err != nil {
				t.Fatalf("retained revision = %v", err)
			}
		})
	}
}

func TestUnpublishManagedSkillHelperConflictRoundTrips(t *testing.T) {
	request, err := EncodeRequest(Request{Version: ProtocolVersion, Operation: "unpublish_managed_skill", CatalogRoot: ".", Path: "skill", Digest: managedDigestA})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Serve(context.Background(), t.TempDir(), bytes.NewReader(request), &out); err != nil {
		t.Fatal(err)
	}
	response, err := DecodeResponse(&out, KindMutation)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(ResponseError(response), sandbox.ErrManagedSkillConflict) {
		t.Fatalf("helper conflict = %v", ResponseError(response))
	}
	if _, err := EncodeRequest(Request{Version: ProtocolVersion, Operation: "unpublish_managed_skill", CatalogRoot: ".", Path: "skill", Digest: managedDigestA, BodyLength: 1}); err == nil {
		t.Fatal("unpublish body accepted")
	}
	if _, err := DecodeResponse(bytes.NewReader([]byte{0, 0, 0, 2, '{', '}'}), KindMutation); err == nil {
		t.Fatal("malformed helper response accepted")
	}
}

func TestUnpublishManagedSkillPinsNestedCatalogBeforeParentSwap(t *testing.T) {
	r, directory := managedSkillTestRoot(t)
	original := filepath.Join(directory, "nested", "catalog")
	managedSkillRevision(t, original, "skill", managedDigestA, "retained")
	if err := os.Symlink(managedSkillRevisionPath("skill", managedDigestA), filepath.Join(original, "skill")); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(directory, "nested", "replacement")
	var synced []string
	r.syncManagedDirectory = func(p string) { synced = append(synced, p) }
	r.afterManagedSkillCatalogPin = func() {
		if err := os.Rename(original, filepath.Join(directory, "nested", "original")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(replacement, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(replacement, "skill"), []byte("replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("replacement", original); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.UnpublishManagedSkillAt(context.Background(), "nested/catalog", "skill", managedDigestA); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(directory, "nested", "original", "skill")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("pinned original selection after unpublish = %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(replacement, "skill")); err != nil || string(got) != "replacement" {
		t.Fatalf("replacement catalog changed: %q, %v", got, err)
	}
	if len(synced) != 1 || synced[0] != "." {
		t.Fatalf("pinned catalog sync paths = %q, want [.]", synced)
	}
}

func TestUnpublishManagedSkillRejectsNestedSymlinkCatalog(t *testing.T) {
	r, directory := managedSkillTestRoot(t)
	if err := os.Mkdir(filepath.Join(directory, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("other", filepath.Join(directory, "nested", "catalog")); err != nil {
		t.Fatal(err)
	}
	if err := r.UnpublishManagedSkillAt(context.Background(), "nested/catalog", "skill", managedDigestA); err == nil || errors.Is(err, sandbox.ErrManagedSkillConflict) {
		t.Fatalf("symlink catalog unpublish = %v, want closed root error", err)
	}
}

func TestUnpublishManagedSkillRacesOrdinaryDirectoryWithPOSIXWinnerSemantics(t *testing.T) {
	for range 16 {
		r, directory := managedSkillTestRoot(t)
		managedSkillRevision(t, directory, "skill", managedDigestA, "retained")
		if err := r.SwapManagedSkillTarget(context.Background(), "skill", managedDigestA); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wait sync.WaitGroup
		wait.Add(2)
		var unpublishErr error
		go func() {
			defer wait.Done()
			<-start
			unpublishErr = r.UnpublishManagedSkillAt(context.Background(), ".", "skill", managedDigestA)
		}()
		go func() {
			defer wait.Done()
			<-start
			_ = os.Remove(filepath.Join(directory, "skill"))
			if err := os.Mkdir(filepath.Join(directory, "skill"), 0o755); err == nil {
				_ = os.WriteFile(filepath.Join(directory, "skill", "ordinary"), []byte("keep"), 0o600)
			}
		}()
		close(start)
		wait.Wait()
		info, err := os.Lstat(filepath.Join(directory, "skill"))
		if errors.Is(err, fs.ErrNotExist) {
			continue // unlink won before the ordinary writer installed its directory.
		}
		if err != nil || !info.IsDir() {
			t.Fatalf("race produced unsafe direct entry: info=%v err=%v unpublish=%v", info, err, unpublishErr)
		}
		if _, err := os.Stat(filepath.Join(directory, "skill", "ordinary")); err != nil {
			t.Fatalf("ordinary writer's directory was recursively removed: %v (unpublish=%v)", err, unpublishErr)
		}
	}
}
