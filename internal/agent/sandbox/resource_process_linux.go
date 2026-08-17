//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// cleanupHostProcessResource finds every process carrying the unforgeable-for-
// callers backend marker and kills its whole process group. Re-scanning proves
// absence and covers descendants that race with the first scan.
func cleanupHostProcessResource(ctx context.Context, resourceID string) error {
	marker := []byte(pkgsandbox.EnvResourceID + "=" + resourceID)
	processGroups := make(map[int]struct{})
	for {
		marked, err := markedProcessIDs(marker)
		if err != nil {
			return err
		}
		for _, pid := range marked {
			if group, groupErr := processGroupID(pid); groupErr == nil && group > 0 {
				processGroups[group] = struct{}{}
			}
		}
		pids, err := processIDsInGroups(processGroups)
		if err != nil {
			return err
		}
		if len(marked) == 0 && len(pids) == 0 {
			return nil
		}
		for _, pid := range pids {
			// A pidfd binds cleanup to this exact process identity, so PID or
			// process-group reuse cannot redirect SIGKILL to an unrelated process.
			pidfd, err := unix.PidfdOpen(pid, 0)
			if err != nil {
				if errors.Is(err, unix.ESRCH) {
					continue
				}
				return fmt.Errorf("open sandbox process identity for pid %d: %w", pid, err)
			}
			err = unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0)
			_ = unix.Close(pidfd)
			if err != nil && !errors.Is(err, unix.ESRCH) {
				return fmt.Errorf("kill sandbox process %d: %w", pid, err)
			}
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("prove sandbox process resource %q absent: %w", resourceID, ctx.Err())
		case <-timer.C:
		}
	}
}

func processIDsInGroups(groups map[int]struct{}) ([]int, error) {
	if len(groups) == 0 {
		return nil, nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		group, err := processGroupID(pid)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		if _, ok := groups[group]; ok {
			if state, stateErr := processState(pid); stateErr == nil && state == 'Z' {
				continue
			}
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func processGroupID(pid int) (int, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}
	end := bytes.LastIndexByte(data, ')')
	if end < 0 {
		return 0, fmt.Errorf("invalid /proc/%d/stat", pid)
	}
	fields := bytes.Fields(data[end+1:])
	if len(fields) < 3 {
		return 0, fmt.Errorf("invalid /proc/%d/stat fields", pid)
	}
	group, err := strconv.Atoi(string(fields[2]))
	if err != nil {
		return 0, fmt.Errorf("parse /proc/%d process group: %w", pid, err)
	}
	return group, nil
}

func markedProcessIDs(marker []byte) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("scan sandbox processes: %w", err)
	}
	var pids []int
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		environ, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "environ"))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if state, stateErr := processState(pid); stateErr == nil && state == 'Z' {
				// A zombie cannot execute or retain descendants. Its owner will reap
				// the bookkeeping entry; it is not a live sandbox resource.
				continue
			}
			if errors.Is(err, os.ErrPermission) {
				// Processes owned by another OS user cannot carry a marker inherited
				// from this executor. A same-UID process can; inability to inspect it
				// therefore makes absence unprovable and must fail closed.
				info, infoErr := entry.Info()
				if infoErr != nil {
					if errors.Is(infoErr, os.ErrNotExist) {
						continue
					}
					return nil, fmt.Errorf("inspect sandbox process %d owner: %w", pid, infoErr)
				}
				stat, ok := info.Sys().(*syscall.Stat_t)
				if !ok || stat.Uid == uint32(os.Geteuid()) {
					return nil, fmt.Errorf("cannot prove sandbox process %d marker absence: %w", pid, err)
				}
				continue
			}
			return nil, fmt.Errorf("read sandbox process %d environment: %w", pid, err)
		}
		for value := range bytes.SplitSeq(environ, []byte{0}) {
			if bytes.Equal(value, marker) {
				pids = append(pids, pid)
				break
			}
		}
	}
	return pids, nil
}

func processState(pid int) (byte, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}
	// comm is parenthesized and may contain spaces or ')', so locate its final
	// delimiter. The state byte follows it as " ) X ".
	end := bytes.LastIndexByte(data, ')')
	if end < 0 || end+2 >= len(data) {
		return 0, fmt.Errorf("invalid /proc/%d/stat", pid)
	}
	return data[end+2], nil
}
