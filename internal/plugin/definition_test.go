package plugin

import (
	"encoding/json"
	"testing"
)

func publishedSpec(t *testing.T, raw string) json.RawMessage {
	t.Helper()
	spec, err := PublishDefinitionSpec(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("publish definition spec: %v", err)
	}
	return spec
}

func publishedSpecOrPanic(raw string) json.RawMessage {
	spec, err := PublishDefinitionSpec(json.RawMessage(raw))
	if err != nil {
		panic(err)
	}
	return spec
}
