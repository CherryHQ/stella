package delegate

import (
	"context"
	"os"
)

type hostPathInfo struct {
	Exists bool
	IsDir  bool
}

// statHostPath stats a filesystem path using os.Stat.
func statHostPath(_ context.Context, path string) (hostPathInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return hostPathInfo{Exists: false}, nil
		}
		return hostPathInfo{}, err
	}
	return hostPathInfo{Exists: true, IsDir: info.IsDir()}, nil
}

type dirEntry struct {
	Name  string
	IsDir bool
	Size  int64
}

// readHostDir reads directory entries using os.ReadDir.
func readHostDir(_ context.Context, path string) ([]dirEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := make([]dirEntry, 0, len(entries))
	for _, e := range entries {
		item := dirEntry{Name: e.Name(), IsDir: e.IsDir()}
		if info, err := e.Info(); err == nil {
			item.Size = info.Size()
		}
		out = append(out, item)
	}
	return out, nil
}

// readHostFile reads a file using os.ReadFile.
func readHostFile(_ context.Context, path string) ([]byte, error) {
	return os.ReadFile(path)
}
