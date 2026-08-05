package fsops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

const (
	managedDigestA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	managedDigestB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestManagedSkillTargetCreateInspectAndFlip(t *testing.T) {
	r, directory := managedSkillTestRoot(t)
	name := "release-notes"
	managedSkillRevision(t, directory, name, managedDigestA, "old")
	managedSkillRevision(t, directory, name, managedDigestB, "new")
	if digest, managed, err := r.ManagedSkillTarget(context.Background(), name); err != nil || managed || digest != "" {
		t.Fatalf("absent target = %q, %t, %v", digest, managed, err)
	}
	if err := r.SwapManagedSkillTarget(context.Background(), name, managedDigestA); err != nil {
		t.Fatal(err)
	}
	assertManagedSkillTarget(t, r, name, managedDigestA)
	if err := r.SwapManagedSkillTarget(context.Background(), name, managedDigestB); err != nil {
		t.Fatal(err)
	}
	assertManagedSkillTarget(t, r, name, managedDigestB)
	assertNoManagedSkillTemps(t, directory)
}

func TestManagedSkillTargetTreatsOrdinaryDirectoryAsUnmanaged(t *testing.T) {
	r, directory := managedSkillTestRoot(t)
	if err := os.Mkdir(filepath.Join(directory, "ordinary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if digest, managed, err := r.ManagedSkillTarget(context.Background(), "ordinary"); err != nil || managed || digest != "" {
		t.Fatalf("ordinary directory = %q, %t, %v", digest, managed, err)
	}
	managedSkillRevision(t, directory, "ordinary", managedDigestA, "revision")
	if err := r.SwapManagedSkillTarget(context.Background(), "ordinary", managedDigestA); err == nil {
		t.Fatal("ordinary directory replaced")
	}
}

func TestManagedSkillTargetRejectsInvalidInputsAndLinks(t *testing.T) {
	r, directory := managedSkillTestRoot(t)
	name := "valid-skill"
	managedSkillRevision(t, directory, name, managedDigestA, "old")
	for _, invalid := range []string{"", "Upper", "two--hyphens", "-leading", "trailing-", strings.Repeat("a", 65)} {
		if _, _, err := r.ManagedSkillTarget(context.Background(), invalid); err == nil {
			t.Fatalf("accepted invalid name %q", invalid)
		}
		if err := r.SwapManagedSkillTarget(context.Background(), invalid, managedDigestA); err == nil {
			t.Fatalf("swap accepted invalid name %q", invalid)
		}
	}
	for _, invalid := range []string{"", strings.Repeat("A", 64), strings.Repeat("a", 63), strings.Repeat("g", 64)} {
		if err := r.SwapManagedSkillTarget(context.Background(), name, invalid); err == nil {
			t.Fatalf("swap accepted invalid digest %q", invalid)
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.ManagedSkillTarget(context.Background(), "file"); err == nil {
		t.Fatal("regular file accepted by inspection")
	}
	if err := r.SwapManagedSkillTarget(context.Background(), "file", managedDigestA); err == nil {
		t.Fatal("regular file replaced")
	}
	socket, err := net.Listen("unix", filepath.Join(directory, "socket"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = socket.Close() })
	if _, _, err := r.ManagedSkillTarget(context.Background(), "socket"); err == nil {
		t.Fatal("special file accepted by inspection")
	}
	if err := r.SwapManagedSkillTarget(context.Background(), "socket", managedDigestA); err == nil {
		t.Fatal("special file replaced")
	}

	for label, target := range map[string]string{
		"absolute": "/tmp/elsewhere",
		"escape":   "../elsewhere",
		"foreign":  ".stella-revisions/other/" + managedDigestA,
		"cyclic":   "bad-link",
	} {
		t.Run(label, func(t *testing.T) {
			link := "bad-link"
			if err := os.Symlink(target, filepath.Join(directory, link)); err != nil {
				t.Fatal(err)
			}
			if _, _, err := r.ManagedSkillTarget(context.Background(), link); err == nil {
				t.Fatal("malformed link accepted")
			}
			if err := r.SwapManagedSkillTarget(context.Background(), link, managedDigestA); err == nil {
				t.Fatal("malformed link replaced")
			}
			if err := os.Remove(filepath.Join(directory, link)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestManagedSkillTargetRejectsInvalidRevisionComponents(t *testing.T) {
	for name, mutate := range map[string]func(string){
		"missing revision": func(directory string) {
			_ = os.RemoveAll(filepath.Join(directory, managedSkillRevisionsDir, "skill", managedDigestA))
		},
		"revision file": func(directory string) {
			_ = os.RemoveAll(filepath.Join(directory, managedSkillRevisionsDir, "skill", managedDigestA))
			_ = os.WriteFile(filepath.Join(directory, managedSkillRevisionsDir, "skill", managedDigestA), []byte("x"), 0o600)
		},
		"symlinked name component": func(directory string) {
			_ = os.RemoveAll(filepath.Join(directory, managedSkillRevisionsDir, "skill"))
			_ = os.Symlink("../elsewhere", filepath.Join(directory, managedSkillRevisionsDir, "skill"))
		},
		"symlinked revisions component": func(directory string) {
			_ = os.RemoveAll(filepath.Join(directory, managedSkillRevisionsDir))
			_ = os.Symlink("elsewhere", filepath.Join(directory, managedSkillRevisionsDir))
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, directory := managedSkillTestRoot(t)
			managedSkillRevision(t, directory, "skill", managedDigestA, "old")
			if err := os.Symlink(managedSkillRevisionPath("skill", managedDigestA), filepath.Join(directory, "skill")); err != nil {
				t.Fatal(err)
			}
			mutate(directory)
			if _, _, err := r.ManagedSkillTarget(context.Background(), "skill"); err == nil {
				t.Fatal("invalid revision accepted")
			}
			if err := r.SwapManagedSkillTarget(context.Background(), "skill", managedDigestA); err == nil {
				t.Fatal("invalid revision accepted for swap")
			}
		})
	}
}

func TestManagedSkillTargetConcurrentSwapsLeaveOneValidWinner(t *testing.T) {
	r, directory := managedSkillTestRoot(t)
	name := "skill"
	managedSkillRevision(t, directory, name, managedDigestA, "old")
	managedSkillRevision(t, directory, name, managedDigestB, "new")
	if err := r.SwapManagedSkillTarget(context.Background(), name, managedDigestA); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, digest := range []string{managedDigestA, managedDigestB} {
		go func(digest string) {
			<-start
			results <- r.SwapManagedSkillTarget(context.Background(), name, digest)
		}(digest)
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil && !sandbox.IsOutcomeUnknown(err) {
			t.Fatalf("concurrent swap failed before publication: %v", err)
		}
	}
	digest, managed, err := r.ManagedSkillTarget(context.Background(), name)
	if err != nil || !managed || (digest != managedDigestA && digest != managedDigestB) {
		t.Fatalf("winner = %q, %t, %v", digest, managed, err)
	}
	assertNoManagedSkillTemps(t, directory)
}

func TestSwapManagedSkillTargetSyncAndCancellationBoundaries(t *testing.T) {
	t.Run("before rename", func(t *testing.T) {
		r, directory := managedSkillTestRoot(t)
		managedSkillRevision(t, directory, "skill", managedDigestA, "old")
		managedSkillRevision(t, directory, "skill", managedDigestB, "new")
		if err := r.SwapManagedSkillTarget(context.Background(), "skill", managedDigestA); err != nil {
			t.Fatal(err)
		}
		r.syncRootDirectory = func(*os.File) error { return errors.New("sync failed") }
		if err := r.SwapManagedSkillTarget(context.Background(), "skill", managedDigestB); err == nil || sandbox.IsOutcomeUnknown(err) {
			t.Fatalf("pre-rename sync = %v", err)
		}
		assertManagedSkillTarget(t, r, "skill", managedDigestA)
		assertNoManagedSkillTemps(t, directory)
	})
	t.Run("after rename", func(t *testing.T) {
		r, directory := managedSkillTestRoot(t)
		managedSkillRevision(t, directory, "skill", managedDigestA, "old")
		managedSkillRevision(t, directory, "skill", managedDigestB, "new")
		if err := r.SwapManagedSkillTarget(context.Background(), "skill", managedDigestA); err != nil {
			t.Fatal(err)
		}
		calls := 0
		r.syncRootDirectory = func(*os.File) error {
			calls++
			if calls == 2 {
				return errors.New("sync failed")
			}
			return nil
		}
		err := r.SwapManagedSkillTarget(context.Background(), "skill", managedDigestB)
		if !sandbox.IsOutcomeUnknown(err) {
			t.Fatalf("post-rename sync = %v", err)
		}
		assertManagedSkillTarget(t, r, "skill", managedDigestB)
	})
	t.Run("canceled", func(t *testing.T) {
		r, directory := managedSkillTestRoot(t)
		managedSkillRevision(t, directory, "skill", managedDigestA, "old")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := r.SwapManagedSkillTarget(ctx, "skill", managedDigestA); !errors.Is(err, context.Canceled) {
			t.Fatalf("pre-rename cancellation = %v", err)
		}
		assertNoManagedSkillTemps(t, directory)
	})
	t.Run("canceled after rename", func(t *testing.T) {
		r, directory := managedSkillTestRoot(t)
		managedSkillRevision(t, directory, "skill", managedDigestA, "old")
		managedSkillRevision(t, directory, "skill", managedDigestB, "new")
		ctx, cancel := context.WithCancel(context.Background())
		r.afterManagedSkillRename = cancel
		err := r.SwapManagedSkillTarget(ctx, "skill", managedDigestB)
		if !sandbox.IsOutcomeUnknown(err) {
			t.Fatalf("post-rename cancellation = %v", err)
		}
		assertManagedSkillTarget(t, r, "skill", managedDigestB)
	})
}

func TestSwapManagedSkillTargetPostPublicationVerificationFailsClosed(t *testing.T) {
	for name, tamper := range map[string]func(*Root, string){
		"foreign temporary link": func(r *Root, temporary string) {
			if err := r.root.Remove(temporary); err != nil {
				t.Fatal(err)
			}
			if err := r.root.Symlink("foreign", temporary); err != nil {
				t.Fatal(err)
			}
		},
		"revision component": func(r *Root, _ string) {
			if err := r.root.RemoveAll(path.Join(managedSkillRevisionsDir, "skill")); err != nil {
				t.Fatal(err)
			}
			if err := r.root.Symlink("foreign", path.Join(managedSkillRevisionsDir, "skill")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, directory := managedSkillTestRoot(t)
			managedSkillRevision(t, directory, "skill", managedDigestA, "old")
			r.afterManagedSkillTemporaryLink = func(temporary string) { tamper(r, temporary) }
			err := r.SwapManagedSkillTarget(context.Background(), "skill", managedDigestA)
			if !sandbox.IsOutcomeUnknown(err) {
				t.Fatalf("tampered publication = %v", err)
			}
			if _, _, err := r.ManagedSkillTarget(context.Background(), "skill"); err == nil {
				t.Fatal("tampered publication remained trusted")
			}
			assertNoManagedSkillTemps(t, directory)
		})
	}
}

// This test pins the digest before opening two files; that is the catalog's
// cross-file consistency contract, distinct from direct-link atomicity below.
func TestManagedSkillTargetPinnedDescriptorReadersAndSwaps(t *testing.T) {
	r, directory := managedSkillTestRoot(t)
	name := "skill"
	managedSkillRevision(t, directory, name, managedDigestA, "old")
	managedSkillRevision(t, directory, name, managedDigestB, "new")
	if err := r.SwapManagedSkillTarget(context.Background(), name, managedDigestA); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	stop := make(chan struct{})
	errs := make(chan error, 16)
	var readers sync.WaitGroup
	for range 8 {
		readers.Go(func() {
			<-start
			for {
				select {
				case <-stop:
					return
				default:
				}
				digest, managed, err := r.ManagedSkillTarget(context.Background(), name)
				if err != nil {
					errs <- fmt.Errorf("inspect target: %w", err)
					return
				}
				if !managed {
					errs <- fmt.Errorf("inspect target = %q, unmanaged", digest)
					return
				}
				revision := filepath.Join(directory, managedSkillRevisionPath(name, digest))
				main, err := os.ReadFile(filepath.Join(revision, "SKILL.md"))
				if err != nil {
					errs <- err
					return
				}
				companion, err := os.ReadFile(filepath.Join(revision, "companion"))
				if err != nil {
					errs <- fmt.Errorf("read companion: %w", err)
					return
				}
				if string(main) != string(companion) {
					errs <- fmt.Errorf("mixed revision %q / %q", main, companion)
					return
				}
			}
		})
	}
	close(start)
	for i := range 100 {
		if err := r.SwapManagedSkillTarget(context.Background(), name, []string{managedDigestA, managedDigestB}[i%2]); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	readers.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	assertNoManagedSkillTemps(t, directory)
}

func TestManagedSkillTargetLinkPathReadersAndSwaps(t *testing.T) {
	r, directory := managedSkillTestRoot(t)
	name := "skill"
	managedSkillRevision(t, directory, name, managedDigestA, "old")
	managedSkillRevision(t, directory, name, managedDigestB, "new")
	if err := r.SwapManagedSkillTarget(context.Background(), name, managedDigestA); err != nil {
		t.Fatal(err)
	}
	start, stop := make(chan struct{}), make(chan struct{})
	errs := make(chan error, 8)
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			<-start
			for {
				select {
				case <-stop:
					return
				default:
				}
				file, err := r.root.Open(path.Join(name, "SKILL.md"))
				if err != nil {
					if errors.Is(err, fs.ErrNotExist) {
						errs <- fmt.Errorf("open through managed link disappeared: %w", err)
						return
					}
					if errors.Is(err, syscall.ENOTDIR) {
						// os.Root's containment walk may lose a concurrently renamed
						// parent. A later open sees one whole link.
						continue
					}
					errs <- fmt.Errorf("open through managed link: %w", err)
					return
				}
				content, readErr := io.ReadAll(file)
				closeErr := file.Close()
				if readErr != nil {
					errs <- fmt.Errorf("read through managed link: %w", readErr)
					return
				}
				if closeErr != nil {
					errs <- fmt.Errorf("close through managed link: %w", closeErr)
					return
				}
				if string(content) != "old" && string(content) != "new" {
					errs <- fmt.Errorf("unexpected managed link content %q", content)
					return
				}
			}
		})
	}
	close(start)
	for i := range 100 {
		if err := r.SwapManagedSkillTarget(context.Background(), name, []string{managedDigestA, managedDigestB}[i%2]); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	readers.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	assertNoManagedSkillTemps(t, directory)
}

func managedSkillTestRoot(t *testing.T) (*Root, string) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("managed Skill publication is unsupported on this platform")
	}
	directory, err := os.MkdirTemp("/tmp", "stella-fsops-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	r, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, directory
}

func managedSkillRevision(t *testing.T, root, name, digest, content string) {
	t.Helper()
	directory := filepath.Join(root, managedSkillRevisionPath(name, digest))
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"SKILL.md", "companion"} {
		if err := os.WriteFile(filepath.Join(directory, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func assertManagedSkillTarget(t *testing.T, r *Root, name, want string) {
	t.Helper()
	got, managed, err := r.ManagedSkillTarget(context.Background(), name)
	if err != nil || !managed || got != want {
		t.Fatalf("target = %q, %t, %v; want %q", got, managed, err, want)
	}
}

func assertNoManagedSkillTemps(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".stella-skill-target-") {
			t.Fatalf("temporary link remains: %s", entry.Name())
		}
	}
}
