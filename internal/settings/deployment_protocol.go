package settings

import (
	"context"
	"errors"
	"fmt"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/controlplane"
	"github.com/CherryHQ/stella/internal/mcp"
)

func (t *Tool) readResource(ctx context.Context, args map[string]any, action string) (string, error) {
	resource, _ := args["resource"].(string)
	if resource == "agents" {
		return t.readAgents(ctx, args, action)
	}
	authority, _ := authz.AuthorityFromContext(ctx)
	switch resource {
	case "providers", "embedding", "vision", "plugins":
		return t.readDeployment(ctx, authority, args, action)
	case "mcp":
		return t.readMCP(ctx, authority, args, action)
	default:
		return "", fmt.Errorf("unsupported settings resource %q", resource)
	}
}

func (t *Tool) readDeployment(ctx context.Context, authority authz.Authority, args map[string]any, action string) (string, error) {
	if t.deploymentMutations == nil {
		return "", ErrUnavailable
	}
	resource, _ := args["resource"].(string)
	allowed := []string{"action", "resource"}
	if action == "get" {
		allowed = append(allowed, "id")
	}
	if err := rejectUnexpected(args, allowed...); err != nil {
		return "", err
	}
	switch resource {
	case "providers":
		if action == "list" {
			rows, err := t.deploymentMutations.ListProviders(ctx, authority)
			if err != nil {
				return "", err
			}
			out := make([]map[string]any, 0, len(rows))
			for _, row := range rows {
				out = append(out, providerView(row))
			}
			return marshalAgentResult(action, map[string]any{"providers": out})
		}
		id, err := stringArg(args, "id", true)
		if err != nil {
			return "", err
		}
		row, err := t.deploymentMutations.GetProvider(ctx, authority, id)
		if err != nil {
			return "", err
		}
		return marshalAgentResult(action, providerView(row))
	case "embedding":
		if action != "get" {
			return "", fmt.Errorf("embedding supports only get")
		}
		row, err := t.deploymentMutations.GetEmbedding(ctx, authority)
		if err != nil {
			return "", err
		}
		return marshalAgentResult(action, embeddingView(row))
	case "vision":
		if action != "get" {
			return "", fmt.Errorf("vision supports only get")
		}
		row, err := t.deploymentMutations.GetVision(ctx, authority)
		if err != nil {
			return "", err
		}
		return marshalAgentResult(action, visionView(row))
	case "plugins":
		if action != "list" {
			return "", fmt.Errorf("plugins supports only list")
		}
		rows, err := t.deploymentMutations.ListPlugins(ctx, authority)
		if err != nil {
			return "", err
		}
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			out = append(out, pluginView(row))
		}
		return marshalAgentResult(action, map[string]any{"plugins": out})
	default:
		return "", fmt.Errorf("unsupported settings resource %q", resource)
	}
}

func (t *Tool) readMCP(ctx context.Context, authority authz.Authority, args map[string]any, action string) (string, error) {
	if t.mcpAccess == nil {
		return "", ErrUnavailable
	}
	allowed := []string{"action", "resource", "scope", "agent_id"}
	if action == "get" {
		allowed = append(allowed, "id")
	}
	if err := rejectUnexpected(args, allowed...); err != nil {
		return "", err
	}
	scope, err := stringArg(args, "scope", true)
	if err != nil {
		return "", err
	}
	agentID, err := stringArg(args, "agent_id", false)
	if err != nil {
		return "", err
	}
	access, err := t.mcpAccess.Begin(authority)
	if err != nil {
		return "", err
	}
	rows, err := access.List(ctx, scope, agentID)
	if err != nil {
		return "", err
	}
	if action == "get" {
		id, err := stringArg(args, "id", true)
		if err != nil {
			return "", err
		}
		row, ok := findRegistration(rows, id)
		if !ok {
			return "", authz.ErrNotFound
		}
		return marshalAgentResult(action, mcpView(row))
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, mcpView(row))
	}
	return marshalAgentResult(action, map[string]any{"servers": out})
}

type providerMutationInput struct {
	ID      string  `json:"id"`
	Type    string  `json:"type"`
	Name    *string `json:"name"`
	Enabled *bool   `json:"enabled"`
	BaseURL *string `json:"base_url"`
}

func (in providerMutationInput) candidate(existing *config.Provider) config.Provider {
	var p config.Provider
	if existing != nil {
		p = *existing
		if existing.Models != nil {
			p.Models = existing.Models
		}
	}
	if in.ID != "" {
		p.ID = in.ID
	}
	if in.Type != "" {
		p.Type = in.Type
	}
	if in.Name != nil {
		p.Name = *in.Name
	}
	if in.Enabled != nil {
		p.Enabled = *in.Enabled
	}
	if in.BaseURL != nil {
		p.BaseURL = *in.BaseURL
	}
	return p
}

func (t *Tool) previewDeployment(ctx context.Context, resource, operation string, input map[string]any) (string, error) {
	if t.deploymentMutations == nil {
		return "", ErrUnavailable
	}
	authority, ok := authz.AuthorityFromContext(ctx)
	if !ok {
		return "", ErrUnavailable
	}
	switch resource {
	case "providers":
		var in providerMutationInput
		if err := decodeObject(input, &in); err != nil {
			return "", err
		}
		switch operation {
		case "create":
			if in.ID == "" || in.Type == "" {
				return "", errors.New("providers.create requires id and type")
			}
			if in.BaseURL != nil && *in.BaseURL != "" {
				if err := validateSettingsURL(*in.BaseURL); err != nil {
					return "", err
				}
			}
			return "", nil
		case "update", "delete":
			if in.ID == "" {
				return "", errors.New("provider id is required")
			}
			current, err := t.deploymentMutations.GetProvider(ctx, authority, in.ID)
			if err != nil {
				return "", err
			}
			if operation == "update" && in.BaseURL != nil && *in.BaseURL != "" {
				if err := validateSettingsURL(*in.BaseURL); err != nil {
					return "", err
				}
			}
			return digestValue(providerView(current)), nil
		default:
			return "", fmt.Errorf("unsupported providers operation %q", operation)
		}
	case "embedding":
		if operation != "update" {
			return "", fmt.Errorf("unsupported embedding operation %q", operation)
		}
		var in embeddingMutationInput
		if err := decodeObject(input, &in); err != nil {
			return "", err
		}
		if in.BaseURL != nil && *in.BaseURL != "" {
			if err := validateSettingsURL(*in.BaseURL); err != nil {
				return "", err
			}
		}
		current, err := t.deploymentMutations.GetEmbedding(ctx, authority)
		if err != nil {
			return "", err
		}
		return digestValue(embeddingView(current)), nil
	case "vision":
		if operation != "update" {
			return "", fmt.Errorf("unsupported vision operation %q", operation)
		}
		var in visionMutationInput
		if err := decodeObject(input, &in); err != nil {
			return "", err
		}
		current, err := t.deploymentMutations.GetVision(ctx, authority)
		if err != nil {
			return "", err
		}
		return digestValue(visionView(current)), nil
	case "plugins":
		if operation != "enable" && operation != "disable" {
			return "", fmt.Errorf("unsupported plugins operation %q", operation)
		}
		var in pluginMutationInput
		if err := decodeObject(input, &in); err != nil {
			return "", err
		}
		if in.Kind == "" || in.Name == "" {
			return "", errors.New("plugins mutation requires kind and name")
		}
		rows, err := t.deploymentMutations.ListPlugins(ctx, authority)
		if err != nil {
			return "", err
		}
		if _, found := findPlugin(rows, in.Kind, in.Name); !found {
			return "", authz.ErrNotFound
		}
		return "", nil
	default:
		return "", fmt.Errorf("unsupported settings resource %q", resource)
	}
}

type embeddingMutationInput struct {
	Enabled   *bool   `json:"enabled"`
	Model     *string `json:"model"`
	Dim       *int    `json:"dim"`
	BaseURL   *string `json:"base_url"`
	Normalize *bool   `json:"normalize"`
}
type visionMutationInput struct {
	Model *string `json:"model"`
}
type pluginMutationInput struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

func (t *Tool) confirmDeployment(ctx context.Context, resource, operation string, input map[string]any, expected string) error {
	if t.deploymentMutations == nil {
		return ErrUnavailable
	}
	authority, ok := authz.AuthorityFromContext(ctx)
	if !ok {
		return ErrUnavailable
	}
	switch resource {
	case "providers":
		var in providerMutationInput
		if err := decodeObject(input, &in); err != nil {
			return err
		}
		switch operation {
		case "create":
			return t.deploymentMutations.CreateProvider(ctx, authority, in.candidate(nil))
		case "update":
			current, err := t.deploymentMutations.GetProvider(ctx, authority, in.ID)
			if err != nil {
				return err
			}
			if digestValue(providerView(current)) != expected {
				return errors.New("provider changed since preview")
			}
			_, err = t.deploymentMutations.UpdateProvider(ctx, authority, in.ID, in.candidate(&current))
			return err
		case "delete":
			return t.deploymentMutations.DeleteProvider(ctx, authority, in.ID)
		}
	case "embedding":
		var in embeddingMutationInput
		if err := decodeObject(input, &in); err != nil {
			return err
		}
		current, err := t.deploymentMutations.GetEmbedding(ctx, authority)
		if err != nil {
			return err
		}
		if digestValue(embeddingView(current)) != expected {
			return errors.New("embedding settings changed since preview")
		}
		upd := controlplane.EmbeddingUpdate{Enabled: current.Enabled, Model: current.Model, Dim: current.Dim, BaseURL: current.BaseURL, Normalize: current.Normalize}
		if in.Enabled != nil {
			upd.Enabled = *in.Enabled
		}
		if in.Model != nil {
			upd.Model = *in.Model
		}
		if in.Dim != nil {
			upd.Dim = *in.Dim
		}
		if in.BaseURL != nil {
			upd.BaseURL = *in.BaseURL
		}
		if in.Normalize != nil {
			upd.Normalize = *in.Normalize
		}
		_, err = t.deploymentMutations.SetEmbedding(ctx, authority, upd)
		return err
	case "vision":
		var in visionMutationInput
		if err := decodeObject(input, &in); err != nil {
			return err
		}
		current, err := t.deploymentMutations.GetVision(ctx, authority)
		if err != nil {
			return err
		}
		if digestValue(visionView(current)) != expected {
			return errors.New("vision settings changed since preview")
		}
		model := current.Model
		if in.Model != nil {
			model = *in.Model
		}
		_, err = t.deploymentMutations.SetVision(ctx, authority, config.VisionSettings{Model: model})
		return err
	case "plugins":
		var in pluginMutationInput
		if err := decodeObject(input, &in); err != nil {
			return err
		}
		_, err := t.deploymentMutations.TogglePlugin(ctx, authority, in.Kind, in.Name, operation == "enable")
		return err
	}
	return fmt.Errorf("unsupported %s operation %q", resource, operation)
}

func (t *Tool) previewMCP(ctx context.Context, operation string, input map[string]any) (string, error) {
	if t.mcpAccess == nil {
		return "", ErrUnavailable
	}
	authority, ok := authz.AuthorityFromContext(ctx)
	if !ok {
		return "", ErrUnavailable
	}
	var in mcpMutationInput
	if err := decodeObject(input, &in); err != nil {
		return "", err
	}
	access, err := t.mcpAccess.Begin(authority)
	if err != nil {
		return "", err
	}
	switch operation {
	case "create":
		if in.AuthType != "" && in.AuthType != mcp.AuthTypeNone {
			return "", errors.New("only no-auth MCP registrations are supported by the model tool")
		}
		if stringValue(in.URL) == "" || stringValue(in.Name) == "" || in.Scope == "" {
			return "", errors.New("mcp.create requires scope, name, and url")
		}
		if err := validateSettingsURL(stringValue(in.URL)); err != nil {
			return "", err
		}
		return "", nil
	case "update", "delete":
		if in.ID == "" || in.Scope == "" {
			return "", errors.New("mcp mutation requires id and scope")
		}
		rows, err := access.List(ctx, in.Scope, in.AgentID)
		if err != nil {
			return "", err
		}
		row, found := findRegistration(rows, in.ID)
		if !found {
			return "", authz.ErrNotFound
		}
		if in.URL != nil && *in.URL != "" {
			if err := validateSettingsURL(*in.URL); err != nil {
				return "", err
			}
		}
		return registrationDigest(row), nil
	default:
		return "", fmt.Errorf("unsupported mcp operation %q", operation)
	}
}

type mcpMutationInput struct {
	ID             string  `json:"id"`
	Scope          string  `json:"scope"`
	AgentID        string  `json:"agent_id"`
	Name           *string `json:"name"`
	URL            *string `json:"url"`
	Transport      *string `json:"transport"`
	Enabled        *bool   `json:"enabled"`
	AuthType       string  `json:"auth_type"`
	ExpectedDigest string  `json:"expected_digest"`
}

func (t *Tool) confirmMCP(ctx context.Context, operation string, input map[string]any, expected string) error {
	if t.mcpAccess == nil {
		return ErrUnavailable
	}
	authority, ok := authz.AuthorityFromContext(ctx)
	if !ok {
		return ErrUnavailable
	}
	var in mcpMutationInput
	if err := decodeObject(input, &in); err != nil {
		return err
	}
	access, err := t.mcpAccess.Begin(authority)
	if err != nil {
		return err
	}
	switch operation {
	case "create":
		if in.AuthType != "" && in.AuthType != mcp.AuthTypeNone {
			return errors.New("only no-auth MCP registrations are supported by the model tool")
		}
		r := mcp.CreateInput{Scope: in.Scope, AgentID: in.AgentID, Name: stringValue(in.Name), URL: stringValue(in.URL), Transport: stringValue(in.Transport), AuthType: mcp.AuthTypeNone}
		_, err := access.Create(ctx, r)
		return err
	case "update":
		rows, err := access.List(ctx, in.Scope, in.AgentID)
		if err != nil {
			return err
		}
		row, found := findRegistration(rows, in.ID)
		if !found {
			return authz.ErrNotFound
		}
		if registrationDigest(row) != expected {
			return errors.New("MCP registration changed since preview")
		}
		upd := mcp.UpdateInput{ID: in.ID, Scope: in.Scope, AgentID: in.AgentID, NewScope: &in.Scope, NewUserID: string(authority.UserID()), NewAgentID: in.AgentID, Name: in.Name, URL: in.URL, Transport: in.Transport, Enabled: in.Enabled}
		if in.AuthType != "" && in.AuthType != mcp.AuthTypeNone {
			return errors.New("only no-auth MCP registrations are supported by the model tool")
		}
		_, err = access.Update(ctx, upd)
		return err
	case "delete":
		rows, err := access.List(ctx, in.Scope, in.AgentID)
		if err != nil {
			return err
		}
		row, found := findRegistration(rows, in.ID)
		if !found {
			return authz.ErrNotFound
		}
		if registrationDigest(row) != expected {
			return errors.New("MCP registration changed since preview")
		}
		return access.Delete(ctx, in.ID, in.Scope, in.AgentID)
	default:
		return fmt.Errorf("unsupported mcp operation %q", operation)
	}
}
