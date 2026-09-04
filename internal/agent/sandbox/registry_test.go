package sandbox

import (
	"context"
	"testing"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestNewBackendRegistryRejectsInvalidDefinitions(t *testing.T) {
	create := func(context.Context, BackendRequest) (pkgsandbox.Session, error) { return pkgsandbox.NopSession(), nil }
	tests := []struct {
		name        string
		definitions []BackendDefinition
	}{
		{name: "empty name", definitions: []BackendDefinition{{Create: create}}},
		{name: "nil backend", definitions: []BackendDefinition{{Name: "local"}}},
		{name: "duplicate name", definitions: []BackendDefinition{{Name: "local", Create: create}, {Name: "local", Create: create}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewBackendRegistry(tt.definitions...); err == nil {
				t.Fatal("NewBackendRegistry returned nil error")
			}
		})
	}
}
