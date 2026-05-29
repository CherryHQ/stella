package plugins

import (
	"context"
	"testing"
	"time"
)

type runtimeLookupStub struct {
	handle RuntimeHandle
}

func (s runtimeLookupStub) Get(_ context.Context, pluginID string, runtimeName string) (RuntimeHandle, bool) {
	if s.handle == nil || pluginID != "channel/telegram" || runtimeName != "bot" {
		return nil, false
	}
	return s.handle, true
}

func (s runtimeLookupStub) Lookup(ctx context.Context, pluginID string, runtimeName string) (RuntimeHandle, bool) {
	return s.Get(ctx, pluginID, runtimeName)
}

type runtimeHandleStub struct {
	snapshot RuntimeStatus
}

func (s runtimeHandleStub) Snapshot(context.Context) (RuntimeStatus, error) {
	return s.snapshot, nil
}

func (s runtimeHandleStub) Status(ctx context.Context) (RuntimeStatus, error) {
	return s.Snapshot(ctx)
}

func TestManagedRuntimeStatus(t *testing.T) {
	now := time.Now().UTC()
	status, err := managedRuntimeStatus(context.Background(), runtimeLookupStub{
		handle: runtimeHandleStub{
			snapshot: RuntimeStatus{
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
