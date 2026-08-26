// Package xberg owns how Stella invokes the bundled Xberg CLI.
//
// Xberg parses untrusted input — documents a user uploaded, images a channel
// delivered — so every call must cross the process boundary the same way:
// a scrubbed environment, no configuration discovery, a bounded runtime, and
// bounded output. Library hardened its own call site and Vision did not, which
// meant the same binary parsed the same class of untrusted bytes under two
// different threat models. This package exists so that cannot recur: callers
// choose flags and limits, never whether to be safe.
package xberg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// ErrOutputLimit reports that Xberg produced more output than the caller allowed.
var ErrOutputLimit = errors.New("xberg output limit exceeded")

// NoConfigDiscovery disables Xberg's search for xberg.toml in the working
// directory and its parents. Always pass it: the working directory of a Stella
// process is not a trust boundary, and for staged input it can be a world-
// writable temp root where any local user could plant configuration.
const NoConfigDiscovery = "--no-config-discovery"

// Limits bounds what a single invocation may return. A zero field means the
// corresponding stream is capped at its default rather than left unbounded.
type Limits struct {
	Stdout int
	Stderr int
}

const (
	defaultStdoutLimit = 48 << 20
	defaultStderrLimit = 64 << 10
)

func (l Limits) resolve() Limits {
	if l.Stdout <= 0 {
		l.Stdout = defaultStdoutLimit
	}
	if l.Stderr <= 0 {
		l.Stderr = defaultStderrLimit
	}
	return l
}

// Command builds an exec.Cmd for the Xberg CLI with the process boundary already
// closed. Callers may adjust Stdout/Stderr but should not widen Env or Dir.
func Command(ctx context.Context, binary string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binary, args...)
	// Anchor the working directory to something Stella owns. Xberg resolves its
	// adjacent shared libraries through @loader_path/$ORIGIN, not the working
	// directory, so this is about configuration discovery, not linking.
	cmd.Dir = filepath.Dir(binary)
	cmd.Env = childEnvironment()
	return cmd
}

// Run executes Xberg and returns its bounded stdout and stderr. Exceeding a
// limit is an error rather than a silent truncation, so a caller can never treat
// a partial document as a complete one.
func Run(ctx context.Context, binary string, args []string, limits Limits) (stdout, stderr []byte, err error) {
	limits = limits.resolve()
	out := &cappedBuffer{max: limits.Stdout}
	errBuf := &cappedBuffer{max: limits.Stderr}
	cmd := Command(ctx, binary, args...)
	cmd.Stdout = out
	cmd.Stderr = errBuf
	runErr := cmd.Run()
	if out.exceeded {
		return nil, errBuf.Bytes(), fmt.Errorf("%w: stdout exceeds %d bytes", ErrOutputLimit, limits.Stdout)
	}
	if errBuf.exceeded {
		return nil, errBuf.Bytes(), fmt.Errorf("%w: stderr exceeds %d bytes", ErrOutputLimit, limits.Stderr)
	}
	if runErr != nil {
		return nil, errBuf.Bytes(), runErr
	}
	return out.Bytes(), errBuf.Bytes(), nil
}

// allowedEnvironment is the complete set of variables Xberg may observe. It is a
// whitelist because the daemon's environment carries provider credentials, and a
// blacklist would silently pass anything added later.
var allowedEnvironment = []string{
	"PATH", "LD_LIBRARY_PATH", "DYLD_LIBRARY_PATH",
	"TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL",
}

func childEnvironment() []string {
	environment := make([]string, 0, len(allowedEnvironment)+2)
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if slices.Contains(allowedEnvironment, key) {
			environment = append(environment, entry)
		}
	}
	environment = append(environment, "NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost")
	slices.Sort(environment)
	return environment
}

// cappedBuffer must NOT embed bytes.Buffer. Embedding promotes Buffer.ReadFrom,
// and io.Copy — which os/exec uses to drain the child's pipe — prefers ReadFrom
// over Write. The cap was silently bypassed that way: the limit check lived in a
// Write method nothing called.
type cappedBuffer struct {
	buf      bytes.Buffer
	max      int
	exceeded bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.max {
		b.exceeded = true
		if allowed := b.max - b.buf.Len(); allowed > 0 {
			_, _ = b.buf.Write(p[:allowed])
		}
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *cappedBuffer) Bytes() []byte { return b.buf.Bytes() }
