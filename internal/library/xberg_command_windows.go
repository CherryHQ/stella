//go:build windows

package library

import (
	"context"
	"fmt"
	"io"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/CherryHQ/stella/internal/manifestplugins"
)

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
	job, err := newXbergJob(maxMemoryBytes, maxCPUTime)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(job)

	cmd := manifestplugins.ManagedCommandContext(ctx, binary, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := assignXbergProcessToJob(job, uint32(cmd.Process.Pid)); err != nil {
		// Fail closed: an unbounded parser must not remain alive after assignment
		// fails. Managed cancellation terminates its whole descendant tree.
		_ = cmd.Cancel()
		_ = cmd.Wait()
		return err
	}
	return cmd.Wait()
}

func newXbergJob(maxMemoryBytes int64, maxCPUTime time.Duration) (windows.Handle, error) {
	if maxMemoryBytes <= 0 || uint64(maxMemoryBytes) > uint64(^uintptr(0)) {
		return 0, fmt.Errorf("invalid Xberg process memory limit %d", maxMemoryBytes)
	}
	if maxCPUTime <= 0 {
		return 0, fmt.Errorf("invalid Xberg process CPU limit %s", maxCPUTime)
	}

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("create Xberg job object: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE |
		windows.JOB_OBJECT_LIMIT_JOB_MEMORY |
		windows.JOB_OBJECT_LIMIT_JOB_TIME
	limits.BasicLimitInformation.PerJobUserTimeLimit = maxCPUTime.Nanoseconds() / 100
	limits.JobMemoryLimit = uintptr(maxMemoryBytes)
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		windows.CloseHandle(job)
		return 0, fmt.Errorf("set Xberg job limits: %w", err)
	}
	return job, nil
}

func assignXbergProcessToJob(job windows.Handle, pid uint32) error {
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		pid,
	)
	if err != nil {
		return fmt.Errorf("open Xberg process for job assignment: %w", err)
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		return fmt.Errorf("assign Xberg process to constrained job: %w", err)
	}
	return nil
}
