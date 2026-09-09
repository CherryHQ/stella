package mcp

import (
	"encoding/json"
	"time"

	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

// defaultDiscoveryTimeout bounds one file-backed discovery pass.
const defaultDiscoveryTimeout = 20 * time.Second

type ToolProvider struct {
	svc *Service
}

// NewToolProvider builds a provider over the registration service.
func NewToolProvider(svc *Service) *ToolProvider {
	return &ToolProvider{
		svc: svc,
	}
}

func catalogAnnotations(reg Registration, catalogTools []CatalogTool, exportedName string) map[string]any {
	if len(catalogTools) == 0 {
		catalogTools = reg.Tools
	}
	for _, catalog := range catalogTools {
		if exportedToolName(reg, catalog.Name) == exportedName {
			return cloneSchema(catalog.Annotations)
		}
	}
	return nil
}

func cloneAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func exportedToolName(reg Registration, remoteName string) string {
	if reg.IsFile() {
		name, _ := agentpackage.ExportedToolName(fileToolPackageIdentity(reg), reg.ServerKey, remoteName)
		return name
	}
	name, _ := agentpackage.ExportedToolName(reg.PluginID, reg.ServerKey, remoteName)
	return name
}

func cloneSchema(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{"type": "object"}
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return map[string]any{"type": "object"}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]any{"type": "object"}
	}
	return out
}
