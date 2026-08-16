//go:build !unix

package storagequal

import (
	"errors"
	"os"
)

func sameOwner(a, b os.FileInfo) bool           { return false }
func sameNamespaceObject(a, b os.FileInfo) bool { return false }
func lockTest(a, b string) error                { return errors.New("POSIX locking unsupported on this platform") }
func syncFileDir(a, b string) error {
	return errors.New("POSIX directory fsync unsupported on this platform")
}
func capacityBenchmark(path string, min int64) Benchmark {
	return Benchmark{"free_capacity", "bytes", 0, float64(min), "greater_or_equal", false}
}
