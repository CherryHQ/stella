package pluginmemory_test

import (
	"context"
	"testing"

	"github.com/vaayne/anna/pkg/memory"
	"github.com/vaayne/anna/pkg/memory/memorytest"
	pluginmemory "github.com/vaayne/anna/plugins/memory"
)

func TestMemoryRegisterAndList(t *testing.T) {
	pluginmemory.Register("test-memory", pluginmemory.Registration{
		Factory: func(_ context.Context, _ pluginmemory.BuildContext) (memory.Provider, error) {
			return memorytest.New(), nil
		},
		Meta: pluginmemory.ProviderMeta{Name: "Test Memory", Description: "for testing"},
	})

	names := pluginmemory.List()
	var found bool
	for _, n := range names {
		if n == "test-memory" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'test-memory' in List()")
	}
}

func TestMemoryMetas(t *testing.T) {
	pluginmemory.Register("meta-memory", pluginmemory.Registration{
		Factory: func(_ context.Context, _ pluginmemory.BuildContext) (memory.Provider, error) {
			return memorytest.New(), nil
		},
		Meta: pluginmemory.ProviderMeta{Name: "Meta Memory"},
	})

	metas := pluginmemory.Metas()
	if _, ok := metas["meta-memory"]; !ok {
		t.Error("expected 'meta-memory' in Metas()")
	}
}

func TestMemoryBuild_Success(t *testing.T) {
	pluginmemory.Register("buildable-memory", pluginmemory.Registration{
		Factory: func(_ context.Context, _ pluginmemory.BuildContext) (memory.Provider, error) {
			return memorytest.New(), nil
		},
	})

	provider, err := pluginmemory.Build(context.Background(), "buildable-memory", pluginmemory.BuildContext{})
	if err != nil {
		t.Fatal(err)
	}
	if provider == nil {
		t.Error("expected non-nil provider")
	}
}

func TestMemoryBuild_Unknown(t *testing.T) {
	_, err := pluginmemory.Build(context.Background(), "nonexistent-memory", pluginmemory.BuildContext{})
	if err == nil {
		t.Error("expected error for unknown memory plugin")
	}
}
