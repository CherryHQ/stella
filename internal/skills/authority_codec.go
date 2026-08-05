package skills

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

const (
	skillMetadataFile             = ".stella-skill.json"
	skillMetadataSchemaVersionV1  = 1
	skillMetadataDefaultLifecycle = 1
)

// skillMetadataEnvelope is the filesystem-owned state that cannot be derived
// from SKILL.md. Scope and identity deliberately remain properties of its root.
type skillMetadataEnvelope struct {
	Status                 string
	DisableModelInvocation bool
	Metadata               map[string]any
	CreatedAt              time.Time
	UpdatedAt              time.Time
	LegacyLifecycleVersion int64
}

func defaultSkillMetadataEnvelope() skillMetadataEnvelope {
	return skillMetadataEnvelope{
		Status:                 SkillStatusActive,
		Metadata:               map[string]any{},
		LegacyLifecycleVersion: skillMetadataDefaultLifecycle,
	}
}

// decodeSkillMetadataEnvelope accepts only the v1 canonical filesystem envelope.
func decodeSkillMetadataEnvelope(src []byte) (skillMetadataEnvelope, error) {
	value, err := decodeStrictJSON(src)
	if err != nil {
		return skillMetadataEnvelope{}, fmt.Errorf("decode skill metadata envelope: %w", err)
	}
	top, ok := value.(map[string]any)
	if !ok {
		return skillMetadataEnvelope{}, fmt.Errorf("decode skill metadata envelope: expected object")
	}
	const fieldCount = 7
	if len(top) != fieldCount {
		return skillMetadataEnvelope{}, fmt.Errorf("decode skill metadata envelope: unknown or missing fields")
	}
	for key := range top {
		switch key {
		case "schema_version", "status", "disable_model_invocation", "metadata", "created_at", "updated_at", "legacy_lifecycle_version":
		default:
			return skillMetadataEnvelope{}, fmt.Errorf("decode skill metadata envelope: unknown field %q", key)
		}
	}

	version, ok := top["schema_version"].(json.Number)
	if !ok || version.String() != "1" {
		return skillMetadataEnvelope{}, fmt.Errorf("decode skill metadata envelope: unsupported schema_version")
	}
	status, ok := top["status"].(string)
	if !ok || (status != SkillStatusActive && status != SkillStatusDeprecated) {
		return skillMetadataEnvelope{}, fmt.Errorf("decode skill metadata envelope: invalid status")
	}
	disable, ok := top["disable_model_invocation"].(bool)
	if !ok {
		return skillMetadataEnvelope{}, fmt.Errorf("decode skill metadata envelope: disable_model_invocation must be boolean")
	}
	metadata, ok := top["metadata"].(map[string]any)
	if !ok || metadata == nil {
		return skillMetadataEnvelope{}, fmt.Errorf("decode skill metadata envelope: metadata must be an object")
	}
	createdAt, err := decodeUTCTimestamp(top["created_at"])
	if err != nil {
		return skillMetadataEnvelope{}, fmt.Errorf("decode skill metadata envelope: created_at: %w", err)
	}
	updatedAt, err := decodeUTCTimestamp(top["updated_at"])
	if err != nil {
		return skillMetadataEnvelope{}, fmt.Errorf("decode skill metadata envelope: updated_at: %w", err)
	}
	if updatedAt.Before(createdAt) {
		return skillMetadataEnvelope{}, fmt.Errorf("decode skill metadata envelope: updated_at precedes created_at")
	}
	lifecycle, ok := top["legacy_lifecycle_version"].(json.Number)
	if !ok {
		return skillMetadataEnvelope{}, fmt.Errorf("decode skill metadata envelope: legacy_lifecycle_version must be a positive integer")
	}
	lifecycleVersion, err := lifecycle.Int64()
	if err != nil || lifecycleVersion <= 0 || lifecycle.String() != fmt.Sprintf("%d", lifecycleVersion) {
		return skillMetadataEnvelope{}, fmt.Errorf("decode skill metadata envelope: legacy_lifecycle_version must be a positive integer")
	}
	return skillMetadataEnvelope{
		Status:                 status,
		DisableModelInvocation: disable,
		Metadata:               metadata,
		CreatedAt:              createdAt,
		UpdatedAt:              updatedAt,
		LegacyLifecycleVersion: lifecycleVersion,
	}, nil
}

// encodeSkillMetadataEnvelope validates then emits the one compact representation
// used in a tree digest. Newline termination makes the on-disk control file sane.
func encodeSkillMetadataEnvelope(envelope skillMetadataEnvelope) ([]byte, error) {
	if err := validateSkillMetadataEnvelope(envelope); err != nil {
		return nil, err
	}
	value := map[string]any{
		"schema_version":           json.Number("1"),
		"status":                   envelope.Status,
		"disable_model_invocation": envelope.DisableModelInvocation,
		"metadata":                 envelope.Metadata,
		"created_at":               formatUTCTimestamp(envelope.CreatedAt),
		"updated_at":               formatUTCTimestamp(envelope.UpdatedAt),
		"legacy_lifecycle_version": json.Number(fmt.Sprintf("%d", envelope.LegacyLifecycleVersion)),
	}
	encoded, err := canonicalJSON(value)
	if err != nil {
		return nil, fmt.Errorf("encode skill metadata envelope: %w", err)
	}
	return append(encoded, '\n'), nil
}

func validateSkillMetadataEnvelope(envelope skillMetadataEnvelope) error {
	if envelope.Status != SkillStatusActive && envelope.Status != SkillStatusDeprecated {
		return fmt.Errorf("invalid skill metadata status")
	}
	if envelope.Metadata == nil {
		return fmt.Errorf("skill metadata must be an object")
	}
	if envelope.LegacyLifecycleVersion <= 0 {
		return fmt.Errorf("legacy lifecycle version must be positive")
	}
	if !envelope.CreatedAt.IsZero() && envelope.CreatedAt.Location() != time.UTC {
		return fmt.Errorf("created_at must be UTC")
	}
	if !envelope.UpdatedAt.IsZero() && envelope.UpdatedAt.Location() != time.UTC {
		return fmt.Errorf("updated_at must be UTC")
	}
	if envelope.UpdatedAt.Before(envelope.CreatedAt) {
		return fmt.Errorf("updated_at precedes created_at")
	}
	if _, err := canonicalJSON(envelope.Metadata); err != nil {
		return fmt.Errorf("invalid skill metadata: %w", err)
	}
	return nil
}

func decodeUTCTimestamp(value any) (time.Time, error) {
	s, ok := value.(string)
	if !ok || !strings.HasSuffix(s, "Z") {
		return time.Time{}, fmt.Errorf("must be an RFC3339 UTC timestamp")
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil || t.Location() != time.UTC {
		return time.Time{}, fmt.Errorf("must be an RFC3339 UTC timestamp")
	}
	return t.UTC(), nil
}

func formatUTCTimestamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// skillTreeEntry uses bytes rather than strings because Skill files are opaque
// filesystem content. A slice makes duplicate paths representable and rejectable.
type skillTreeEntry struct {
	Path    string
	Content []byte
	Mode    fs.FileMode
}

type skillTree struct {
	Metadata skillMetadataEnvelope
	Files    []skillTreeEntry
}

// digestSkillTree hashes a complete managed revision, adding the reserved control
// file itself so metadata updates are ordinary content changes to its authority.
func digestSkillTree(tree skillTree) (string, error) {
	metadata, err := encodeSkillMetadataEnvelope(tree.Metadata)
	if err != nil {
		return "", err
	}
	if err := validateSkillTreeFiles(tree.Files); err != nil {
		return "", err
	}
	files := append(append([]skillTreeEntry(nil), tree.Files...), skillTreeEntry{Path: skillMetadataFile, Content: metadata, Mode: 0o644})
	entries := make([]sandbox.ManagedSkillTreeEntry, 0, len(files))
	for _, file := range files {
		content := file.Content
		entries = append(entries, sandbox.ManagedSkillTreeEntry{Path: file.Path, Mode: file.Mode, Length: int64(len(content)), Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(content)), nil }})
	}
	return sandbox.DigestManagedSkillTreeV1(entries)
}

func validateSkillTreeFiles(files []skillTreeEntry) error {
	seen := make(map[string]struct{}, len(files))
	mainFile := false
	for _, file := range files {
		if err := validateSkillTreePath(file.Path); err != nil {
			return err
		}
		if file.Mode&fs.ModeType != 0 {
			return fmt.Errorf("skill tree file %q is not a regular file", file.Path)
		}
		if _, ok := seen[file.Path]; ok {
			return fmt.Errorf("duplicate skill tree path %q", file.Path)
		}
		seen[file.Path] = struct{}{}
		if file.Path == MainFile {
			if len(file.Content) == 0 {
				return fmt.Errorf("SKILL.md must not be empty")
			}
			mainFile = true
		}
	}
	if !mainFile {
		return fmt.Errorf("skill tree is missing SKILL.md")
	}
	return nil
}

func validateSkillTreePath(raw string) error {
	if err := sandbox.ValidateManagedSkillTreePath(raw); err != nil {
		return err
	}
	if raw == skillMetadataFile {
		return fmt.Errorf("skill tree path %q is Stella-owned", raw)
	}
	if raw == ".stella-revisions" || strings.HasPrefix(raw, ".stella-revisions/") {
		return fmt.Errorf("skill tree path %q is in the reserved revisions namespace", raw)
	}
	return nil
}

func decodeStrictJSON(src []byte) (any, error) {
	if !utf8.Valid(src) {
		return nil, fmt.Errorf("invalid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(src))
	decoder.UseNumber()
	value, err := decodeJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	if decoder.More() {
		return nil, fmt.Errorf("trailing JSON value")
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("trailing JSON value")
		}
		return nil, fmt.Errorf("trailing JSON value: %w", err)
	}
	return value, nil
}

func decodeJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch token := token.(type) {
	case json.Delim:
		switch token {
		case '{':
			object := map[string]any{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, fmt.Errorf("object key is not a string")
				}
				if _, exists := object[key]; exists {
					return nil, fmt.Errorf("duplicate JSON object key %q", key)
				}
				value, err := decodeJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				object[key] = value
			}
			if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
				return nil, fmt.Errorf("unterminated object")
			}
			return object, nil
		case '[':
			var array []any
			for decoder.More() {
				value, err := decodeJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				array = append(array, value)
			}
			if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
				return nil, fmt.Errorf("unterminated array")
			}
			return array, nil
		default:
			return nil, fmt.Errorf("unexpected JSON delimiter %q", token)
		}
	default:
		return token, nil
	}
}

func canonicalJSON(value any) ([]byte, error) {
	var out bytes.Buffer
	if err := appendCanonicalJSON(&out, value); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func appendCanonicalJSON(out *bytes.Buffer, value any) error {
	switch value := value.(type) {
	case nil, bool:
		encoded, _ := json.Marshal(value)
		out.Write(encoded)
	case string:
		encoded, _ := json.Marshal(value)
		out.Write(encoded)
	case json.Number:
		number, err := normalizeJSONNumber(value.String())
		if err != nil {
			return err
		}
		out.WriteString(number)
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			encoded, _ := json.Marshal(key)
			out.Write(encoded)
			out.WriteByte(':')
			if err := appendCanonicalJSON(out, value[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	case []any:
		out.WriteByte('[')
		for i, item := range value {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := appendCanonicalJSON(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	default:
		return fmt.Errorf("unsupported JSON value %T", value)
	}
	return nil
}

// normalizeJSONNumber maps each exact JSON decimal value to a compact scientific
// spelling. It never converts through float64 or expands a value by its exponent.
func normalizeJSONNumber(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("invalid JSON number %q", raw)
	}
	i := 0
	negative := false
	if raw[i] == '-' {
		negative = true
		i++
		if i == len(raw) {
			return "", fmt.Errorf("invalid JSON number %q", raw)
		}
	}

	integerStart := i
	switch {
	case raw[i] == '0':
		i++
		if i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
			return "", fmt.Errorf("invalid JSON number %q", raw)
		}
	case raw[i] >= '1' && raw[i] <= '9':
		for i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
			i++
		}
	default:
		return "", fmt.Errorf("invalid JSON number %q", raw)
	}
	integer := raw[integerStart:i]

	fraction := ""
	if i < len(raw) && raw[i] == '.' {
		fractionStart := i + 1
		i = fractionStart
		for i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
			i++
		}
		if i == fractionStart {
			return "", fmt.Errorf("invalid JSON number %q", raw)
		}
		fraction = raw[fractionStart:i]
	}

	exponent := new(big.Int)
	if i < len(raw) && (raw[i] == 'e' || raw[i] == 'E') {
		i++
		exponentStart := i
		if i < len(raw) && (raw[i] == '+' || raw[i] == '-') {
			i++
		}
		digitsStart := i
		for i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
			i++
		}
		if i == digitsStart {
			return "", fmt.Errorf("invalid JSON number %q", raw)
		}
		if _, ok := exponent.SetString(raw[exponentStart:i], 10); !ok {
			return "", fmt.Errorf("invalid JSON number %q", raw)
		}
	}
	if i != len(raw) {
		return "", fmt.Errorf("invalid JSON number %q", raw)
	}

	coefficient := strings.TrimLeft(integer+fraction, "0")
	if coefficient == "" {
		return "0", nil
	}
	trailingZeros := len(coefficient) - len(strings.TrimRight(coefficient, "0"))
	coefficient = coefficient[:len(coefficient)-trailingZeros]

	scale := new(big.Int).Set(exponent)
	scale.Sub(scale, big.NewInt(int64(len(fraction))))
	scale.Add(scale, big.NewInt(int64(trailingZeros)))
	scientificExponent := scale.Add(scale, big.NewInt(int64(len(coefficient)-1)))

	var out strings.Builder
	if negative {
		out.WriteByte('-')
	}
	out.WriteByte(coefficient[0])
	if len(coefficient) > 1 {
		out.WriteByte('.')
		out.WriteString(coefficient[1:])
	}
	if scientificExponent.Sign() != 0 {
		out.WriteByte('e')
		out.WriteString(scientificExponent.String())
	}
	return out.String(), nil
}
