//go:build linux || darwin

package db

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestEphemeralJanitorRemovesOwnerlessMarkedRoot(t *testing.T) {
	parent := t.TempDir()
	root := newEphemeralCandidate(t, parent, false)

	runEphemeralJanitor(parent, "unused-pg_ctl", func(string, string) error {
		t.Fatal("pg_ctl must not run without postmaster.pid")
		return nil
	})

	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("ownerless root remains: %v", err)
	}
}

func TestEphemeralJanitorSkipsHeldOwnerLock(t *testing.T) {
	parent := t.TempDir()
	root := newEphemeralCandidate(t, parent, true)
	lock, err := openEphemeralFile(filepath.Join(root, ephemeralLockName), unix.O_RDWR)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()

	runEphemeralJanitor(parent, "unused-pg_ctl", func(string, string) error {
		t.Fatal("pg_ctl must not run for a held root")
		return nil
	})

	if _, err := os.Lstat(root); err != nil {
		t.Fatalf("held root was removed: %v", err)
	}
}

func TestEphemeralJanitorSkipsUntrustedCandidates(t *testing.T) {
	parent := t.TempDir()
	tests := []struct {
		name  string
		setup func(t *testing.T, root string)
	}{
		{
			name: "malformed marker",
			setup: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, ephemeralMarkerName), "not json")
			},
		},
		{
			name: "mismatched data dir",
			setup: func(t *testing.T, root string) {
				writeMarker(t, root, filepath.Join(root, "other"))
			},
		},
		{
			name: "symlink marker",
			setup: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, ephemeralMarkerName)); err != nil {
					t.Fatal(err)
				}
				external := filepath.Join(parent, "outside-marker")
				writeFile(t, external, "{}")
				if err := os.Symlink(external, filepath.Join(root, ephemeralMarkerName)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink lock",
			setup: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, ephemeralLockName)); err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(parent, "outside-lock"), "")
				if err := os.Symlink(filepath.Join(parent, "outside-lock"), filepath.Join(root, ephemeralLockName)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "malformed pid file",
			setup: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "data", "postmaster.pid"), "not a pid file")
			},
		},
		{
			name: "mismatched pid data dir",
			setup: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "data", "postmaster.pid"), "12345\n/other/data\n")
			},
		},
		{
			name: "symlink data dir",
			setup: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, "data")); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(parent, "outside-data")
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(root, "data")); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newEphemeralCandidate(t, parent, true)
			tt.setup(t, root)
			runEphemeralJanitor(parent, "unused-pg_ctl", func(string, string) error { return nil })
			if _, err := os.Lstat(root); err != nil {
				t.Fatalf("untrusted root was removed: %v", err)
			}
		})
	}

	t.Run("symlink root", func(t *testing.T) {
		target := newEphemeralCandidate(t, parent, true)
		link := filepath.Join(parent, ephemeralRootPrefix+"symlink")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		runEphemeralJanitor(parent, "unused-pg_ctl", func(string, string) error { return nil })
		if _, err := os.Lstat(link); err != nil {
			t.Fatalf("symlink root was removed: %v", err)
		}
	})
}

func TestEphemeralJanitorSkipsForeignRootWhenTestable(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing a file owner requires root")
	}
	parent := t.TempDir()
	root := newEphemeralCandidate(t, parent, false)
	if err := os.Chown(root, 1, -1); err != nil {
		t.Fatal(err)
	}

	runEphemeralJanitor(parent, "unused-pg_ctl", func(string, string) error { return nil })
	if _, err := os.Lstat(root); err != nil {
		t.Fatalf("foreign-owned root was removed: %v", err)
	}
}

func TestEphemeralJanitorArbitratesConcurrentCleanup(t *testing.T) {
	parent := t.TempDir()
	root := newEphemeralCandidate(t, parent, true)
	writeFile(t, filepath.Join(root, "data", "postmaster.pid"), "12345\n"+filepath.Join(root, "data")+"\n")
	entered := filepath.Join(parent, "entered")
	release := filepath.Join(parent, "release")
	stopped := filepath.Join(parent, "stopped")

	cmd := exec.Command(os.Args[0], "-test.run=^TestEphemeralJanitorChild$")
	cmd.Env = append(os.Environ(),
		"GO_WANT_EPHEMERAL_JANITOR=1",
		"EPHEMERAL_JANITOR_PARENT="+parent,
		"EPHEMERAL_JANITOR_ENTERED="+entered,
		"EPHEMERAL_JANITOR_RELEASE="+release,
		"EPHEMERAL_JANITOR_STOPPED="+stopped,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.WriteFile(release, nil, 0o600)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	waitForFile(t, entered)

	var parentStops atomic.Int32
	runEphemeralJanitor(parent, "unused-pg_ctl", func(string, string) error {
		parentStops.Add(1)
		return nil
	})
	if got := parentStops.Load(); got != 0 {
		t.Fatalf("second janitor reached pg_ctl %d times", got)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stopped); err != nil {
		t.Fatalf("first janitor did not stop PostgreSQL: %v", err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("cleaned root remains: %v", err)
	}
}

func TestEphemeralJanitorChild(t *testing.T) {
	if os.Getenv("GO_WANT_EPHEMERAL_JANITOR") != "1" {
		return
	}
	parent := os.Getenv("EPHEMERAL_JANITOR_PARENT")
	runEphemeralJanitor(parent, "unused-pg_ctl", func(string, string) error {
		if err := os.WriteFile(os.Getenv("EPHEMERAL_JANITOR_ENTERED"), nil, 0o600); err != nil {
			return err
		}
		for {
			if _, err := os.Stat(os.Getenv("EPHEMERAL_JANITOR_RELEASE")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		return os.WriteFile(os.Getenv("EPHEMERAL_JANITOR_STOPPED"), nil, 0o600)
	})
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func TestEphemeralJanitorLeavesRootWhenStopFails(t *testing.T) {
	parent := t.TempDir()
	root := newEphemeralCandidate(t, parent, true)
	writeFile(t, filepath.Join(root, "data", "postmaster.pid"), "12345\n"+filepath.Join(root, "data")+"\n")

	runEphemeralJanitor(parent, "unused-pg_ctl", func(string, string) error { return errors.New("stop failed") })
	if _, err := os.Lstat(root); err != nil {
		t.Fatalf("root removed after failed stop: %v", err)
	}
}

func TestEphemeralStartFailureRetainsMarkedRootAndReleasesLock(t *testing.T) {
	parent := t.TempDir()
	t.Setenv("TMPDIR", parent)
	runtimeRoot := fakeRuntime(t)
	t.Setenv(postgresRuntimeEnvName, runtimeRoot)

	_, err := StartEmbedded("", 0)
	if err == nil {
		t.Fatal("StartEmbedded succeeded with no initdb")
	}
	roots, err := filepath.Glob(filepath.Join(parent, ephemeralRootPrefix+"*"))
	if err != nil || len(roots) != 1 {
		t.Fatalf("ephemeral roots = %v, %v; want one", roots, err)
	}
	root := roots[0]
	if _, err := os.Stat(filepath.Join(root, ephemeralMarkerName)); err != nil {
		t.Fatalf("marker missing after start failure: %v", err)
	}
	lock, err := openEphemeralFile(filepath.Join(root, ephemeralLockName), unix.O_RDWR)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("owner lock remains held after start failure: %v", err)
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()
}

func TestEphemeralRuntimeResolutionFailureRemovesRoot(t *testing.T) {
	parent := t.TempDir()
	t.Setenv("TMPDIR", parent)
	t.Setenv(postgresRuntimeEnvName, filepath.Join(parent, "missing-runtime"))

	if _, err := StartEmbedded("", 0); err == nil {
		t.Fatal("StartEmbedded succeeded with a missing runtime")
	}
	roots, err := filepath.Glob(filepath.Join(parent, ephemeralRootPrefix+"*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 0 {
		t.Fatalf("runtime resolution failure left ephemeral roots: %v", roots)
	}
}

func TestStableStartDoesNotCreateEphemeralOwnership(t *testing.T) {
	runtimeRoot := fakeRuntime(t)
	t.Setenv(postgresRuntimeEnvName, runtimeRoot)
	dataDir := filepath.Join(t.TempDir(), "data")

	_, err := StartEmbedded(dataDir, 0)
	if err == nil {
		t.Fatal("StartEmbedded succeeded with no initdb")
	}
	for _, name := range []string{ephemeralLockName, ephemeralMarkerName} {
		if _, err := os.Lstat(filepath.Join(dataDir, name)); !os.IsNotExist(err) {
			t.Fatalf("stable data dir has %s: %v", name, err)
		}
	}
}

type fakeEmbeddedServer struct{ err error }

func (s fakeEmbeddedServer) Stop() error { return s.err }

func TestEmbeddedStopRetainsRootOnFailureAndRemovesAfterSuccess(t *testing.T) {
	owner, err := createEphemeralOwner()
	if err != nil {
		t.Fatal(err)
	}
	root := owner.root
	if err := (&Embedded{pg: fakeEmbeddedServer{err: errors.New("stop failed")}, ephemeral: owner}).Stop(); err == nil {
		t.Fatal("Stop succeeded, want failure")
	}
	if _, err := os.Lstat(root); err != nil {
		t.Fatalf("root removed after failed Stop: %v", err)
	}

	lock, err := openEphemeralFile(filepath.Join(root, ephemeralLockName), unix.O_RDWR)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	owner = newEphemeralOwner(root, lock)
	if err := (&Embedded{pg: fakeEmbeddedServer{}, ephemeral: owner}).Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("root remains after successful Stop: %v", err)
	}
}

func newEphemeralCandidate(t *testing.T, parent string, dataDir bool) string {
	t.Helper()
	root, err := os.MkdirTemp(parent, ephemeralRootPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := openEphemeralFile(filepath.Join(root, ephemeralLockName), unix.O_RDWR|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if dataDir {
		if err := os.Mkdir(filepath.Join(root, "data"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeMarker(t, root, filepath.Join(root, "data"))
	return root
}

func writeMarker(t *testing.T, root, dataDir string) {
	t.Helper()
	writeFile(t, filepath.Join(root, ephemeralMarkerName), "{\"schema\":\""+ephemeralSchema+"\",\"version\":1,\"data_dir\":\""+dataDir+"\"}\n")
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fakeRuntime(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	pgCtl := filepath.Join(bin, pgCtlName())
	if err := os.WriteFile(pgCtl, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestEphemeralOwnerLockIsCloseOnExec(t *testing.T) {
	owner, err := createEphemeralOwner()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		owner.release()
		_ = os.RemoveAll(owner.root)
	}()

	lock, err := openEphemeralFile(filepath.Join(owner.root, ephemeralLockName), unix.O_RDWR)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	flags, err := unix.FcntlInt(lock.Fd(), unix.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}
	if flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("owner lock is not close-on-exec")
	}
}

func TestPostmasterDataDirMatchesExactly(t *testing.T) {
	if postmasterDataDirMatches([]byte("123\n/tmp/data/other\n"), "/tmp/data") {
		t.Fatal("accepted non-exact data dir")
	}
	if !postmasterDataDirMatches([]byte("123\n/tmp/data\n"), "/tmp/data") {
		t.Fatal("rejected exact data dir")
	}
	if postmasterDataDirMatches([]byte(strings.Repeat("x", 10)), "/tmp/data") {
		t.Fatal("accepted malformed pid file")
	}
}
