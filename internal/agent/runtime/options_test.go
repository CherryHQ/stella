package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/agentrun"
)

func TestCompletionBarrierFailsClosedWithoutLiveOwnershipChecker(t *testing.T) {
	barrier := NewCompletionBarrier()
	barrier.bind(context.Background(), agentrun.Guard{
		RunID: "run", SessionID: "session", ExecutorBootID: "boot",
	})
	_, err := barrier.Context(t.Context())
	if err == nil || !strings.Contains(err.Error(), "live ownership checker") {
		t.Fatalf("Context error = %v, want missing live checker", err)
	}
}
