package plugins

import (
	"context"
	"testing"
	"time"
)

type runtimeLookupStub struct {
	handle RuntimeHandle
}

func (s runtimeLookupStub) Get(pluginID string, runtimeName string) (RuntimeHandle, bool) {
	if s.handle == nil || pluginID != "channel/telegram" || runtimeName != "bot" {
		return nil, false
	}
	return s.handle, true
}

type runtimeHandleStub struct {
	snapshot RuntimeSnapshot
}

func (s runtimeHandleStub) Snapshot(context.Context) (RuntimeSnapshot, error) {
	return s.snapshot, nil
}

func TestManagedRuntimeStatus(t *testing.T) {
	now := time.Now().UTC()
	status, err := managedRuntimeStatus(context.Background(), runtimeLookupStub{
		handle: runtimeHandleStub{
			snapshot: RuntimeSnapshot{
				State:     RuntimeStateRunning,
				Message:   "running",
				UpdatedAt: now,
				Metadata:  map[string]any{"channel": "telegram"},
			},
		},
	}, "channel/telegram", "bot")
	if err != nil {
		t.Fatalf("managedRuntimeStatus: %v", err)
	}
	out := status.(map[string]any)
	if out["state"] != RuntimeStateRunning {
		t.Fatalf("unexpected state: %#v", out)
	}
	if out["message"] != "running" {
		t.Fatalf("unexpected message: %#v", out)
	}
}
