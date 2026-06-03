// Package agentoauth exposes OAuth connection management as native agent tools.
// Connections are scoped to the acting user (read from context). OAuth is
// human-in-the-loop: oauth_connect returns the verification URL immediately and
// spawns a background watcher that notifies the user once the flow completes,
// so the agent's turn is never blocked on the human authorizing.
package agentoauth

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/CherryHQ/stella/internal/credentials"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/tools/toolctx"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/tools"
)

// Notifier delivers a one-shot push notification to a user when a backgrounded
// OAuth flow reaches a terminal state.
type Notifier interface {
	NotifyUser(ctx context.Context, userID string, n pkgchannel.Notification) error
}

// BackgroundRunner runs fn in a tracked goroutine. The ctx passed to fn is
// scoped to the server lifetime (cancelled on shutdown), NOT to the tool call —
// the watcher must outlive the agent turn that started the flow.
type BackgroundRunner func(fn func(ctx context.Context))

// pollInterval is how often the watcher reads flow state; flowTimeout caps how
// long it waits before giving up if the broker never sets an expiry.
const (
	pollInterval = 3 * time.Second
	flowTimeout  = 15 * time.Minute
)

// oauthService is the slice of *credentials.Service the tools depend on. Keeping
// it an interface lets the watcher's notification semantics be tested without a
// live OAuth provider.
type oauthService interface {
	GetProviderStatuses(ctx context.Context, userID string) []credentials.ProviderStatus
	StartFlow(ctx context.Context, userID, provider string) (credentials.FlowStatus, error)
	PollFlow(ctx context.Context, userID, provider, flowID string) (credentials.FlowStatus, bool, error)
	Disconnect(ctx context.Context, userID, provider string) error
}

// NewTools builds the OAuth native tools. Returns nil when the credentials
// service is unavailable so callers can append unconditionally.
func NewTools(svc *credentials.Service, notifier Notifier, bg BackgroundRunner) []tools.Tool {
	if svc == nil {
		return nil
	}
	return newTools(svc, notifier, bg)
}

func newTools(svc oauthService, notifier Notifier, bg BackgroundRunner) []tools.Tool {
	t := &impl{svc: svc, notifier: notifier, bg: bg}
	return []tools.Tool{
		fnTool{providersDef(), t.providers},
		fnTool{statusDef(), t.status},
		fnTool{connectDef(), t.connect},
		fnTool{disconnectDef(), t.disconnect},
	}
}

type impl struct {
	svc      oauthService
	notifier Notifier
	bg       BackgroundRunner
}

func (t *impl) providers(ctx context.Context, _ map[string]any) (string, error) {
	userID, err := toolctx.UserID(ctx)
	if err != nil {
		return "", err
	}
	return marshal(t.svc.GetProviderStatuses(ctx, userID))
}

func (t *impl) status(ctx context.Context, args map[string]any) (string, error) {
	userID, err := toolctx.UserID(ctx)
	if err != nil {
		return "", err
	}
	provider, _ := args["provider"].(string)
	if provider == "" {
		return "", fmt.Errorf("provider is required")
	}
	for _, ps := range t.svc.GetProviderStatuses(ctx, userID) {
		if ps.Provider == provider {
			return marshal(ps)
		}
	}
	return "", fmt.Errorf("unknown provider %q", provider)
}

func (t *impl) disconnect(ctx context.Context, args map[string]any) (string, error) {
	userID, err := toolctx.UserID(ctx)
	if err != nil {
		return "", err
	}
	provider, _ := args["provider"].(string)
	if provider == "" {
		return "", fmt.Errorf("provider is required")
	}
	if err := t.svc.Disconnect(ctx, userID, provider); err != nil {
		return "", err
	}
	return fmt.Sprintf("Disconnected from %s.", provider), nil
}

func (t *impl) connect(ctx context.Context, args map[string]any) (string, error) {
	userID, err := toolctx.UserID(ctx)
	if err != nil {
		return "", err
	}
	provider, _ := args["provider"].(string)
	if provider == "" {
		return "", fmt.Errorf("provider is required")
	}
	agentID := memory.AgentIDFromContext(ctx) // for notification routing only

	flow, err := t.svc.StartFlow(ctx, userID, provider)
	if err != nil {
		return "", err
	}

	t.watch(userID, agentID, provider, flow.FlowID, flow.ExpiresAt)

	out := struct {
		Provider        string `json:"provider"`
		VerificationURI string `json:"verification_uri"`
		UserCode        string `json:"user_code,omitempty"`
		Note            string `json:"note"`
	}{
		Provider:        provider,
		VerificationURI: flow.VerificationURI,
		UserCode:        flow.UserCode,
		Note:            "Ask the user to open the URL (and enter the code if shown) to authorize. You'll be notified once the connection completes — do not poll.",
	}
	return marshal(out)
}

// watch spawns the background notification watcher. It reads flow state via
// PollFlow (the broker owns the actual token polling) and pushes exactly one
// terminal notification. It must not use the request ctx — that is cancelled
// when the agent turn ends.
func (t *impl) watch(userID, agentID, provider, flowID string, expiresAt time.Time) {
	if t.bg == nil || t.notifier == nil {
		return
	}
	t.bg(func(bgctx context.Context) {
		deadline := expiresAt
		if deadline.IsZero() {
			deadline = time.Now().Add(flowTimeout)
		}
		wctx, cancel := context.WithDeadline(bgctx, deadline)
		defer cancel()

		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()

		notify := func(text string) {
			n := pkgchannel.Notification{Text: text, AgentID: agentID}
			_ = t.notifier.NotifyUser(context.WithoutCancel(bgctx), userID, n)
		}

		for {
			select {
			case <-wctx.Done():
				notify(fmt.Sprintf("OAuth connection to %s timed out before you authorized.", provider))
				return
			case <-ticker.C:
				st, done, err := t.svc.PollFlow(wctx, userID, provider, flowID)
				if err != nil {
					notify(fmt.Sprintf("OAuth connection to %s failed: %v", provider, err))
					return
				}
				if done {
					notify(fmt.Sprintf("Connected to %s.", provider))
					return
				}
				switch st.State {
				case "failed":
					notify(fmt.Sprintf("OAuth connection to %s failed.", provider))
					return
				case "expired":
					notify(fmt.Sprintf("OAuth connection to %s expired before you authorized.", provider))
					return
				}
			}
		}
	})
}

type fnTool struct {
	def tools.Definition
	fn  func(context.Context, map[string]any) (string, error)
}

func (t fnTool) Definition() tools.Definition { return t.def }
func (t fnTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	return t.fn(ctx, args)
}

func marshal(v any) (string, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}
