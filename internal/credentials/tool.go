package credentials

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vaayne/anna/pkg/memory"
	"github.com/vaayne/anna/pkg/tools"
)

var credentialsInputSchema = func() map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["status", "list", "delete", "oauth_start", "oauth_poll", "oauth_disconnect", "add_secret"],
      "description": "The action to perform: status=check providers and vault, list=list stored secrets, delete=remove a secret, oauth_start=begin OAuth device flow, oauth_poll=check OAuth flow progress, oauth_disconnect=remove OAuth credentials, add_secret=get instructions for storing a manual secret"
    },
    "provider": {
      "type": "string",
      "enum": ["github", "lark"],
      "description": "OAuth provider (required for oauth_* actions)"
    },
    "flow_id": {
      "type": "string",
      "description": "Flow ID from oauth_start (required for oauth_poll)"
    },
    "name": {
      "type": "string",
      "description": "Secret key name in uppercase (required for delete and add_secret)"
    },
    "purpose": {
      "type": "string",
      "description": "Why this secret is needed (used in add_secret instructions)"
    }
  },
  "required": ["action"]
}`), &m)
	return m
}()

// Tool is the built-in credentials tool available to all agents.
type Tool struct {
	svc *Service
}

// NewTool creates a credentials tool backed by the given service.
func NewTool(svc *Service) *Tool {
	return &Tool{svc: svc}
}

// CredentialsDefinition returns the tool definition without requiring a live service.
func CredentialsDefinition() tools.Definition {
	return tools.Definition{
		Name:        "credentials",
		Description: "Manage credentials: check OAuth provider availability, start/poll OAuth device flows, list/delete vault secrets, and get instructions for storing manual secrets securely. Use add_secret when you need to ask the user to store an API key or token.",
		InputSchema: credentialsInputSchema,
	}
}

// Definition implements tools.Tool.
func (t *Tool) Definition() tools.Definition {
	return CredentialsDefinition()
}

// Execute implements tools.Tool.
func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action, _ := args["action"].(string)
	userID := memory.UserIDFromContext(ctx)
	if userID == 0 {
		return "", fmt.Errorf("credentials tool requires user context")
	}

	switch action {
	case "status":
		return t.status(ctx, userID)
	case "list":
		return t.list(ctx, userID)
	case "delete":
		return t.delete(ctx, userID, args)
	case "oauth_start":
		return t.oauthStart(ctx, userID, args)
	case "oauth_poll":
		return t.oauthPoll(ctx, userID, args)
	case "oauth_disconnect":
		return t.oauthDisconnect(ctx, userID, args)
	case "add_secret":
		return t.addSecret(args)
	default:
		return "", fmt.Errorf("unknown action %q; expected status/list/delete/oauth_start/oauth_poll/oauth_disconnect/add_secret", action)
	}
}

func (t *Tool) status(ctx context.Context, userID int64) (string, error) {
	providers := t.svc.GetProviderStatuses(ctx, userID)
	entries, _ := t.svc.ListVault(ctx, userID)

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

	b.WriteString("\nVault secrets:\n")
	if len(entries) == 0 {
		b.WriteString("  (none)\n")
	} else {
		for _, e := range entries {
			fmt.Fprintf(&b, "  %s (updated %s)\n", e.Name, e.UpdatedAt)
		}
	}
	return b.String(), nil
}

func (t *Tool) list(ctx context.Context, userID int64) (string, error) {
	entries, err := t.svc.ListVault(ctx, userID)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "No secrets stored.", nil
	}
	var b strings.Builder
	b.WriteString("Stored secrets:\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "  %s (updated %s)\n", e.Name, e.UpdatedAt)
	}
	return b.String(), nil
}

func (t *Tool) delete(ctx context.Context, userID int64, args map[string]any) (string, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return "", fmt.Errorf("name is required for delete action")
	}
	if err := t.svc.DeleteVaultEntry(ctx, userID, name); err != nil {
		return "", err
	}
	return fmt.Sprintf("Secret %s removed.", name), nil
}

func (t *Tool) oauthStart(ctx context.Context, userID int64, args map[string]any) (string, error) {
	provider, _ := args["provider"].(string)
	if provider == "" {
		return "", fmt.Errorf("provider is required for oauth_start action")
	}
	status, err := t.svc.StartFlow(ctx, userID, provider)
	if err != nil {
		return "", err
	}
	out, _ := json.MarshalIndent(status, "", "  ")
	return fmt.Sprintf("OAuth flow started. Visit the URL and enter the code:\n%s\n\nCall oauth_poll with flow_id=%q to check progress.", out, status.FlowID), nil
}

func (t *Tool) oauthPoll(ctx context.Context, userID int64, args map[string]any) (string, error) {
	provider, _ := args["provider"].(string)
	flowID, _ := args["flow_id"].(string)
	if provider == "" {
		return "", fmt.Errorf("provider is required for oauth_poll action")
	}
	if flowID == "" {
		return "", fmt.Errorf("flow_id is required for oauth_poll action")
	}

	status, completed, err := t.svc.PollFlow(ctx, userID, provider, flowID)
	if err != nil {
		return "", err
	}
	if completed {
		return fmt.Sprintf("%s OAuth completed successfully. Your credentials are stored. Continue with the original task.", provider), nil
	}
	out, _ := json.MarshalIndent(status, "", "  ")
	return fmt.Sprintf("Flow status:\n%s\n\nKeep polling with the same flow_id until state is 'authorized'.", out), nil
}

func (t *Tool) oauthDisconnect(ctx context.Context, userID int64, args map[string]any) (string, error) {
	provider, _ := args["provider"].(string)
	if provider == "" {
		return "", fmt.Errorf("provider is required for oauth_disconnect action")
	}
	if err := t.svc.Disconnect(ctx, userID, provider); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s credentials removed.", provider), nil
}

func (t *Tool) addSecret(args map[string]any) (string, error) {
	name, _ := args["name"].(string)
	purpose, _ := args["purpose"].(string)
	if name == "" {
		return "", fmt.Errorf("name is required for add_secret action")
	}
	inst := t.svc.AddSecretInstruction(name, purpose)
	var b strings.Builder
	fmt.Fprintf(&b, "To store %s securely, please send the following command in this chat:\n\n", inst.Name)
	fmt.Fprintf(&b, "  %s\n\n", inst.Command)
	if inst.Purpose != "" {
		fmt.Fprintf(&b, "This secret is needed for: %s\n", inst.Purpose)
	}
	b.WriteString("\nReplace <value> with the actual secret. The value will be stored encrypted and will never appear in conversation history.")
	return b.String(), nil
}
