package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// ManagedSkillTreeEntry is one complete regular file in an immutable managed
// Skill revision. Path is a canonical relative POSIX path and Open must yield
// exactly Length bytes. DigestManagedSkillTreeV1 owns and closes each returned stream.
type ManagedSkillTreeEntry struct {
	Path   string
	Mode   fs.FileMode
	Length int64
	Open   func() (io.ReadCloser, error)
}

// ValidateManagedSkillTreePath validates the provider-neutral tree coordinate.
// Namespace policy and metadata encoding intentionally belong to skills.
func ValidateManagedSkillTreePath(p string) error {
	if p == "" || strings.ContainsRune(p, '\x00') || strings.Contains(p, "\\") || path.IsAbs(p) || path.Clean(p) != p || p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return fmt.Errorf("invalid managed skill tree path %q", p)
	}
	return nil
}

// DigestManagedSkillTreeV1 calculates the canonical v1 digest. It never buffers
// file bodies and rejects duplicate paths, non-regular modes, and short/long
// streams. The caller supplies every file, including .stella-skill.json.
func DigestManagedSkillTreeV1(entries []ManagedSkillTreeEntry) (string, error) {
	ordered := append([]ManagedSkillTreeEntry(nil), entries...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	h := sha256.New()
	_, _ = h.Write([]byte("stella.skill.tree.digest.v1\x00"))
	for i, entry := range ordered {
		if err := ValidateManagedSkillTreePath(entry.Path); err != nil {
			return "", err
		}
		if entry.Mode&fs.ModeType != 0 {
			return "", fmt.Errorf("managed skill tree entry %q is not a regular file", entry.Path)
		}
		if entry.Length < 0 || entry.Open == nil {
			return "", fmt.Errorf("invalid managed skill tree entry %q", entry.Path)
		}
		if i > 0 && ordered[i-1].Path == entry.Path {
			return "", fmt.Errorf("duplicate managed skill tree path %q", entry.Path)
		}
		writeTreeField(h, []byte(entry.Path))
		writeTreeUint(h, uint64(entry.Mode.Perm()))
		writeTreeUint(h, uint64(entry.Length))
		r, err := entry.Open()
		if err != nil {
			return "", fmt.Errorf("open managed skill tree entry %q: %w", entry.Path, err)
		}
		n, copyErr := io.Copy(h, io.LimitReader(r, entry.Length+1))
		closeErr := r.Close()
		if copyErr != nil {
			return "", fmt.Errorf("read managed skill tree entry %q: %w", entry.Path, copyErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("close managed skill tree entry %q: %w", entry.Path, closeErr)
		}
		if n != entry.Length {
			return "", fmt.Errorf("managed skill tree entry %q has length %d, want %d", entry.Path, n, entry.Length)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeTreeField(dst io.Writer, value []byte) {
	writeTreeUint(dst, uint64(len(value)))
	_, _ = dst.Write(value)
}

func writeTreeUint(dst io.Writer, value uint64) {
	var b [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(b[:], value)
	_, _ = dst.Write(b[:n])
}

// ManagedSkillPublication is the narrow optional mutation capability. Files
// are streamed in manifest order; BodyLength is their exact combined length.
type ManagedSkillPublication struct{ Files []ManagedSkillTreeEntry }

// ManagedSkillPublisher publishes one complete immutable revision and selects
// it as the catalog entry. catalogRoot and name are canonical provider paths,
// never host coordinates.
type ManagedSkillPublisher interface {
	PublishManagedSkill(context.Context, string, string, string, ManagedSkillPublication) error
}
