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
	agentsCtx string
	switchCtx string
	switched  bool
}

func (h *fullSurfaceHandler) HandleIncoming(ctx context.Context, _ pkgchannel.IncomingMessage, _, _ string) (string, bool, *pkgchannel.ChatStream, error) {
	h.handle = ctx
	h.handleCtx, _ = ctx.Value(marker).(string)
	return "ok", true, nil, nil
}

func (h *fullSurfaceHandler) ListModels() []pkgchannel.ModelOption {
	return []pkgchannel.ModelOption{{Provider: "p", Model: "m"}}
}
func (h *fullSurfaceHandler) SwitchModel(string, string) error { return nil }
func (h *fullSurfaceHandler) ListAgents(ctx context.Context, _ pkgchannel.IncomingMessage) ([]pkgchannel.AgentInfo, string, error) {
	h.agentsCtx, _ = ctx.Value(marker).(string)
	return nil, "", nil
}

func (h *fullSurfaceHandler) SwitchAgent(ctx context.Context, _ pkgchannel.IncomingMessage, _ string) error {
	h.switchCtx, _ = ctx.Value(marker).(string)
	h.switched = true
	return nil
}
func (h *fullSurfaceHandler) RegisterBotIdentity(string, string, string)    {}
func (h *fullSurfaceHandler) RegisterGroupPublisher(string, GroupPublisher) {}
func (h *fullSurfaceHandler) ProvisionUser(context.Context, pkgchannel.ProvisionRequest) error {
	return nil
}

func (h *fullSurfaceHandler) ResolveUserRoot(context.Context, pkgchannel.IncomingMessage) (string, error) {
	return "", nil
}

func (h *fullSurfaceHandler) EnsurePlatformGroupMember(context.Context, string, string, string) error {
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
	if _, _, err := wrapped.ListAgents(callCtx, pkgchannel.IncomingMessage{}); err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if inner.agentsCtx != "poll" {
		t.Fatalf("ListAgents value = %q, want call-scoped value", inner.agentsCtx)
	}
	if err := wrapped.SwitchAgent(callCtx, pkgchannel.IncomingMessage{}, "x"); err != nil {
		t.Fatalf("SwitchAgent: %v", err)
	}
	if inner.switchCtx != "poll" {
		t.Fatalf("SwitchAgent value = %q, want call-scoped value", inner.switchCtx)
	}
	cancelOperation()
	select {
	case <-inner.handle.Done():
	case <-time.After(time.Second):
		t.Fatal("operation context cancellation did not reach accepted operation")
	}

	// ListModels/SwitchModel delegate unchanged.
	if got := wrapped.ListModels(); len(got) != 1 || got[0].Model != "m" {
		t.Fatalf("ListModels did not delegate: %v", got)
	}
}

func TestWrapOperationHandlerPreservesOptionalInterfaces(t *testing.T) {
	wrapped := WrapOperationHandler(&fullSurfaceHandler{}, context.Background())

	if _, ok := wrapped.(pkgchannel.BotRegistrar); !ok {
		t.Error("wrapper dropped BotRegistrar")
	}
	if _, ok := wrapped.(pkgchannel.Provisioner); !ok {
		t.Error("wrapper dropped Provisioner")
	}
	if _, ok := wrapped.(pkgchannel.UserRootResolver); !ok {
		t.Error("wrapper dropped UserRootResolver")
	}
	if _, ok := wrapped.(interface {
		RegisterGroupPublisher(string, GroupPublisher)
	}); !ok {
		t.Error("wrapper dropped RegisterGroupPublisher")
	}
	if _, ok := wrapped.(interface {
		EnsurePlatformGroupMember(context.Context, string, string, string) error
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
