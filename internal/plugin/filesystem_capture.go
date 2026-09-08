package plugin

import (
	"archive/zip"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/CherryHQ/stella/internal/platform/home"
)

// Capture limits match project Skill snapshots. Large CLI artifacts belong in
// the installer cache, never inside the authored resource tree.
const (
	resourceMaxFiles     = 512
	resourceMaxFileBytes = 1 << 20
	resourceMaxBytes     = 16 << 20
	resourceMaxEntries   = 4096
	resourceMaxDepth     = 64
)

var ErrResourceLimit = errors.New("plugin: resource capture limit exceeded")

// ResourceContent contains only captured bytes; opening a file cannot consult
// the mutable source or retain an authorized Home capability.
type ResourceContent struct {
	fs           *zip.Reader
	Digest       string
	bytes, files int
}

func (c *ResourceContent) FS() fs.FS { return c.fs }

func captureResource(ctx context.Context, root home.RootOperations, base string) (*ResourceContent, error) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	count, entries, total := 0, 0, 0
	var walk func(string, int) error
	walk = func(dir string, depth int) error {
		if depth > resourceMaxDepth {
			return ErrResourceLimit
		}
		children, err := root.List(ctx, dir, home.ListOptions{Limit: 1024})
		if err != nil {
			return err
		}
		slices.SortFunc(children, func(a, b fs.DirEntry) int { return cmp.Compare(a.Name(), b.Name()) })
		for _, child := range children {
			entries++
			if entries > resourceMaxEntries {
				return ErrResourceLimit
			}
			name := child.Name()
			if name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
				return fs.ErrPermission
			}
			relative := path.Join(dir, name)
			info, err := child.Info()
			if err != nil {
				return err
			}
			if info.IsDir() {
				if err := walk(relative, depth+1); err != nil {
					return err
				}
				continue
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("plugin: unsupported resource file %q", relative)
			}
			count++
			if count > resourceMaxFiles {
				return ErrResourceLimit
			}
			var data bytes.Buffer
			if err := root.Read(ctx, relative, &data, home.ReadOptions{MaxBytes: resourceMaxFileBytes}); err != nil {
				return err
			}
			total += data.Len()
			if total > resourceMaxBytes {
				return ErrResourceLimit
			}
			capturedName := strings.TrimPrefix(relative, base+"/")
			header := &zip.FileHeader{Name: capturedName, Method: zip.Store}
			header.SetMode(info.Mode().Perm() & 0o555)
			file, err := writer.CreateHeader(header)
			if err != nil {
				return err
			}
			if _, err := file.Write(data.Bytes()); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(base, 0); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	data := archive.Bytes()
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	return &ResourceContent{fs: reader, Digest: "sha256:" + hex.EncodeToString(digest[:]), bytes: total, files: count}, nil
}
