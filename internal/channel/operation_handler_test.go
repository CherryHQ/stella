package channel

import (
	"context"
	"testing"
	"time"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

type ctxKey string

const marker ctxKey = "marker"

// fullSurfaceHandler implements the complete operationHandlerSurface and records
// the context each agent-facing method receives.
type fullSurfaceHandler struct {
	handle    context.Context
	handleCtx string
	assetCtx  string
}

func (h *fullSurfaceHandler) HandleIncoming(ctx context.Context, _ pkgchannel.IncomingMessage, _, _ string) (string, bool, *pkgchannel.ChatStream, error) {
	h.handle = ctx
	h.handleCtx, _ = ctx.Value(marker).(string)
	return "ok", true, nil, nil
}

func (h *fullSurfaceHandler) RegisterBotIdentity(string, string, string)    {}
func (h *fullSurfaceHandler) UnregisterBotIdentity(string, string, string)  {}
func (h *fullSurfaceHandler) RegisterGroupPublisher(string, GroupPublisher) {}
func (h *fullSurfaceHandler) UnregisterGroupPublisher(string)               {}
func (h *fullSurfaceHandler) ProvisionUser(context.Context, pkgchannel.ProvisionRequest) error {
	return nil
}

func (h *fullSurfaceHandler) AdmitAssetSave(context.Context, pkgchannel.IncomingMessage) error {
	return nil
}

func (h *fullSurfaceHandler) SaveAsset(ctx context.Context, _ pkgchannel.IncomingMessage, _ string, _ []byte) (string, error) {
	h.assetCtx, _ = ctx.Value(marker).(string)
	return "saved", nil
}

func (h *fullSurfaceHandler) EnsurePlatformGroupMember(context.Context, string, string, string) error {
	return nil
}

func (h *fullSurfaceHandler) EnsurePlatformThreadGroupMember(context.Context, string, string, string, string) error {
	return nil
}

func (h *fullSurfaceHandler) ImportGroupHistory(context.Context, []pkgchannel.IncomingMessage) error {
	return nil
}

func (h *fullSurfaceHandler) RemovePlatformGroupMember(context.Context, string, string, string) error {
	return nil
}

func TestWrapOperationHandlerUsesOperationLifetimeAndCallValues(t *testing.T) {
	inner := &fullSurfaceHandler{}
	opCtx, cancelOperation := context.WithCancel(context.WithValue(context.Background(), marker, "operation"))
	defer cancelOperation()
	pollParent, cancelPoll := context.WithCancel(context.WithValue(context.Background(), marker, "poll"))
	callCtx, cancelDeadline := context.WithTimeout(pollParent, time.Minute)
	defer cancelDeadline()

	wrapped := WrapOperationHandler(inner, opCtx)

	if _, _, _, err := wrapped.HandleIncoming(callCtx, pkgchannel.IncomingMessage{}, "", ""); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}
	if inner.handleCtx != "poll" {
		t.Fatalf("HandleIncoming value = %q, want call-scoped value", inner.handleCtx)
	}
	wantDeadline, _ := callCtx.Deadline()
	gotDeadline, ok := inner.handle.Deadline()
	if !ok || !gotDeadline.Equal(wantDeadline) {
		t.Fatalf("operation deadline = %v, %v; want %v", gotDeadline, ok, wantDeadline)
	}
	cancelPoll()
	if err := inner.handle.Err(); err != nil {
		t.Fatalf("poll cancellation leaked into accepted operation: %v", err)
	}
	cancelOperation()
	select {
	case <-inner.handle.Done():
	case <-time.After(time.Second):
		t.Fatal("operation context cancellation did not reach accepted operation")
	}
}

func TestWrapOperationHandlerPreservesOptionalInterfaces(t *testing.T) {
	inner := &fullSurfaceHandler{}
	wrapped := WrapOperationHandler(inner, context.Background())

	if _, ok := wrapped.(pkgchannel.BotRegistrar); !ok {
		t.Error("wrapper dropped BotRegistrar")
	}
	if _, ok := wrapped.(pkgchannel.Provisioner); !ok {
		t.Error("wrapper dropped Provisioner")
	}
	if _, ok := wrapped.(pkgchannel.AssetSaveAdmitter); !ok {
		t.Error("wrapper dropped UserRootResolver")
	}
	assetSaver, ok := wrapped.(pkgchannel.AssetSaver)
	if !ok {
		t.Fatal("wrapper dropped AssetSaver")
	}
	if _, err := assetSaver.SaveAsset(context.WithValue(context.Background(), marker, "asset"), pkgchannel.IncomingMessage{}, "file", nil); err != nil {
		t.Fatalf("SaveAsset: %v", err)
	}
	if inner.assetCtx != "asset" {
		t.Fatalf("SaveAsset context value = %q, want call-scoped value", inner.assetCtx)
	}
	if _, ok := wrapped.(interface {
		RegisterGroupPublisher(string, GroupPublisher)
	}); !ok {
		t.Error("wrapper dropped RegisterGroupPublisher")
	}
	if _, ok := wrapped.(interface {
		EnsurePlatformGroupMember(context.Context, string, string, string) error
		EnsurePlatformThreadGroupMember(context.Context, string, string, string, string) error
		ImportGroupHistory(context.Context, []pkgchannel.IncomingMessage) error
		RemovePlatformGroupMember(context.Context, string, string, string) error
	}); !ok {
		t.Error("wrapper dropped group-member provisioner")
	}
}

// baseOnlyHandler implements just pkgchannel.Handler.
type baseOnlyHandler struct{}

func (baseOnlyHandler) HandleIncoming(context.Context, pkgchannel.IncomingMessage, string, string) (string, bool, *pkgchannel.ChatStream, error) {
	return "", false, nil, nil
}
func (baseOnlyHandler) ListModels() []pkgchannel.ModelOption { return nil }
func (baseOnlyHandler) SwitchModel(string, string) error     { return nil }
func (baseOnlyHandler) ListAgents(context.Context, pkgchannel.IncomingMessage) ([]pkgchannel.AgentInfo, string, error) {
	return nil, "", nil
}

func (baseOnlyHandler) SwitchAgent(context.Context, pkgchannel.IncomingMessage, string) error {
	return nil
}

func TestWrapOperationHandlerPassesThroughIncompleteHandlers(t *testing.T) {
	base := baseOnlyHandler{}
	if got := WrapOperationHandler(base, context.Background()); got != pkgchannel.Handler(base) {
		t.Error("handler without full surface should be returned unchanged")
	}
	full := &fullSurfaceHandler{}
	if got := WrapOperationHandler(full, nil); got != pkgchannel.Handler(full) {
		t.Error("nil operation context should return the handler unchanged")
	}
}
