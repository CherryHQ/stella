//go:build windows

package db

// Windows keeps the ephemeral root and marker lifecycle but does not attempt
// Unix flock-based orphan recovery. The release builds retain their existing
// behavior; the janitor is intentionally limited to platforms with flock.
func lockNewEphemeralRoot(root string) (*ephemeralOwner, error) {
	return &ephemeralOwner{root: root, releaseLock: func() {}}, nil
}

func runEphemeralJanitor(tempDir, pgCtl string, stop func(string, string) error) {}

func stopEphemeralPostgres(pgCtl, dataDir string) error { return nil }
