//go:build darwin

package testbed

import (
	"io/fs"
	"strconv"

	"golang.org/x/sys/unix"
)

func identityFor(pid int) (processIdentity, error) {
	p, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || p == nil || p.Proc.P_pid != int32(pid) {
		return processIdentity{}, fs.ErrNotExist
	}
	t := p.Proc.P_starttime
	return processIdentity{PID: pid, Started: strconv.FormatInt(t.Sec, 10) + "." + strconv.FormatInt(int64(t.Usec), 10)}, nil
}
