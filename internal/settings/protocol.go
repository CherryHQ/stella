package settings

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/library"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/skills"
	"github.com/CherryHQ/stella/pkg/tools"
)

const mutationTokenTTL = 2 * time.Minute

var operationContracts = map[string]map[string]map[string]any{
	"agents": {
		"list":   {"required": []string{}, "optional": []string{"page_size", "page_token"}, "constraints": []string{"page_size is 1..100; page_token is the opaque token returned by the previous page"}},
		"get":    {"required": []string{"id"}, "optional": []string{}, "constraints": []string{"id must identify an Agent readable by the current Authority"}},
		"create": {"required": []string{"name"}, "optional": []string{"id", "model", "model_thinking", "model_strong", "model_strong_thinking", "model_fast", "model_fast_thinking", "system_prompt", "soul", "scope", "enabled"}, "constraints": []string{"scope is restricted for ordinary users and may be system only for admin Authority; model fields use provider/model; thinking fields are minimal, low, medium, high, or xhigh"}},
		"update": {"required": []string{"id"}, "optional": []string{"name", "model", "model_thinking", "model_strong", "model_strong_thinking", "model_fast", "model_fast_thinking", "system_prompt", "soul", "scope", "enabled", "expected_digest"}, "constraints": []string{"ordinary users may update only their own restricted Agent; expected_digest is checked against the current row at confirm"}},
		"delete": {"required": []string{"id"}, "optional": []string{"expected_digest"}, "constraints": []string{"ordinary users may delete only their own restricted Agent; expected_digest is checked against the current row at confirm"}},
	},
	"library": {
		"create": {"required": []string{"scope", "file_name", "content"}, "optional": []string{"agent_id"}, "constraints": []string{"scope is user, user_agent, system, or system_agent; agent_id is required only for agent-bound scopes; content is bounded text"}},
		"delete": {"required": []string{"id"}, "optional": []string{"expected_digest"}, "constraints": []string{"the current Authority must own the file; expected_digest is the raw snapshot SHA-256"}},
	},
	"skills": {
		"create": {"required": []string{"scope", "name", "body or files"}, "optional": []string{"agent_id", "description", "disable_model_invocation", "files"}, "constraints": []string{"scope is user, user_agent, system, or system_agent; agent_id is required only for agent-bound scopes; files must include SKILL.md when supplied"}},
		"update": {"required": []string{"id", "expected_digest"}, "optional": []string{"description", "disable_model_invocation", "files", "delete_files"}, "constraints": []string{"expected_digest must equal the current Skill content digest; the current Authority must own the Skill"}},
		"delete": {"required": []string{"id", "expected_digest"}, "optional": []string{}, "constraints": []string{"expected_digest must equal the current Skill content digest; the current Authority must own the Skill"}},
	},
	"tool_overrides": {
		"set":   {"required": []string{"tool_name", "scope", "enabled"}, "optional": []string{"agent_id"}, "constraints": []string{"scope is user, user_agent, system, or system_agent; agent_id is required only for agent-bound scopes; core and unmanaged tools are rejected"}},
		"clear": {"required": []string{"tool_name", "scope"}, "optional": []string{"agent_id"}, "constraints": []string{"scope is user, user_agent, system, or system_agent; agent_id is required only for agent-bound scopes; core and unmanaged tools are rejected"}},
	},
}

type pendingMutation struct {
	userID    string
	sessionID string
	agentID   string
	resource  string
	operation string
	input     json.RawMessage
	digest    string
	expiresAt time.Time
}

func (t *Tool) describeResource(ctx context.Context, args map[string]any) (string, error) {
	if err := rejectUnexpected(args, "action", "resource"); err != nil {
		return "", err
	}
	resource, err := stringArg(args, "resource", true)
	if err != nil {
		return "", err
	}
	contracts, ok := operationContracts[resource]
	if !ok {
		return "", fmt.Errorf("unsupported settings resource %q", resource)
	}
	operations := make([]string, 0, len(contracts))
	for operation := range contracts {
		operations = append(operations, operation)
	}
	sort.Strings(operations)
	return tools.MarshalResult(map[string]any{
		"resource":            resource,
		"operations":          operations,
		"operation_contracts": contracts,
		"protocol":            "preview then confirm; confirmation is model-side and is not human approval",
	})
}

func (t *Tool) previewMutation(ctx context.Context, args map[string]any) (string, error) {
	if err := rejectUnexpected(args, "action", "resource", "operation", "input"); err != nil {
		return "", err
	}
	resource, err := stringArg(args, "resource", true)
	if err != nil {
		return "", err
	}
	operation, err := stringArg(args, "operation", true)
	if err != nil {
		return "", err
	}
	input, ok := args["input"].(map[string]any)
	if !ok {
		return "", errors.New("input must be an object")
	}
	authority, ok := authz.AuthorityFromContext(ctx)
	if !ok {
		return "", ErrUnavailable
	}
	if err := authorizeMutationScope(authority, input); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	var digest string
	switch resource {
	case "agents":
		digest, err = t.previewAgent(ctx, operation, input)
	case "library":
		digest, err = t.previewLibrary(ctx, operation, input)
	case "skills":
		digest, err = t.previewSkill(ctx, operation, input)
	case "tool_overrides":
		digest, err = t.previewOverride(ctx, operation, input)
	default:
		err = fmt.Errorf("unsupported settings resource %q", resource)
	}
	if err != nil {
		return "", err
	}
	token, err := newMutationToken()
	if err != nil {
		return "", err
	}
	t.tokensMu.Lock()
	if t.tokens == nil {
		t.tokens = make(map[string]pendingMutation)
	}
	now := time.Now().UTC()
	for key, pending := range t.tokens {
		if !pending.expiresAt.After(now) {
			delete(t.tokens, key)
		}
	}
	if len(t.tokens) >= 256 {
		t.tokensMu.Unlock()
		return "", errors.New("too many pending settings confirmations")
	}
	t.tokens[token] = pendingMutation{
		userID: authz.UserIDFromContext(ctx), sessionID: currentSessionID(ctx), agentID: authz.AgentIDFromContext(ctx),
		resource: resource, operation: operation, input: encoded, digest: digest, expiresAt: now.Add(mutationTokenTTL),
	}
	t.tokensMu.Unlock()
	result := map[string]any{"confirmation_token": token, "resource": resource, "operation": operation, "expires_in_seconds": int(mutationTokenTTL / time.Second)}
	if digest != "" {
		result["expected_digest"] = digest
	}
	return tools.MarshalResult(result)
}

func (t *Tool) confirmMutation(ctx context.Context, args map[string]any) (string, error) {
	if err := rejectUnexpected(args, "action", "token"); err != nil {
		return "", err
	}
	token, err := stringArg(args, "token", true)
	if err != nil {
		return "", err
	}
	t.tokensMu.Lock()
	pending, ok := t.tokens[token]
	if ok {
		delete(t.tokens, token)
	}
	t.tokensMu.Unlock()
	if !ok || !pending.expiresAt.After(time.Now().UTC()) {
		return "", errors.New("confirmation token is invalid or expired")
	}
	if pending.userID != authz.UserIDFromContext(ctx) || pending.sessionID != currentSessionID(ctx) || pending.agentID != authz.AgentIDFromContext(ctx) {
		return "", errors.New("confirmation token belongs to another turn")
	}
	var input map[string]any
	if err := json.Unmarshal(pending.input, &input); err != nil {
		return "", errors.New("confirmation token payload is invalid")
	}
	authority, ok := authz.AuthorityFromContext(ctx)
	if !ok {
		return "", ErrUnavailable
	}
	if err := authorizeMutationScope(authority, input); err != nil {
		return "", err
	}
	var applyErr error
	switch pending.resource {
	case "agents":
		applyErr = t.confirmAgent(ctx, pending.operation, input, pending.digest)
	case "library":
		applyErr = t.confirmLibrary(ctx, pending.operation, input, pending.digest)
	case "skills":
		applyErr = t.confirmSkill(ctx, pending.operation, input, pending.digest)
	case "tool_overrides":
		applyErr = t.confirmOverride(ctx, pending.operation, input, pending.digest)
	default:
		applyErr = fmt.Errorf("unsupported settings resource %q", pending.resource)
	}
	if applyErr != nil {
		return "", applyErr
	}
	return tools.MarshalResult(map[string]any{"confirmed": true, "resource": pending.resource, "operation": pending.operation})
}

func currentSessionID(ctx context.Context) string { return memory.SessionIDFromContext(ctx) }

func newMutationToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("create confirmation token: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func digestValue(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func agentDigest(a config.Agent) string { return digestValue(a) }

func authorizeMutationScope(authority authz.Authority, input map[string]any) error {
	scope, _ := input["scope"].(string)
	if !authority.IsAdmin() && (scope == config.AgentScopeSystem || scope == "system_agent") {
		return authz.ErrForbidden
	}
	return nil
}

func authorizeAgentScope(authority authz.Authority, requested string, current *config.Agent) error {
	if authority.IsAdmin() {
		return nil
	}
	if current != nil && current.Scope == config.AgentScopeSystem {
		return authz.ErrForbidden
	}
	if requested == config.AgentScopeSystem {
		return authz.ErrForbidden
	}
	if requested != "" && requested != config.AgentScopeRestricted {
		return fmt.Errorf("invalid agent scope %q", requested)
	}
	return nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func validateAgentCandidate(a config.Agent) error {
	if strings.TrimSpace(a.Name) == "" {
		return errors.New("agent name is required")
	}
	if a.Scope != "" && a.Scope != config.AgentScopeSystem && a.Scope != config.AgentScopeRestricted {
		return fmt.Errorf("invalid agent scope %q", a.Scope)
	}
	for field, value := range map[string]string{"model": a.Model, "model_strong": a.ModelStrong, "model_fast": a.ModelFast} {
		if strings.TrimSpace(value) == "" {
			continue
		}
		provider, model := config.ParseModelRef(value)
		if strings.TrimSpace(provider) == "" || strings.TrimSpace(model) == "" {
			return fmt.Errorf("invalid %s %q: expected provider/model", field, value)
		}
	}
	for field, value := range map[string]string{"model_thinking": a.ModelThinking, "model_strong_thinking": a.ModelStrongThinking, "model_fast_thinking": a.ModelFastThinking} {
		switch value {
		case "", "minimal", "low", "medium", "high", "xhigh":
		default:
			return fmt.Errorf("invalid %s %q", field, value)
		}
	}
	return nil
}

func (t *Tool) previewAgent(ctx context.Context, operation string, input map[string]any) (string, error) {
	if t.agentMutations == nil {
		return "", ErrUnavailable
	}
	var in agentWireInput
	if err := decodeObject(input, &in); err != nil {
		return "", err
	}
	authority, ok := authz.AuthorityFromContext(ctx)
	if !ok {
		return "", ErrUnavailable
	}
	switch operation {
	case "create":
		candidate := in.createCandidate()
		if err := authorizeAgentScope(authority, candidate.Scope, nil); err != nil {
			return "", err
		}
		if err := validateAgentCandidate(candidate); err != nil {
			return "", err
		}
		if canCreate, ok := t.agents.(interface {
			CanCreate(context.Context, authz.Authority) error
		}); ok {
			if err := canCreate.CanCreate(ctx, authority); err != nil {
				return "", err
			}
		}
		return "", nil
	case "update", "delete":
		if in.ID == "" {
			return "", errors.New("agent id is required")
		}
		if t.agents == nil {
			return "", ErrUnavailable
		}
		current, err := t.agents.Read(ctx, authority, in.ID)
		if err != nil {
			return "", err
		}
		if err := authorizeAgentScope(authority, stringValue(in.Scope), &current); err != nil {
			return "", err
		}
		digest := agentDigest(current)
		if in.ExpectedDigest != "" && in.ExpectedDigest != digest {
			return "", errors.New("agent changed since it was inspected")
		}
		if operation == "update" {
			if !in.hasPatch() {
				return "", errors.New("agent update has no fields")
			}
			in.apply(&current)
			if err := validateAgentCandidate(current); err != nil {
				return "", err
			}
		}
		return digest, nil
	default:
		return "", fmt.Errorf("unsupported agents operation %q", operation)
	}
}

func (t *Tool) confirmAgent(ctx context.Context, operation string, input map[string]any, expected string) error {
	if t.agentMutations == nil {
		return ErrUnavailable
	}
	var in agentWireInput
	if err := decodeObject(input, &in); err != nil {
		return err
	}
	authority, ok := authz.AuthorityFromContext(ctx)
	if !ok {
		return ErrUnavailable
	}
	switch operation {
	case "create":
		candidate := in.createCandidate()
		if err := authorizeAgentScope(authority, candidate.Scope, nil); err != nil {
			return err
		}
		_, err := t.agentMutations.Create(ctx, authority, candidate)
		return err
	case "update":
		expectedAgent, err := t.agents.Read(ctx, authority, in.ID)
		if err != nil {
			return err
		}
		if err := authorizeAgentScope(authority, stringValue(in.Scope), &expectedAgent); err != nil {
			return err
		}
		if agentDigest(expectedAgent) != expected {
			return errors.New("agent changed since preview")
		}
		current := expectedAgent
		in.apply(&current)
		atomic, ok := t.agentMutations.(interface {
			UpdateIfUnchanged(context.Context, authz.Authority, config.Agent, config.Agent) (config.Agent, error)
		})
		if !ok {
			return ErrUnavailable
		}
		_, err = atomic.UpdateIfUnchanged(ctx, authority, expectedAgent, current)
		return err
	case "delete":
		current, err := t.agents.Read(ctx, authority, in.ID)
		if err != nil {
			return err
		}
		if err := authorizeAgentScope(authority, "", &current); err != nil {
			return err
		}
		if agentDigest(current) != expected {
			return errors.New("agent changed since preview")
		}
		atomic, ok := t.agentMutations.(interface {
			DeleteIfUnchanged(context.Context, authz.Authority, config.Agent) error
		})
		if !ok {
			return ErrUnavailable
		}
		return atomic.DeleteIfUnchanged(ctx, authority, current)
	default:
		return fmt.Errorf("unsupported agents operation %q", operation)
	}
}

type agentWireInput struct {
	ID                  string  `json:"id"`
	ExpectedDigest      string  `json:"expected_digest"`
	Name                *string `json:"name"`
	Model               *string `json:"model"`
	SystemPrompt        *string `json:"system_prompt"`
	Soul                *string `json:"soul"`
	Scope               *string `json:"scope"`
	Enabled             *bool   `json:"enabled"`
	ModelThinking       *string `json:"model_thinking"`
	ModelStrong         *string `json:"model_strong"`
	ModelStrongThinking *string `json:"model_strong_thinking"`
	ModelFast           *string `json:"model_fast"`
	ModelFastThinking   *string `json:"model_fast_thinking"`
}

func (in agentWireInput) hasPatch() bool {
	return in.Name != nil || in.Model != nil || in.SystemPrompt != nil || in.Soul != nil || in.Scope != nil || in.Enabled != nil || in.ModelThinking != nil || in.ModelStrong != nil || in.ModelStrongThinking != nil || in.ModelFast != nil || in.ModelFastThinking != nil
}

func (in agentWireInput) createCandidate() config.Agent {
	a := config.Agent{}
	in.apply(&a)
	if a.ID == "" {
		a.ID = slug(in.Name)
	}
	return a
}

func (in agentWireInput) apply(a *config.Agent) {
	if in.Name != nil {
		a.Name = *in.Name
	}
	if in.Model != nil {
		a.Model = *in.Model
	}
	if in.SystemPrompt != nil {
		a.SystemPrompt = *in.SystemPrompt
	}
	if in.Soul != nil {
		a.Soul = *in.Soul
	}
	if in.Scope != nil {
		a.Scope = *in.Scope
	}
	if in.Enabled != nil {
		a.Enabled = *in.Enabled
	}
	if in.ModelThinking != nil {
		a.ModelThinking = *in.ModelThinking
	}
	if in.ModelStrong != nil {
		a.ModelStrong = *in.ModelStrong
	}
	if in.ModelStrongThinking != nil {
		a.ModelStrongThinking = *in.ModelStrongThinking
	}
	if in.ModelFast != nil {
		a.ModelFast = *in.ModelFast
	}
	if in.ModelFastThinking != nil {
		a.ModelFastThinking = *in.ModelFastThinking
	}
}

func slug(value *string) string {
	if value == nil {
		return "agent"
	}
	s := strings.ToLower(strings.TrimSpace(*value))
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if b.Len() > 0 {
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "agent"
	}
	return strings.Trim(b.String(), "-")
}

func (t *Tool) previewLibrary(ctx context.Context, operation string, input map[string]any) (string, error) {
	if t.libraryMutations == nil {
		return "", ErrUnavailable
	}
	var in libraryWireInput
	if err := decodeObject(input, &in); err != nil {
		return "", err
	}
	authority, ok := authz.AuthorityFromContext(ctx)
	if !ok {
		return "", ErrUnavailable
	}
	switch operation {
	case "create":
		if in.FileName == "" || in.Content == "" {
			return "", errors.New("library file_name and content are required")
		}
		if len(in.Content) > library.MaxFileBytes {
			return "", library.ErrFileTooLarge
		}
		_, err := t.libraryMutations.ResolveManageOwner(ctx, authority, library.Scope(in.Scope), in.AgentID)
		return "", err
	case "delete":
		if in.ID == "" {
			return "", errors.New("library id is required")
		}
		file, err := t.libraryMutations.GetManaged(ctx, authority, in.ID)
		if err != nil {
			return "", err
		}
		d := hex.EncodeToString(file.RawSHA256)
		if in.ExpectedDigest != "" && in.ExpectedDigest != d {
			return "", library.ErrGenerationChanged
		}
		return d, nil
	default:
		return "", fmt.Errorf("unsupported library operation %q", operation)
	}
}

type libraryWireInput struct {
	ID             string `json:"id"`
	Scope          string `json:"scope"`
	AgentID        string `json:"agent_id"`
	FileName       string `json:"file_name"`
	Content        string `json:"content"`
	ExpectedDigest string `json:"expected_digest"`
}

func (t *Tool) confirmLibrary(ctx context.Context, operation string, input map[string]any, expected string) error {
	var in libraryWireInput
	if err := decodeObject(input, &in); err != nil {
		return err
	}
	a, ok := authz.AuthorityFromContext(ctx)
	if !ok {
		return ErrUnavailable
	}
	switch operation {
	case "create":
		_, err := t.libraryMutations.CreateManagedUpload(ctx, a, library.Scope(in.Scope), in.AgentID, in.FileName, strings.NewReader(in.Content))
		return err
	case "delete":
		return t.libraryMutations.DeleteManagedWithDigest(ctx, a, in.ID, expected)
	default:
		return fmt.Errorf("unsupported library operation %q", operation)
	}
}

func (t *Tool) previewSkill(ctx context.Context, operation string, input map[string]any) (string, error) {
	if t.skillMutations == nil {
		return "", ErrUnavailable
	}
	var in skillWireInput
	if err := decodeObject(input, &in); err != nil {
		return "", err
	}
	a, ok := authz.AuthorityFromContext(ctx)
	if !ok {
		return "", ErrUnavailable
	}
	switch operation {
	case "create":
		if err := ensureSkillInput(in.create()); err != nil {
			return "", err
		}
		return "", t.skillMutations.PreviewCreate(ctx, a, in.Scope, in.AgentID)
	case "update":
		if in.ID == "" {
			return "", errors.New("skill id is required")
		}
		sk, err := t.skillMutations.PreviewExisting(ctx, a, in.ID, authz.ActionWrite)
		if err != nil {
			return "", err
		}
		if in.ExpectedDigest == "" || in.ExpectedDigest != sk.ContentDigest {
			return "", skills.ErrSkillDigestConflict
		}
		return sk.ContentDigest, nil
	case "delete":
		if in.ID == "" {
			return "", errors.New("skill id is required")
		}
		sk, err := t.skillMutations.PreviewExisting(ctx, a, in.ID, authz.ActionDelete)
		if err != nil {
			return "", err
		}
		if in.ExpectedDigest != "" && in.ExpectedDigest != sk.ContentDigest {
			return "", skills.ErrSkillDigestConflict
		}
		return sk.ContentDigest, nil
	default:
		return "", fmt.Errorf("unsupported skills operation %q", operation)
	}
}

type skillWireInput struct {
	ID                     string            `json:"id"`
	Scope                  string            `json:"scope"`
	AgentID                string            `json:"agent_id"`
	Name                   string            `json:"name"`
	Description            *string           `json:"description"`
	Body                   string            `json:"body"`
	Files                  map[string]string `json:"files"`
	DeleteFiles            []string          `json:"delete_files"`
	DisableModelInvocation *bool             `json:"disable_model_invocation"`
	ExpectedDigest         string            `json:"expected_digest"`
}

func (in skillWireInput) create() skillCreateRequest {
	var description string
	if in.Description != nil {
		description = *in.Description
	}
	var disable bool
	if in.DisableModelInvocation != nil {
		disable = *in.DisableModelInvocation
	}
	return skillCreateRequest{Scope: in.Scope, AgentID: in.AgentID, Name: in.Name, Description: description, Body: in.Body, Files: in.Files, DisableModelInvocation: disable}
}

func (in skillWireInput) update() skillUpdateRequest {
	return skillUpdateRequest{ID: in.ID, ExpectedDigest: in.ExpectedDigest, Description: in.Description, DisableModelInvocation: in.DisableModelInvocation, Files: in.Files, DeleteFiles: in.DeleteFiles}
}

func (t *Tool) confirmSkill(ctx context.Context, operation string, input map[string]any, expected string) error {
	var in skillWireInput
	if err := decodeObject(input, &in); err != nil {
		return err
	}
	a, ok := authz.AuthorityFromContext(ctx)
	if !ok {
		return ErrUnavailable
	}
	switch operation {
	case "create":
		return func() error {
			req := in.create()
			returnErr := ensureSkillInput(req)
			if returnErr != nil {
				return returnErr
			}
			_, err := t.skillMutations.Create(ctx, a, req)
			return err
		}()
	case "update":
		req := in.update()
		req.ExpectedDigest = expected
		_, err := t.skillMutations.Update(ctx, a, req)
		return err
	case "delete":
		return t.skillMutations.Delete(ctx, a, skillDeleteRequest{ID: in.ID, ExpectedDigest: expected})
	default:
		return fmt.Errorf("unsupported skills operation %q", operation)
	}
}

func (t *Tool) previewOverride(ctx context.Context, operation string, input map[string]any) (string, error) {
	if t.toolOverrideMutations == nil {
		return "", ErrUnavailable
	}
	var in toolOverrideWireInput
	if err := decodeObject(input, &in); err != nil {
		return "", err
	}
	if operation != "set" && operation != "clear" {
		return "", fmt.Errorf("unsupported tool_overrides operation %q", operation)
	}
	if operation == "set" && in.Enabled == nil {
		return "", errors.New("tool_overrides.set requires enabled")
	}
	return t.toolOverrideMutations.Preview(ctx, mustAuthority(ctx), in.request())
}

type toolOverrideWireInput struct {
	ToolName string `json:"tool_name"`
	Scope    string `json:"scope"`
	AgentID  string `json:"agent_id"`
	Enabled  *bool  `json:"enabled"`
}

func (in toolOverrideWireInput) request() toolOverrideRequest {
	return toolOverrideRequest{ToolName: in.ToolName, Scope: in.Scope, AgentID: in.AgentID, Enabled: in.Enabled != nil && *in.Enabled}
}

func (t *Tool) confirmOverride(ctx context.Context, operation string, input map[string]any, expected string) error {
	var in toolOverrideWireInput
	if err := decodeObject(input, &in); err != nil {
		return err
	}
	a, ok := authz.AuthorityFromContext(ctx)
	if !ok {
		return ErrUnavailable
	}
	switch operation {
	case "set":
		return t.toolOverrideMutations.Set(ctx, a, in.request(), expected)
	case "clear":
		return t.toolOverrideMutations.Clear(ctx, a, in.request(), expected)
	default:
		return fmt.Errorf("unsupported tool_overrides operation %q", operation)
	}
}

func mustAuthority(ctx context.Context) authz.Authority {
	a, _ := authz.AuthorityFromContext(ctx)
	return a
}
