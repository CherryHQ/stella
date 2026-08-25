//go:build linux

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"golang.org/x/sys/unix"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// cleanupHostProcessResource only follows durably registered kernel process
// identities. Environment markers are deliberately not used for discovery.
func cleanupHostProcessResource(ctx context.Context, _ string) error {
	for _, identity := range pkgsandbox.ProcessIdentities(ctx) {
		if err := killRegisteredTree(ctx, identity); err != nil {
			return err
		}
	}
	return nil
}

func killRegisteredTree(ctx context.Context, identity pkgsandbox.ProcessIdentity) error {
	current, err := pkgsandbox.LinuxProcessIdentity(identity.PID)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect registered sandbox process %d: %w", identity.PID, err)
	}
	if current.StartTime != identity.StartTime {
		// The registered process is already absent; this PID belongs to somebody
		// else and must never be signalled.
		return nil
	}
	// Stop the registered root before taking the final descendant snapshot.
	// Descendants are followed by PPID, not process group, so a target cannot
	// evade recovery with setsid(2). Repeating after each stop closes the fork
	// race: once every discovered process is stopped, the tree cannot grow.
	identities := map[int]pkgsandbox.ProcessIdentity{identity.PID: identity}
	pidfds := make(map[int]int)
	defer func() {
		for _, pidfd := range pidfds {
			_ = unix.Close(pidfd)
		}
	}()
	for {
		tree, err := descendantProcessIdentities(identity.PID)
		if err != nil {
			return err
		}
		added := false
		for pid, candidate := range tree {
			if _, exists := identities[pid]; exists {
				continue
			}
			identities[pid] = candidate
			added = true
		}
		openedAny := false
		for pid, candidate := range identities {
			if _, opened := pidfds[pid]; opened {
				continue
			}
			pidfd, err := openExactProcess(candidate)
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ESRCH) {
				continue
			}
			if err != nil {
				return err
			}
			pidfds[pid] = pidfd
			openedAny = true
			if err := unix.PidfdSendSignal(pidfd, unix.SIGSTOP, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
				return fmt.Errorf("stop registered sandbox process %d: %w", pid, err)
			}
		}
		if !added && !openedAny {
			break
		}
	}
	for pid, pidfd := range pidfds {
		if err := unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
			return fmt.Errorf("kill registered sandbox process %d: %w", pid, err)
		}
	}
	for {
		alive := 0
		for _, pidfd := range pidfds {
			poll := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
			if n, pollErr := unix.Poll(poll, 0); pollErr != nil {
				return fmt.Errorf("poll registered sandbox process: %w", pollErr)
			} else if n > 0 {
				continue
			}
			alive++
		}
		if alive == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("prove registered sandbox process absent: %w", ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func openExactProcess(identity pkgsandbox.ProcessIdentity) (int, error) {
	pidfd, err := unix.PidfdOpen(identity.PID, 0)
	if err != nil {
		return -1, err
	}
	current, err := pkgsandbox.LinuxProcessIdentity(identity.PID)
	if err != nil || current.StartTime != identity.StartTime {
		_ = unix.Close(pidfd)
		if err != nil {
			return -1, err
		}
		return -1, os.ErrNotExist
	}
	return pidfd, nil
}

func descendantProcessIdentities(root int) (map[int]pkgsandbox.ProcessIdentity, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	type process struct {
		identity pkgsandbox.ProcessIdentity
		parent   int
		state    byte
	}
	processes := make(map[int]process)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		identity, parent, state, err := pkgsandbox.LinuxProcessStat(pid)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if state != 'Z' {
			processes[pid] = process{identity: identity, parent: parent, state: state}
		}
	}
	result := make(map[int]pkgsandbox.ProcessIdentity)
	result[root] = processes[root].identity
	changed := true
	for changed {
		changed = false
		for pid, process := range processes {
			if _, known := result[pid]; known {
				continue
			}
			if _, parentKnown := result[process.parent]; parentKnown {
				result[pid] = process.identity
				changed = true
			}
		}
	}
	delete(result, root)
	return result, nil
}
