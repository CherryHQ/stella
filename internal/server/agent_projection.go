package server

import (
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/core/providercred"
)

// agentFromAPI is the explicit transport-to-domain projection for ordinary Agent
// writes. Provider credentials deliberately have no representation on
// config.Agent, so PATCH remains unable to modify them.
func agentFromAPI(in apitypes.Agent) config.Agent {
	out := config.Agent{
		ID:                         stringValue(in.Id),
		Name:                       stringValue(in.Name),
		Model:                      stringValue(in.Model),
		ModelThinking:              stringValue(in.ModelThinking),
		ModelStrong:                stringValue(in.ModelStrong),
		ModelStrongThinking:        stringValue(in.ModelStrongThinking),
		ModelFast:                  stringValue(in.ModelFast),
		ModelFastThinking:          stringValue(in.ModelFastThinking),
		SystemPrompt:               stringValue(in.SystemPrompt),
		Soul:                       stringValue(in.Soul),
		Workspace:                  stringValue(in.Workspace),
		Scope:                      stringValue(in.Scope),
		CreatorID:                  stringValue(in.CreatorId),
		Enabled:                    boolValue(in.Enabled),
		SystemSettingsToolsEnabled: boolValue(in.SystemSettingsToolsEnabled),
	}
	if in.LastActive != nil {
		lastActive := in.LastActive.UTC()
		out.LastActive = &lastActive
	}
	if in.Sandbox != nil && in.Sandbox.Network != nil {
		out.Sandbox.Network.Mode = stringValue(in.Sandbox.Network.Mode)
		if in.Sandbox.Network.Allowlist != nil {
			out.Sandbox.Network.Allowlist = append([]string(nil), (*in.Sandbox.Network.Allowlist)...)
		}
	}
	return out
}

// applyAgentPatch merges a PATCH body into the stored Agent. Every body field
// is a pointer, so omission is distinguishable from an explicit value: omitted
// fields keep their stored value and an explicit "" clears one. Before this the
// handler decoded the body into a zero Agent, which turned {"soul": "x"} into a
// rename-and-disable. Server-owned fields (id, creator, workspace, last_active)
// are ignored; Management.Update re-asserts them.
func applyAgentPatch(existing config.Agent, in apitypes.Agent) config.Agent {
	out := existing
	setString := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	setString(&out.Name, in.Name)
	setString(&out.Model, in.Model)
	setString(&out.ModelThinking, in.ModelThinking)
	setString(&out.ModelStrong, in.ModelStrong)
	setString(&out.ModelStrongThinking, in.ModelStrongThinking)
	setString(&out.ModelFast, in.ModelFast)
	setString(&out.ModelFastThinking, in.ModelFastThinking)
	setString(&out.SystemPrompt, in.SystemPrompt)
	setString(&out.Soul, in.Soul)
	setString(&out.Scope, in.Scope)
	if in.Enabled != nil {
		out.Enabled = *in.Enabled
	}
	if in.SystemSettingsToolsEnabled != nil {
		out.SystemSettingsToolsEnabled = *in.SystemSettingsToolsEnabled
	}
	if in.Sandbox != nil && in.Sandbox.Network != nil {
		setString(&out.Sandbox.Network.Mode, in.Sandbox.Network.Mode)
		if in.Sandbox.Network.Allowlist != nil {
			out.Sandbox.Network.Allowlist = append([]string(nil), (*in.Sandbox.Network.Allowlist)...)
		}
	}
	return out
}

// createAgentFromAPI extracts the sole credential-bearing Agent request into
// write-only domain inputs. The API DTO is never used as a response projection.
func createAgentFromAPI(in apitypes.CreateAgentRequest) (config.Agent, string, []providercred.Input) {
	agent := agentFromAPI(apitypes.Agent{
		CreatorId:                  in.CreatorId,
		Enabled:                    in.Enabled,
		SystemSettingsToolsEnabled: in.SystemSettingsToolsEnabled,
		Id:                         in.Id,
		Model:                      in.Model,
		ModelFast:                  in.ModelFast,
		ModelFastThinking:          in.ModelFastThinking,
		ModelStrong:                in.ModelStrong,
		ModelStrongThinking:        in.ModelStrongThinking,
		ModelThinking:              in.ModelThinking,
		Name:                       in.Name,
		Sandbox:                    in.Sandbox,
		Scope:                      in.Scope,
		Soul:                       in.Soul,
		SystemPrompt:               in.SystemPrompt,
		Workspace:                  in.Workspace,
	})

	var inputs []providercred.Input
	if in.ProviderCredentials != nil {
		inputs = make([]providercred.Input, len(*in.ProviderCredentials))
		for i, credential := range *in.ProviderCredentials {
			inputs[i] = providercred.Input{
				ProviderID: credential.ProviderId,
				APIKey:     stringValue(credential.ApiKey),
			}
		}
	}
	return agent, stringValue(in.TemplateId), inputs
}

// agentViewer is the caller a projection is written for. Two of the Agent
// fields are viewer-dependent, so the projection cannot be a pure function of
// the domain row.
type agentViewer struct {
	userID  string
	isAdmin bool
}

func viewerFrom(info *AuthInfo) agentViewer {
	if info == nil {
		return agentViewer{}
	}
	return agentViewer{userID: info.UserID, isAdmin: info.IsAdmin}
}

// agentToAPI is the explicit secret-free domain-to-transport projection. Keep
// this field list in sync with the public Agent schema rather than serializing a
// domain struct whose internals may grow.
//
// can_manage is the server's answer to "may I configure this?", so no client
// has to rebuild the rule from ids. creator_id rides along only for a viewer
// who may manage the agent: it is a stable user identifier, and a readable
// system-scope agent would otherwise hand every user a directory of user ids.
func agentToAPI(in config.Agent, viewer agentViewer) apitypes.Agent {
	out := apitypes.Agent{
		Id:                  stringPtr(in.ID),
		Name:                stringPtr(in.Name),
		Model:               stringPtr(in.Model),
		ModelThinking:       stringPtr(in.ModelThinking),
		ModelStrong:         stringPtr(in.ModelStrong),
		ModelStrongThinking: stringPtr(in.ModelStrongThinking),
		ModelFast:           stringPtr(in.ModelFast),
		ModelFastThinking:   stringPtr(in.ModelFastThinking),
		SystemPrompt:        stringPtr(in.SystemPrompt),
		Soul:                stringPtr(in.Soul),
		Workspace:           stringPtr(in.Workspace),
		Scope:               stringPtr(in.Scope),
		Enabled:             boolPtrValue(in.Enabled),
		Sandbox: &apitypes.SandboxConfig{Network: &apitypes.SandboxNetworkConfig{
			Mode:      stringPtr(in.Sandbox.Network.Mode),
			Allowlist: stringsPtr(in.Sandbox.Network.Allowlist),
		}},
	}
	canManage := viewer.isAdmin || (in.CreatorID != "" && in.CreatorID == viewer.userID)
	out.CanManage = &canManage
	if canManage {
		out.CreatorId = stringPtr(in.CreatorID)
		out.SystemSettingsToolsEnabled = boolPtrValue(in.SystemSettingsToolsEnabled)
	}
	if in.LastActive != nil {
		lastActive := in.LastActive.UTC()
		out.LastActive = &lastActive
	}
	return out
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func boolValue(value *bool) bool {
	return value != nil && *value
}

func stringPtr(value string) *string { return &value }

func boolPtrValue(value bool) *bool { return &value }

func stringsPtr(values []string) *[]string {
	copy := append([]string(nil), values...)
	return &copy
}
