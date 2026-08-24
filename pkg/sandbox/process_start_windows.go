//go:build windows

package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows has no process group that outlives an intermediate process, so the
// "prove the tree is destroyed" invariant is carried by one Job Object per
// started command: every descendant is confined to the job,
// TerminateJobObject kills the whole tree at once, and the job's
// active-process count is the proof that nothing is left.
//
// Not covered here: recovery after stellad itself dies. The durable
// PID+start-time identity registry is Linux-only. JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
// bounds the damage instead: when our last handle to the job goes away, which
// includes our own process dying, the kernel kills the tree.

// jobObjectBasicAccountingInformation mirrors
// JOBOBJECT_BASIC_ACCOUNTING_INFORMATION, which golang.org/x/sys/windows does
// not declare. Only ActiveProcesses is read.
type jobObjectBasicAccountingInformation struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

// jobFence owns the job handle for one started command. The RWMutex exists so
// that terminating or querying the job never blocks behind the multi-second
// absence poll: a killer must be able to run *while* a prover is polling,
// otherwise the kill that makes the proof succeed would be serialized after it.
type jobFence struct {
	mu     sync.RWMutex
	handle windows.Handle
	closed bool
}

// terminate kills every process in the job. Returns false when the fence has
// already been retired, so the caller can fall back to the leader handle.
func (f *jobFence) terminate() bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
		return false
	}
	_ = windows.TerminateJobObject(f.handle, 1)
	return true
}

// activeProcesses reports how many processes remain in the job. The bool is
// false once the fence is retired, which already means the tree is gone.
func (f *jobFence) activeProcesses() (uint32, bool, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
		return 0, false, nil
	}
	var info jobObjectBasicAccountingInformation
	err := windows.QueryInformationJobObject(
		f.handle,
		windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
		nil,
	)
	if err != nil {
		return 0, true, err
	}
	return info.ActiveProcesses, true, nil
}

// retire closes the job handle exactly once. Closing the last handle also fires
// KILL_ON_JOB_CLOSE, so anything that slipped into the job between the
// zero-count query and this call still dies.
func (f *jobFence) retire() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	f.closed = true
	_ = windows.CloseHandle(f.handle)
}

// fences maps a started *exec.Cmd to the job owning its process tree. An entry
// is dropped as soon as the tree is proven absent, so the map stays bounded by
// the number of live sandbox processes.
var fences sync.Map // *exec.Cmd -> *jobFence

// SetProcessTreeSysProcAttr prepares cmd so that StartProcessRegistered can
// fence its process tree. The child is created suspended: assigning it to the
// job before it runs a single instruction closes the race where it spawns a
// descendant that would land outside the job.
func SetProcessTreeSysProcAttr(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
}

// StartProcessRegistered starts cmd and confines its process tree to a fresh
// Job Object. registrar is ignored: durable cross-restart process identity is a
// Linux-only mechanism.
func StartProcessRegistered(_ context.Context, cmd *exec.Cmd, _ ProcessRegistrar) error {
	suspended := cmd.SysProcAttr != nil &&
		cmd.SysProcAttr.CreationFlags&windows.CREATE_SUSPENDED != 0
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := fenceProcessTree(cmd, suspended); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	return nil
}

// fenceProcessTree creates the job, assigns the freshly started leader to it
// and, when the leader was created suspended, releases it.
func fenceProcessTree(cmd *exec.Cmd, suspended bool) error {
	if cmd.Process == nil {
		return fmt.Errorf("sandbox: process tree fence: process was not started")
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("sandbox: create job object: %w", err)
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("sandbox: configure job object: %w", err)
	}

	// os/exec keeps the original process handle open, so the PID cannot have
	// been recycled and reopening it by PID is safe here.
	proc, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("sandbox: open sandbox process %d: %w", cmd.Process.Pid, err)
	}
	// Nested jobs are supported since Windows 8, so stellad running inside a job
	// itself does not block this assignment.
	assignErr := windows.AssignProcessToJobObject(job, proc)
	_ = windows.CloseHandle(proc)
	if assignErr != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("sandbox: assign process %d to job: %w", cmd.Process.Pid, assignErr)
	}

	fence := &jobFence{handle: job}
	// Publish before resuming: a kill racing with the resume must find the job,
	// not fall back to killing the leader alone.
	fences.Store(cmd, fence)
	if suspended {
		if err := resumeProcess(uint32(cmd.Process.Pid)); err != nil {
			fences.Delete(cmd)
			// Retiring the job fires KILL_ON_JOB_CLOSE, which disposes of the
			// still-suspended leader.
			fence.retire()
			return err
		}
	}
	return nil
}

// resumeProcess releases a process created with CREATE_SUSPENDED. os/exec closes
// the initial thread handle before returning, so the thread has to be found
// again through a toolhelp snapshot.
func resumeProcess(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("sandbox: snapshot threads of process %d: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()

	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	resumed := 0
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if openErr != nil {
			return fmt.Errorf("sandbox: open thread %d of process %d: %w", entry.ThreadID, pid, openErr)
		}
		_, resumeErr := windows.ResumeThread(thread)
		_ = windows.CloseHandle(thread)
		if resumeErr != nil {
			return fmt.Errorf("sandbox: resume thread %d of process %d: %w", entry.ThreadID, pid, resumeErr)
		}
		resumed++
	}
	if resumed == 0 {
		return fmt.Errorf("sandbox: no resumable thread found for process %d", pid)
	}
	return nil
}

// KillProcessTree terminates every process spawned by cmd. It falls back to the
// leader alone when cmd has no fence, which only happens before the job exists.
func KillProcessTree(cmd *exec.Cmd) {
	if value, ok := fences.Load(cmd); ok {
		if value.(*jobFence).terminate() {
			return
		}
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// WaitProcessTreeAbsent blocks until the job of cmd holds no process, and
// returns an error when that cannot be proven within the deadline. Callers treat
// that error as "still fenced" and keep the resource alive.
func WaitProcessTreeAbsent(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return fmt.Errorf("sandbox: cannot prove absence of an unstarted process")
	}
	value, ok := fences.Load(cmd)
	if !ok {
		// Either the tree was already proven absent (the entry is dropped on
		// proof), or fencing never took effect, in which case
		// StartProcessRegistered killed the leader before it ran any code and no
		// descendant can exist.
		return nil
	}
	fence := value.(*jobFence)

	deadline := time.Now().Add(5 * time.Second)
	for {
		active, alive, err := fence.activeProcesses()
		if err != nil {
			return fmt.Errorf("sandbox: prove process tree of pid %d absent: %w", cmd.Process.Pid, err)
		}
		if !alive || active == 0 {
			fence.retire()
			fences.Delete(cmd)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sandbox: process tree of pid %d still exists", cmd.Process.Pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
