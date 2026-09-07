package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// EnsureMCPServerDeclaration adds one server key to an owned remote-MCP
// definition. The caller must already be inside Service.WithMutationTx so the
// definition CAS, parent config update, and child identity insert share one
// transaction. Package definitions are immutable and cannot gain keys here.
func (b *Access) EnsureMCPServerDeclaration(ctx context.Context, pluginID string, expectedDefRevision int64, key string, server MCPServerResource) (Definition, error) {
	if err := b.ensureActive(); err != nil {
		return Definition{}, err
	}
	if !b.service.txBound || b.service.mutationTx == nil {
		return Definition{}, ErrNestedMutation
	}
	if expectedDefRevision < 1 || strings.TrimSpace(pluginID) == "" || strings.TrimSpace(key) == "" {
		return Definition{}, fmt.Errorf("%w: MCP declaration identity is required", ErrInvalidConfig)
	}
	definition, err := b.managedDefinition(ctx, pluginID)
	if err != nil {
		return Definition{}, err
	}
	if definition.Revision != expectedDefRevision {
		return Definition{}, ErrConflict
	}
	payload, err := DecodeResourcePayload(definition.Spec, "MCP definition spec")
	if err != nil {
		return Definition{}, err
	}
	if payload.Origin != "remote_mcp" {
		return Definition{}, fmt.Errorf("%w: only remote MCP definitions may add server declarations", ErrForbidden)
	}
	if payload.MCPServers == nil {
		payload.MCPServers = make(map[string]MCPServerResource)
	}
	if existing, ok := payload.MCPServers[key]; ok {
		if reflect.DeepEqual(existing, server) {
			return definition, nil
		}
		return Definition{}, fmt.Errorf("%w: MCP server declaration %q already exists", ErrConflict, key)
	}
	payload.MCPServers[key] = server
	if err := ValidateResourceDeclarations(payload, definition.ID, nil); err != nil {
		return Definition{}, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Definition{}, fmt.Errorf("%w: encode MCP declaration", ErrInvalidDefinition)
	}
	spec, err := PublishDefinitionSpec(encoded)
	if err != nil {
		return Definition{}, err
	}
	candidate := definition
	candidate.Spec = spec
	if err := validateCustomSpec(candidate); err != nil {
		return Definition{}, err
	}
	row, err := b.service.q.UpdatePluginDefinitionCAS(ctx, sqlc.UpdatePluginDefinitionCASParams{
		ID: definition.ID, Revision: expectedDefRevision, DisplayName: definition.DisplayName, Spec: spec,
	})
	if err != nil {
		return Definition{}, mapConflict(err)
	}
	return fromSQLDefinition(row), nil
}
