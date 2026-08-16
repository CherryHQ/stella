//go:build unix

package storagequal

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func sameOwner(a, b os.FileInfo) bool {
	x, ok := a.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	y, ok := b.Sys().(*syscall.Stat_t)
	return ok && x.Uid == y.Uid && x.Gid == y.Gid
}

func sameNamespaceObject(a, b os.FileInfo) bool {
	x, ok := a.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	y, ok := b.Sys().(*syscall.Stat_t)
	// Independent FUSE mounts use different synthetic device numbers while the
	// backend-stable inode identifies the same namespace object.
	return ok && x.Ino == y.Ino && a.Mode() == b.Mode()
}

func lockTest(a, b string) error {
	f1, e := os.OpenFile(a, os.O_CREATE|os.O_RDWR, 0o600)
	if e != nil {
		return e
	}
	f2, e := os.OpenFile(b, os.O_RDWR, 0)
	if e != nil {
		_ = f1.Close()
		return e
	}
	if e = unix.Flock(int(f1.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		return errors.Join(e, f2.Close(), f1.Close())
	}
	peerErr := unix.Flock(int(f2.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if peerErr == nil {
		_ = unix.Flock(int(f2.Fd()), unix.LOCK_UN)
	}
	cleanupErr := errors.Join(unix.Flock(int(f1.Fd()), unix.LOCK_UN), f2.Close(), f1.Close())
	if peerErr == nil {
		return errors.Join(errors.New("peer acquired held advisory lock"), cleanupErr)
	}
	return cleanupErr
}

func syncFileDir(a, b string) error {
	f, e := os.OpenFile(filepathJoin(a, "durable"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if e != nil {
		return e
	}
	if _, e = f.Write([]byte("durable")); e == nil {
		e = f.Sync()
	}
	_ = f.Close()
	if e != nil {
		return e
	}
	d, e := os.Open(a)
	if e != nil {
		return e
	}
	e = d.Sync()
	_ = d.Close()
	if e != nil {
		return e
	}
	v, e := os.ReadFile(filepathJoin(b, "durable"))
	if e != nil || string(v) != "durable" {
		return errors.New("synced value unavailable")
	}
	return nil
}

func capacityBenchmark(path string, min int64) Benchmark {
	var s unix.Statfs_t
	e := unix.Statfs(path, &s)
	free := int64(s.Bavail) * s.Bsize
	return Benchmark{"free_capacity", "bytes", float64(free), float64(min), "greater_or_equal", e == nil && free >= min}
}
func filepathJoin(a, b string) string { return a + string(os.PathSeparator) + b }
