package credentials

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/vaayne/anna/pkg/memory"
	"github.com/vaayne/anna/pkg/tools"
)

var oauthInputSchemaBase = func() map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["status", "connect", "disconnect"],
      "description": "status=show which providers are available and connected; connect=start an OAuth flow (requires provider) or poll its progress (requires provider and flow_id); disconnect=remove OAuth credentials for a provider (requires provider)"
    },
    "provider": {
      "type": "string",
      "enum": [],
      "description": "OAuth provider name (required for connect and disconnect)"
    },
    "flow_id": {
      "type": "string",
      "description": "Flow ID returned by a previous connect call; include to poll progress instead of starting a new flow"
    }
  },
  "required": ["action"]
}`), &m)
	return m
}()

// OAuthTool manages OAuth provider connections.
type OAuthTool struct {
	svc *Service
}

// NewOAuthTool creates an OAuthTool backed by the given service.
func NewOAuthTool(svc *Service) *OAuthTool {
	return &OAuthTool{svc: svc}
}

// OAuthDefinition returns the tool definition without requiring a live service.
func OAuthDefinition() tools.Definition {
	return tools.Definition{
		Name:        "oauth",
		Description: "Manage OAuth provider connections. Check which providers are available and connected, start an OAuth flow for the user to authenticate, poll flow progress, or disconnect a provider.",
		InputSchema: cloneMap(oauthInputSchemaBase),
	}
}

// Definition implements tools.Tool. The provider enum is built dynamically from
// the registry so newly-declared manifest providers are immediately reachable.
func (t *OAuthTool) Definition() tools.Definition {
	schema := cloneMap(oauthInputSchemaBase)
	providers := []string{}
	if t.svc != nil && t.svc.registry != nil {
		providers = t.svc.registry.IDs()
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		if prov, ok := props["provider"].(map[string]any); ok {
			if len(providers) > 0 {
				prov["enum"] = providers
			}
		}
	}
	desc := "Manage OAuth provider connections. Check which providers are available and connected, start an OAuth flow for the user to authenticate, poll flow progress, or disconnect a provider."
	if len(providers) > 0 {
		desc = fmt.Sprintf("Manage OAuth provider connections (%s). Check which providers are available and connected, start an OAuth flow for the user to authenticate, poll flow progress, or disconnect a provider.", strings.Join(providers, ", "))
	}
	return tools.Definition{
		Name:        "oauth",
		Description: desc,
		InputSchema: schema,
	}
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	maps.Copy(out, m)
	return out
}

// Execute implements tools.Tool.
func (t *OAuthTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action, _ := args["action"].(string)
	userID := memory.UserIDFromContext(ctx)
	if userID == 0 {
		return "", fmt.Errorf("oauth tool requires user context")
	}
	switch action {
	case "status":
		return t.status(ctx, userID)
	case "connect":
		return t.connect(ctx, userID, args)
	case "disconnect":
		return t.disconnect(ctx, userID, args)
	default:
		return "", fmt.Errorf("unknown action %q; expected status/connect/disconnect", action)
	}
}

func (t *OAuthTool) status(ctx context.Context, userID int64) (string, error) {
	providers := t.svc.GetProviderStatuses(ctx, userID)
	var b strings.Builder
	b.WriteString("OAuth providers:\n")
	for _, p := range providers {
		switch {
		case !p.Available:
			fmt.Fprintf(&b, "  %s: unavailable (%s)\n", p.Provider, p.Unavailable)
		case p.Connected:
			label := p.Username
			if label == "" {
				label = "connected"
			}
			fmt.Fprintf(&b, "  %s: connected (%s)\n", p.Provider, label)
		default:
			fmt.Fprintf(&b, "  %s: available, not connected\n", p.Provider)
		}
	}
	return b.String(), nil
}

func (t *OAuthTool) connect(ctx context.Context, userID int64, args map[string]any) (string, error) {
	provider, _ := args["provider"].(string)
	if provider == "" {
		return "", fmt.Errorf("provider is required for connect action")
	}
	flowID, _ := args["flow_id"].(string)

	if flowID == "" {
		// Start a new flow.
		status, err := t.svc.StartFlow(ctx, userID, provider)
		if err != nil {
			return "", err
		}
		out, _ := json.MarshalIndent(status, "", "  ")
		return fmt.Sprintf("OAuth flow started. Visit the URL to authenticate:\n%s\n\nCall connect again with provider=%q and flow_id=%q to check progress.", out, provider, status.FlowID), nil
	}

	// Poll an existing flow.
	status, completed, err := t.svc.PollFlow(ctx, userID, provider, flowID)
	if err != nil {
		return "", err
	}
	if completed {
		return fmt.Sprintf("%s OAuth completed successfully. Your credentials are stored. Continue with the original task.", provider), nil
	}
	out, _ := json.MarshalIndent(status, "", "  ")
	return fmt.Sprintf("Flow status:\n%s\n\nKeep calling connect with the same provider and flow_id until you receive a success message.", out), nil
}

func (t *OAuthTool) disconnect(ctx context.Context, userID int64, args map[string]any) (string, error) {
	provider, _ := args["provider"].(string)
	if provider == "" {
		return "", fmt.Errorf("provider is required for disconnect action")
	}
	if err := t.svc.Disconnect(ctx, userID, provider); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s credentials removed.", provider), nil
}
