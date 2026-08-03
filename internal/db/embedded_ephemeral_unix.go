//go:build linux || darwin

package db

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func lockNewEphemeralRoot(root string) (*ephemeralOwner, error) {
	lock, err := openEphemeralFile(filepath.Join(root, ephemeralLockName), unix.O_RDWR|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return nil, err
	}
	if !isOwnedRegularFile(lock) {
		_ = lock.Close()
		return nil, os.ErrPermission
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return newEphemeralOwner(root, lock), nil
}

func newEphemeralOwner(root string, lock *os.File) *ephemeralOwner {
	return &ephemeralOwner{
		root: root,
		releaseLock: func() {
			_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
			_ = lock.Close()
		},
	}
}

func openEphemeralFile(path string, flags int) (*os.File, error) {
	fd, err := unix.Open(path, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func runEphemeralJanitor(tempDir, pgCtl string, stop func(string, string) error) {
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ephemeralRootPrefix) {
			continue
		}
		runEphemeralJanitorCandidate(filepath.Join(tempDir, entry.Name()), pgCtl, stop)
	}
}

func runEphemeralJanitorCandidate(root, pgCtl string, stop func(string, string) error) {
	if !isOwnedDirectory(root) {
		return
	}

	lock, err := openEphemeralFile(filepath.Join(root, ephemeralLockName), unix.O_RDWR)
	if err != nil {
		return
	}
	defer func() { _ = lock.Close() }()
	if !isOwnedRegularFile(lock) {
		return
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()

	dataDir, ok := validEphemeralMarker(root)
	if !ok || !isOwnedDirectoryOrAbsent(dataDir) {
		return
	}

	pidFile := filepath.Join(dataDir, "postmaster.pid")
	pid, err := readOwnedRegularFile(pidFile)
	if errors.Is(err, os.ErrNotExist) {
		// The cluster never reached PostgreSQL startup, so no server can need
		// stopping. This includes a marked root with no data directory yet.
		_ = os.RemoveAll(root)
		return
	}
	if err != nil || !isOwnedDirectory(dataDir) || !postmasterDataDirMatches(pid, dataDir) {
		return
	}
	if stop(pgCtl, dataDir) != nil {
		return
	}
	_ = os.RemoveAll(root)
}

func validEphemeralMarker(root string) (string, bool) {
	dataDir := filepath.Join(root, "data")
	contents, err := readOwnedRegularFile(filepath.Join(root, ephemeralMarkerName))
	if err != nil {
		return "", false
	}
	var marker ephemeralMarker
	if json.Unmarshal(contents, &marker) != nil ||
		marker.Schema != ephemeralSchema ||
		marker.Version != ephemeralVersion ||
		marker.DataDir != dataDir {
		return "", false
	}
	return dataDir, true
}

func postmasterDataDirMatches(contents []byte, dataDir string) bool {
	lines := strings.Split(string(contents), "\n")
	return len(lines) >= 2 && strings.TrimSpace(lines[1]) == dataDir
}

func stopEphemeralPostgres(pgCtl, dataDir string) error {
	return exec.Command(pgCtl, "stop", "-w", "-D", dataDir).Run()
}

func isOwnedDirectory(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsDir() && isCurrentUser(info)
}

func isOwnedDirectoryOrAbsent(path string) bool {
	info, err := os.Lstat(path)
	return errors.Is(err, os.ErrNotExist) || (err == nil && info.Mode().IsDir() && isCurrentUser(info))
}

func readOwnedRegularFile(path string) ([]byte, error) {
	f, err := openEphemeralFile(path, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if !isOwnedRegularFile(f) {
		return nil, os.ErrPermission
	}
	return io.ReadAll(f)
}

func isOwnedRegularFile(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode().IsRegular() && isCurrentUser(info)
}

func isCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid())
}
