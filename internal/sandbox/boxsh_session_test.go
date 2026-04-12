package sandbox

import (
	"testing"
	"time"

	"github.com/vaayne/anna/internal/sandbox/boxshclient"
)

func TestBoxshSessionDoneClosesWhenBackendDies(t *testing.T) {
	session := &boxshSession{
		backend: &boxshclient.SharedBackend{}, // Alive() == false because no client is attached
		done:    make(chan struct{}),
	}

	go session.watchBackend()

	select {
	case <-session.Done():
		// expected
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Done() should close when backend is no longer alive")
	}
}
