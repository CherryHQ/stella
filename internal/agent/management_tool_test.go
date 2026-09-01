package agent

import (
	"context"
	"strings"
	"testing"
)

type rejectingAgentHandler struct{}

func (rejectingAgentHandler) Create(context.Context, SettingsAgentCreateInput) (any, error) {
	return nil, nil
}

func (rejectingAgentHandler) Delete(context.Context, SettingsAgentDeleteInput) (any, error) {
	return nil, nil
}

func (rejectingAgentHandler) Get(context.Context, SettingsAgentGetInput) (any, error) {
	return nil, nil
}

func (rejectingAgentHandler) List(context.Context, SettingsAgentListInput) (any, error) {
	return nil, nil
}

func (rejectingAgentHandler) Update(context.Context, SettingsAgentUpdateInput) (any, error) {
	return nil, nil
}

// The settings contracts intentionally omit all Agent credential inputs. This
// checks strict decoding too, so a hand-crafted Code Mode call cannot bypass the
// provider schema and smuggle a secret into a persisted tool invocation.
func TestAgentManagementSchemasRejectCredentialFields(t *testing.T) {
	for _, action := range []string{"create", "update"} {
		for _, field := range []string{"api_key", "token", "credential_ref"} {
			t.Run(action+"/"+field, func(t *testing.T) {
				args := map[string]any{field: "secret"}
				if action == "create" {
					args["name"] = "agent"
				} else {
					args["id"] = "agent"
					args["expected_version"] = "version"
				}
				_, err := SettingsAgentDispatch(t.Context(), rejectingAgentHandler{}, action, args)
				if err == nil || !strings.Contains(err.Error(), field) {
					t.Fatalf("SettingsAgentDispatch(%s) error = %v, want rejected %q", action, err, field)
				}
			})
		}
	}
}
