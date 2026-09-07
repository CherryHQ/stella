package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const contentDigestField = "content_digest"

// PublishDefinitionSpec canonicalizes one authored definition and attaches its
// content identity. The digest is over the complete declaration with the
// digest field removed, so a caller cannot choose an identity for different
// content. Callers persist the returned bytes as the only Definition.Spec
// representation.
func PublishDefinitionSpec(raw json.RawMessage) (json.RawMessage, error) {
	object, err := decodeSpecObject(raw)
	if err != nil {
		return nil, err
	}
	delete(object, contentDigestField)
	canonical, err := canonicalSpecObject(object)
	if err != nil {
		return nil, fmt.Errorf("plugin: encode definition spec: %w", err)
	}
	digest := sha256.Sum256(canonical)
	object[contentDigestField] = json.RawMessage(fmt.Sprintf(`"sha256:%s"`, hex.EncodeToString(digest[:])))
	return canonicalSpecObject(object)
}

// ValidateDefinitionSpecDigest checks the digest envelope and recomputes the
// canonical definition identity. Asset bytes are represented by the Content
// reference inside the envelope, so one digest covers both declaration and
// the exact published tree it points at.
func ValidateDefinitionSpecDigest(raw json.RawMessage) error {
	object, err := decodeSpecObject(raw)
	if err != nil {
		return err
	}
	value, ok := object[contentDigestField]
	if !ok {
		return fmt.Errorf("%w: content_digest is required", ErrInvalidDefinition)
	}
	var supplied string
	if err := json.Unmarshal(value, &supplied); err != nil || supplied == "" {
		return fmt.Errorf("%w: content_digest must be a string", ErrInvalidDefinition)
	}
	if len(supplied) != len("sha256:")+sha256.Size*2 || !bytes.HasPrefix([]byte(supplied), []byte("sha256:")) {
		return fmt.Errorf("%w: content_digest must be sha256 hex", ErrInvalidDefinition)
	}
	if _, err := hex.DecodeString(supplied[len("sha256:"):]); err != nil {
		return fmt.Errorf("%w: content_digest must be sha256 hex", ErrInvalidDefinition)
	}
	if content, ok := object["content"]; ok {
		var reference ContentReference
		if err := decodeStrictJSON(content, &reference); err != nil || !validSHA256Digest(reference.Digest) {
			return fmt.Errorf("%w: content.digest must be sha256 hex", ErrInvalidDefinition)
		}
	}
	delete(object, contentDigestField)
	canonical, err := canonicalSpecObject(object)
	if err != nil {
		return fmt.Errorf("%w: encode definition spec: %w", ErrInvalidDefinition, err)
	}
	digest := sha256.Sum256(canonical)
	expected := "sha256:" + hex.EncodeToString(digest[:])
	if supplied != expected {
		return fmt.Errorf("%w: content_digest does not match definition spec", ErrInvalidDefinition)
	}
	return nil
}

func validSHA256Digest(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !bytes.HasPrefix([]byte(value), []byte("sha256:")) {
		return false
	}
	_, err := hex.DecodeString(value[len("sha256:"):])
	return err == nil
}

func decodeSpecObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, fmt.Errorf("%w: spec must be JSON", ErrInvalidDefinition)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%w: spec must be object", ErrInvalidDefinition)
	}
	return object, nil
}

func canonicalSpecObject(object map[string]json.RawMessage) ([]byte, error) {
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
