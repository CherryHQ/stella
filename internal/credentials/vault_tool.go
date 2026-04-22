package credentials

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vaayne/anna/pkg/memory"
	"github.com/vaayne/anna/pkg/tools"
)

var vaultInputSchema = func() map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["list", "remove", "add"],
      "description": "list=show all stored secrets; remove=delete a secret by name (requires name); add=get instructions to store a manual API key or token (requires name, purpose optional)"
    },
    "name": {
      "type": "string",
      "description": "Secret key name in uppercase (required for delete and add_secret)"
    },
    "purpose": {
      "type": "string",
      "description": "Why this secret is needed (optional; included in the instructions shown to the user)"
    }
  },
  "required": ["action"]
}`), &m)
	return m
}()

// VaultTool manages stored key-value secrets.
type VaultTool struct {
	svc *Service
}

// NewVaultTool creates a VaultTool backed by the given service.
func NewVaultTool(svc *Service) *VaultTool {
	return &VaultTool{svc: svc}
}

// VaultDefinition returns the tool definition without requiring a live service.
func VaultDefinition() tools.Definition {
	return tools.Definition{
		Name:        "vault",
		Description: "Manage stored secrets (API keys, tokens). List what is stored, delete a secret by name, or get instructions for the user to store a new secret securely. Use add_secret when you need an API key that the user must provide.",
		InputSchema: vaultInputSchema,
	}
}

// Definition implements tools.Tool.
func (t *VaultTool) Definition() tools.Definition {
	return VaultDefinition()
}

// Execute implements tools.Tool.
func (t *VaultTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action, _ := args["action"].(string)
	userID := memory.UserIDFromContext(ctx)
	if userID == 0 {
		return "", fmt.Errorf("vault tool requires user context")
	}
	switch action {
	case "list":
		return t.list(ctx, userID)
	case "remove":
		return t.delete(ctx, userID, args)
	case "add":
		return t.addSecret(args)
	default:
		return "", fmt.Errorf("unknown action %q; expected list/remove/add", action)
	}
}

func (t *VaultTool) list(ctx context.Context, userID int64) (string, error) {
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

func (t *VaultTool) delete(ctx context.Context, userID int64, args map[string]any) (string, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return "", fmt.Errorf("name is required for delete action")
	}
	if err := t.svc.DeleteVaultEntry(ctx, userID, name); err != nil {
		return "", err
	}
	return fmt.Sprintf("Secret %s removed.", name), nil
}

func (t *VaultTool) addSecret(args map[string]any) (string, error) {
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
