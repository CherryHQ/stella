package sandbox

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"time"
)

// Canonical paths are the only filesystem coordinates shared with callers.
const (
	PathWorkspace = MountWorkspace
	PathUser      = MountUserData
	PathTemp      = "/tmp"
)

var (
	ErrReadLimit      = errors.New("sandbox: read exceeds limit")
	ErrOutcomeUnknown = errors.New("sandbox: operation outcome unknown")
)

// IsOutcomeUnknown reports an interrupted mutation whose result cannot safely
// be inferred. Callers must surface it rather than retrying the operation.
func IsOutcomeUnknown(err error) bool { return errors.Is(err, ErrOutcomeUnknown) }

type FileInfo struct {
	Name    string
	Size    int64
	Mode    fs.FileMode
	ModTime time.Time
	IsDir   bool
}

type ReadOptions struct {
	// MaxBytes is required and bounds bytes returned from the stream.
	MaxBytes int64
}

type WriteOptions struct {
	// Perm applies only when creating a file. Existing file permissions survive.
	Perm fs.FileMode
	// ContentLength is required by one-shot remote writers. Nil means unknown;
	// direct implementations may stream it until EOF.
	ContentLength *int64
}

// ManagedSkillTarget is the opaque result of inspecting one canonical Skill
// entry. It deliberately exposes neither a symlink target nor provider path.
type ManagedSkillTarget struct {
	Digest  string
	Managed bool
}

// ManagedSkillTargetInspector is an optional, narrowly scoped capability for
// the managed Skill revision layout. path must name the direct canonical Skill
// entry; ordinary directories and absent entries are unmanaged.
type ManagedSkillTargetInspector interface {
	InspectManagedSkillTarget(context.Context, string) (ManagedSkillTarget, error)
}

// Filesystem is the provider-neutral filesystem operation boundary. Paths are
// canonical sandbox paths; implementations never return a host coordinate.
type Filesystem interface {
	io.Closer
	Read(context.Context, string, ReadOptions) (io.ReadCloser, FileInfo, error)
	Write(context.Context, string, io.Reader, WriteOptions) error
	Upload(context.Context, string, io.Reader, WriteOptions) error
	Stat(context.Context, string) (FileInfo, error)
	List(context.Context, string) ([]DirEntry, error)
	Mkdir(context.Context, string, fs.FileMode) error
	Remove(context.Context, string, bool) error
	// Rename atomically moves a path only when the destination does not exist.
	// Existing destinations return an error matching fs.ErrExist.
	Rename(context.Context, string, string) error
}

// FilesystemSession is implemented by providers that expose the mediated file
// boundary. It is separate from Session while Phase 2.1 coexists with legacy
// ResolvePath callers; subsequent PRs migrate consumers before removing them.
type FilesystemSession interface {
	Session
	Filesystem() (Filesystem, error)
}
