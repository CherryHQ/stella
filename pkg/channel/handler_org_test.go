package channel

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/orgctx"
)

type recordingHandler struct {
	gotOrg     string
	provOrg    string
	resolveOrg string
}

func (h *recordingHandler) HandleIncoming(ctx context.Context, _ IncomingMessage, _ string, _ string) (string, bool, *ChatStream, error) {
	h.gotOrg = orgctx.OrgIDFromContext(ctx)
	return "", true, nil, nil
}
func (h *recordingHandler) ListModels() []ModelOption     { return nil }
func (h *recordingHandler) SwitchModel(_, _ string) error { return nil }
func (h *recordingHandler) ListAgents(context.Context, IncomingMessage) ([]AgentInfo, string, error) {
	return nil, "", nil
}
func (h *recordingHandler) SwitchAgent(context.Context, IncomingMessage, string) error { return nil }

type fullHandler struct {
	recordingHandler
}

func (h *fullHandler) ResolveUserRoot(ctx context.Context, _ IncomingMessage) (string, error) {
	h.resolveOrg = orgctx.OrgIDFromContext(ctx)
	return "/tmp", nil
}

func (h *fullHandler) ProvisionUser(ctx context.Context, _ ProvisionRequest) error {
	h.provOrg = orgctx.OrgIDFromContext(ctx)
	return nil
}

func TestHandlerWithOrgID_InjectsOrgIntoHandleIncoming(t *testing.T) {
	inner := &recordingHandler{}
	wrapped := HandlerWithOrgID("org-A", inner)
	if _, _, _, err := wrapped.HandleIncoming(context.Background(), IncomingMessage{}, "", ""); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}
	if inner.gotOrg != "org-A" {
		t.Fatalf("HandleIncoming ctx org = %q, want org-A", inner.gotOrg)
	}
}

func TestHandlerWithOrgID_EmptyOrgIsPassthrough(t *testing.T) {
	inner := &recordingHandler{}
	wrapped := HandlerWithOrgID("", inner)
	if any(wrapped) != any(inner) {
		t.Fatalf("empty orgID should return inner as-is")
	}
}

func TestHandlerWithOrgID_PreservesOptionalInterfaces(t *testing.T) {
	inner := &fullHandler{}
	wrapped := HandlerWithOrgID("org-X", inner)

	resolver, ok := wrapped.(UserRootResolver)
	if !ok {
		t.Fatal("expected wrapper to satisfy UserRootResolver when inner does")
	}
	if _, err := resolver.ResolveUserRoot(context.Background(), IncomingMessage{}); err != nil {
		t.Fatalf("ResolveUserRoot: %v", err)
	}
	if inner.resolveOrg != "org-X" {
		t.Fatalf("ResolveUserRoot ctx org = %q, want org-X", inner.resolveOrg)
	}

	provisioner, ok := wrapped.(Provisioner)
	if !ok {
		t.Fatal("expected wrapper to satisfy Provisioner when inner does")
	}
	if err := provisioner.ProvisionUser(context.Background(), ProvisionRequest{}); err != nil {
		t.Fatalf("ProvisionUser: %v", err)
	}
	if inner.provOrg != "org-X" {
		t.Fatalf("ProvisionUser ctx org = %q, want org-X", inner.provOrg)
	}
}

func TestHandlerWithOrgID_DoesNotFakeOptionalInterfaces(t *testing.T) {
	inner := &recordingHandler{} // does NOT implement UserRootResolver/Provisioner
	wrapped := HandlerWithOrgID("org-A", inner)
	if _, ok := wrapped.(UserRootResolver); ok {
		t.Fatal("wrapper should not satisfy UserRootResolver when inner does not")
	}
	if _, ok := wrapped.(Provisioner); ok {
		t.Fatal("wrapper should not satisfy Provisioner when inner does not")
	}
}
