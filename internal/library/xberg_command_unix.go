//go:build !windows

package library

import (
	"context"
	"io"
	"strconv"
	"time"

	"github.com/CherryHQ/stella/internal/manifestplugins"
)

const xbergLimitShell = `ulimit -v "$1" && ulimit -t "$2" && shift 2 && exec "$@"`

func runXbergCommand(
	ctx context.Context,
	binary string,
	args []string,
	dir string,
	env []string,
	stdout, stderr io.Writer,
	maxMemoryBytes int64,
	maxCPUTime time.Duration,
) error {
	// POSIX shells apply these hard resource limits before replacing themselves
	// with Xberg. Descendants inherit both limits and the managed process group.
	memoryKiB := maxMemoryBytes / 1024
	if maxMemoryBytes%1024 != 0 {
		memoryKiB++
	}
	cpuSeconds := int64(maxCPUTime / time.Second)
	if maxCPUTime%time.Second != 0 {
		cpuSeconds++
	}
	commandArgs := []string{
		"-c",
		xbergLimitShell,
		"stella-xberg",
		strconv.FormatInt(memoryKiB, 10),
		strconv.FormatInt(cpuSeconds, 10),
		binary,
	}
	commandArgs = append(commandArgs, args...)
	cmd := manifestplugins.ManagedCommandContext(ctx, "/bin/sh", commandArgs...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
