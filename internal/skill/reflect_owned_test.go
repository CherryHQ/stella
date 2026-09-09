package skill

import (
	"encoding/json"
	"testing"
)

func TestMarkReflectOwnedMetadataPreservesExistingFields(t *testing.T) {
	metadata, err := MarkReflectOwnedMetadata(json.RawMessage(`{"created-at":"2026-07-01T00:00:00Z","source":"manual"}`))
	if err != nil {
		t.Fatalf("MarkReflectOwnedMetadata: %v", err)
	}

	var got map[string]string
	if err := json.Unmarshal(metadata, &got); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if got["created_by"] != ReflectSkillCreatedBy || got["created-at"] != "2026-07-01T00:00:00Z" || got["source"] != "manual" {
		t.Fatalf("metadata fields = %#v", got)
	}
}

func TestMarkReflectOwnedMetadataAcceptsWhitespaceNull(t *testing.T) {
	metadata, err := MarkReflectOwnedMetadata(json.RawMessage(" \nnull\t"))
	if err != nil {
		t.Fatalf("MarkReflectOwnedMetadata: %v", err)
	}
	if !json.Valid(metadata) || !IsReflectOwned(Skill{Metadata: metadata}) {
		t.Fatalf("invalid Reflect metadata: %s", metadata)
	}

	var fields map[string]any
	if err := json.Unmarshal(metadata, &fields); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if len(fields) != 1 || fields[reflectSkillCreatedByKey] != ReflectSkillCreatedBy {
		t.Fatalf("metadata fields = %#v", fields)
	}
}
