//go:build !windows

package vision

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

var xbergGroupConfirmWait = time.Second

func xbergFallbackSupported() error { return nil }

// terminateXbergProcessTree must run before cmd.Wait. The still-unreaped
// supervisor is the process-group leader, so ManagedCommandContext can kill
// exactly its group without relying on a potentially recycled numeric PGID.
func terminateXbergProcessTree(cmd *exec.Cmd) error {
	err := cmd.Cancel()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

// confirmXbergProcessGroupGone probes only after the held supervisor has been
// reaped. Before reap, its PID pins the group identity; afterward, this probe
// never signals the numeric PGID, so a rapid reuse can only cause a bounded
// cleanup failure, never harm an unrelated group. This is process-group
// containment, not a proof about a malicious descendant that called setsid;
// a retained daemon pipe from such a process fails the bounded drain above.
func confirmXbergProcessGroupGone(pgid int) error {
	timer := time.NewTimer(xbergGroupConfirmWait)
	defer timer.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		select {
		case <-timer.C:
			return fmt.Errorf("xberg process group remained after %s", xbergGroupConfirmWait)
		case <-tick.C:
		}
	}
}
