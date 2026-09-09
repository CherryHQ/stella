package plugin

import (
	"errors"
	"testing"
)

func TestPublishDefinitionSpecRequiresMatchingDigest(t *testing.T) {
	published, err := PublishDefinitionSpec([]byte(`{"description":"demo","content":{"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateDefinitionSpecDigest(published); err != nil {
		t.Fatalf("published spec rejected: %v", err)
	}
	if err := ValidateDefinitionSpecDigest([]byte(`{"description":"demo"}`)); !errors.Is(err, ErrInvalidDefinition) {
		t.Fatalf("missing digest error = %v, want ErrInvalidDefinition", err)
	}
	if err := ValidateDefinitionSpecDigest([]byte(`{"description":"demo","content_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)); !errors.Is(err, ErrInvalidDefinition) {
		t.Fatalf("mismatched digest error = %v, want ErrInvalidDefinition", err)
	}
}
