package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/tools"
)

const (
	statusDraft      = "draft"
	statusActive     = "active"
	statusDeprecated = "deprecated"
)

type Store interface {
	pkgplugins.KnowledgeWriter
	pkgplugins.KnowledgeStore
}

type Tool struct {
	store Store
}

func NewTool(store Store) *Tool {
	return &Tool{store: store}
}

func (t *Tool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "knowledge",
		Description: "Manage fact/context knowledge that appears in the dedicated Knowledge section of the system prompt. Use create to save draft fact/context knowledge, patch to activate or update it, and deprecate to retire it.",
		InputSchema: knowledgeInputSchema,
	}
}

var knowledgeInputSchema = func() map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["create", "patch", "deprecate"],
      "description": "Action to perform: create saves a draft knowledge entry, patch updates content/status, deprecate retires an entry"
    },
    "kind": {
      "type": "string",
      "enum": ["fact", "context"],
      "description": "fact is durable project/domain knowledge; context is time-bound background information"
    },
    "scope": {
      "type": "string",
      "enum": ["user", "agent"],
      "description": "Writable scope. Defaults to user. Use agent to bind the entry to the current user-agent pair."
    },
    "name": {
      "type": "string",
      "description": "Stable knowledge title/name"
    },
    "description": {
      "type": "string",
      "description": "Short optional description"
    },
    "content": {
      "type": "string",
      "description": "Knowledge body text"
    },
    "status": {
      "type": "string",
      "enum": ["draft", "active", "deprecated"],
      "description": "Knowledge status. Active entries affect normal sessions."
    },
    "evidence": {
      "type": "string",
      "description": "Optional evidence text or transcript quote supporting the entry"
    },
    "confidence": {
      "type": "number",
      "description": "Optional confidence from 0.0 to 1.0"
    },
    "expires_at": {
      "type": "string",
      "description": "Optional RFC3339 expiration timestamp for time-bound context"
    }
  },
  "required": ["action"]
}`), &m)
	return m
}()

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action, _ := args["action"].(string)
	switch action {
	case "create":
		return t.create(ctx, args)
	case "patch":
		return t.patch(ctx, args)
	case "deprecate":
		return t.deprecate(ctx, args)
	default:
		return "", fmt.Errorf("unknown action %q, expected create/patch/deprecate", action)
	}
}

func (t *Tool) create(ctx context.Context, args map[string]any) (string, error) {
	if t.store == nil {
		return "", fmt.Errorf("knowledge store unavailable")
	}
	name, kind, err := nameAndKind(args)
	if err != nil {
		return "", err
	}
	description, _ := args["description"].(string)
	content, _ := args["content"].(string)
	if content == "" {
		return "", fmt.Errorf("content is required for create action")
	}
	scope, userID, agentID, err := targetOwner(ctx, args)
	if err != nil {
		return "", err
	}
	status := statusDraft
	if v, ok := args["status"].(string); ok && v != "" {
		status, err = normalizeStatus(v)
		if err != nil {
			return "", err
		}
	}
	evidence, err := evidenceJSON(args)
	if err != nil {
		return "", err
	}
	confidence, err := confidenceArg(args)
	if err != nil {
		return "", err
	}
	expiresAt, err := expiresAtArg(args)
	if err != nil {
		return "", err
	}

	// Keep tool-created metadata generic; kind is a first-class column now.
	meta := json.RawMessage(fmt.Sprintf(`{"created-at":%q}`, time.Now().UTC().Format(time.RFC3339)))
	if _, err := t.store.CreateKnowledge(ctx, pkgplugins.KnowledgeCreateParams{
		Name:          name,
		Description:   description,
		Content:       content,
		KnowledgeType: kind,
		Scope:         scope,
		UserID:        userID,
		AgentID:       agentID,
		Status:        status,
		Evidence:      evidence,
		Confidence:    confidence,
		ExpiresAt:     expiresAt,
		Metadata:      meta,
	}); err != nil {
		return "", fmt.Errorf("create knowledge %q: %w", name, err)
	}
	return fmt.Sprintf("Knowledge %s %q created as %s (scope=%s).", kind, name, status, scope), nil
}

func (t *Tool) patch(ctx context.Context, args map[string]any) (string, error) {
	name, kind, err := nameAndKind(args)
	if err != nil {
		return "", err
	}
	entry, err := t.resolve(ctx, args, name, kind)
	if err != nil {
		return "", err
	}

	params := pkgplugins.KnowledgeUpdateParams{
		ID:          entry.ID,
		Name:        entry.Name,
		Description: entry.Description,
		Content:     entry.Content,
		Status:      entry.Status,
		Evidence:    entry.Evidence,
		Confidence:  entry.Confidence,
		ExpiresAt:   entry.ExpiresAt,
		Supersedes:  entry.Supersedes,
		Metadata:    entry.Metadata,
	}
	if v, ok := args["description"].(string); ok && v != "" {
		params.Description = v
	}
	if v, ok := args["content"].(string); ok && v != "" {
		params.Content = v
	}
	if v, ok := args["status"].(string); ok && v != "" {
		params.Status, err = normalizeStatus(v)
		if err != nil {
			return "", err
		}
	}
	if _, ok := args["evidence"]; ok {
		params.Evidence, err = evidenceJSON(args)
		if err != nil {
			return "", err
		}
	}
	if _, ok := args["confidence"]; ok {
		params.Confidence, err = confidenceArg(args)
		if err != nil {
			return "", err
		}
	}
	if _, ok := args["expires_at"]; ok {
		params.ExpiresAt, err = expiresAtArg(args)
		if err != nil {
			return "", err
		}
	}
	if _, err := t.store.UpdateKnowledge(ctx, params); err != nil {
		return "", fmt.Errorf("patch knowledge %q: %w", name, err)
	}
	return fmt.Sprintf("Knowledge %s %q updated.", kind, name), nil
}

func (t *Tool) deprecate(ctx context.Context, args map[string]any) (string, error) {
	name, kind, err := nameAndKind(args)
	if err != nil {
		return "", err
	}
	entry, err := t.resolve(ctx, args, name, kind)
	if err != nil {
		return "", err
	}
	if err := t.store.DeprecateKnowledge(ctx, entry.ID); err != nil {
		return "", fmt.Errorf("deprecate knowledge %q: %w", name, err)
	}
	return fmt.Sprintf("Knowledge %s %q deprecated.", kind, name), nil
}

func (t *Tool) resolve(ctx context.Context, args map[string]any, name string, kind pkgplugins.KnowledgeType) (*pkgplugins.KnowledgeEntry, error) {
	if t.store == nil {
		return nil, fmt.Errorf("knowledge store unavailable")
	}
	scope, userID, agentID, err := targetOwner(ctx, args)
	if err != nil {
		return nil, err
	}
	rows, err := t.store.ListKnowledgeByNameAndScope(ctx, name, scope, userID, agentID)
	if err != nil {
		return nil, err
	}
	var matches []pkgplugins.KnowledgeEntry
	for _, row := range rows {
		if row.KnowledgeType == kind {
			matches = append(matches, row)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("knowledge %s %q not found in scope=%s", kind, name, scope)
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("knowledge %s %q is ambiguous in scope=%s", kind, name, scope)
	}
	return &matches[0], nil
}

func nameAndKind(args map[string]any) (string, pkgplugins.KnowledgeType, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return "", "", fmt.Errorf("name is required")
	}
	kind, _ := args["kind"].(string)
	switch kind {
	case "fact":
		return name, pkgplugins.KnowledgeTypeFact, nil
	case "context":
		return name, pkgplugins.KnowledgeTypeContext, nil
	default:
		return "", "", fmt.Errorf("kind is required and must be fact or context")
	}
}

func targetOwner(ctx context.Context, args map[string]any) (scope string, userID string, agentID string, err error) {
	rawScope, _ := args["scope"].(string)
	switch filepath.Clean(rawScope) {
	case "", ".", "user":
		userID = memory.UserIDFromContext(ctx)
		if userID == "" {
			return "", "", "", fmt.Errorf("user knowledge scope is unavailable without a user context")
		}
		return "user", userID, "", nil
	case "agent", "user_agent":
		userID = memory.UserIDFromContext(ctx)
		agentID = memory.AgentIDFromContext(ctx)
		if userID == "" || agentID == "" {
			return "", "", "", fmt.Errorf("agent knowledge scope requires user and agent context")
		}
		return "user_agent", userID, agentID, nil
	default:
		return "", "", "", fmt.Errorf("invalid scope %q, expected user or agent", rawScope)
	}
}

func normalizeStatus(status string) (string, error) {
	switch status {
	case statusDraft, statusActive, statusDeprecated:
		return status, nil
	default:
		return "", fmt.Errorf("invalid status %q", status)
	}
}

func evidenceJSON(args map[string]any) (json.RawMessage, error) {
	evidence, _ := args["evidence"].(string)
	if evidence == "" {
		return json.RawMessage("[]"), nil
	}
	raw, err := json.Marshal([]map[string]string{{"text": evidence}})
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func confidenceArg(args map[string]any) (*float64, error) {
	v, ok := args["confidence"]
	if !ok {
		return nil, nil
	}
	f, ok := v.(float64)
	if !ok {
		return nil, fmt.Errorf("confidence must be a number")
	}
	if f < 0 || f > 1 {
		return nil, fmt.Errorf("confidence must be between 0 and 1")
	}
	return &f, nil
}

func expiresAtArg(args map[string]any) (*time.Time, error) {
	raw, _ := args["expires_at"].(string)
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf("expires_at must be RFC3339: %w", err)
	}
	u := t.UTC()
	return &u, nil
}
