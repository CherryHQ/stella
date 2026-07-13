package main

import (
	"os"
	"syscall"
	"testing"
)

// TestWatchStartupSignalHandsBackWhenReady covers the race the helper exists for:
// a signal is deliverable at the same instant startup completes. Whichever select
// branch the watcher takes, it must NOT abort and must leave the signal available
// for the drain supervisor.
func TestWatchStartupSignalHandsBackWhenReady(t *testing.T) {
	for i := range 100 {
		sigCh := make(chan os.Signal, 2)
		startupDone := make(chan struct{})
		close(startupDone) // startup already finished
		sigCh <- syscall.SIGTERM

		aborted := false
		watchStartupSignal(sigCh, startupDone, func() { aborted = true })

		if aborted {
			t.Fatalf("iteration %d: aborted after startup completed", i)
		}
		select {
		case <-sigCh:
			// Signal preserved for the drain supervisor.
		default:
			t.Fatalf("iteration %d: signal was lost, not handed back", i)
		}
	}
}

// TestWatchStartupSignalAbortsDuringStartup verifies a signal before handoff
// aborts startup.
func TestWatchStartupSignalAbortsDuringStartup(t *testing.T) {
	sigCh := make(chan os.Signal, 2)
	startupDone := make(chan struct{}) // startup NOT complete
	sigCh <- syscall.SIGINT

	aborted := false
	watchStartupSignal(sigCh, startupDone, func() { aborted = true })

	if !aborted {
		t.Fatal("expected abort when a signal arrives during startup")
	}
}

// TestWatchStartupSignalCleanHandoff verifies the normal path: startup completes
// with no signal, the watcher returns without aborting and leaks nothing.
func TestWatchStartupSignalCleanHandoff(t *testing.T) {
	sigCh := make(chan os.Signal, 2)
	startupDone := make(chan struct{})
	close(startupDone)

	aborted := false
	watchStartupSignal(sigCh, startupDone, func() { aborted = true })

	if aborted {
		t.Fatal("clean handoff must not abort")
	}
}
