//go:build windows

package sandbox

import "context"

// cleanupHostProcessResource has nothing to do on Windows: by the time it can
// run, the kernel has already destroyed the process tree it would target.
//
// This function only ever covers one case, recovery after stellad itself died
// (in-process teardown belongs to Session.Close, which terminates the job and
// proves the tree empty before returning). On Windows every sandbox command is
// confined to a Job Object carrying JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, so
// stellad's death is exactly the event that kills the tree: the kernel closes
// our last handle to the job as it tears the process down, and closing the last
// handle terminates every process still in the job.
//
// The proof chain that makes this "absence already proven" rather than
// fail-open:
//
//   - Nothing escapes the job. The child is created with CREATE_SUSPENDED and
//     assigned to the job before it executes a single instruction, so it cannot
//     have spawned a descendant outside the job. Job membership is inherited by
//     every descendant and cannot be dropped.
//   - Nothing keeps the job alive past us. The only handle is the one held by
//     pkg/sandbox's fence; it is not inheritable and is never duplicated to a
//     child, so no surviving process can hold the job open and defeat
//     KILL_ON_JOB_CLOSE.
//   - Nothing is left for us to find. There is no PID+start-time registry to
//     replay on Windows, and there must not be: any process this function could
//     discover would have to have outlived the job close, which the two points
//     above rule out.
//
// Returning nil is therefore a statement about the kernel's guarantee, not an
// unchecked assumption about the host. See pkg/sandbox/process_start_windows.go
// for the fence itself.
func cleanupHostProcessResource(_ context.Context, _ string) error {
	return nil
}
