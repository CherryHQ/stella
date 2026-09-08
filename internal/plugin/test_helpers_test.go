package plugin

import "encoding/json"

func testDefinition() Definition {
	spec, err := PublishDefinitionSpec(json.RawMessage(`{}`))
	if err != nil {
		panic(err)
	}
	return Definition{ID: "test", DisplayName: "Test", Source: SourceBuiltin, Spec: spec, Revision: 1}
}

func boolPtr(value bool) *bool { return &value }
