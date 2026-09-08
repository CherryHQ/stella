package skill

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	SkillManifestFile             = ".stella-skill.json"
	MaxManagedSkillFiles          = 512
	MaxManagedSkillPathBytes      = 512
	MaxManagedSkillFileBytes      = 32 << 20
	MaxManagedSkillAggregateBytes = 32 << 20
	MaxManagedSkillManifestBytes  = 256 << 10
	MaxManagedSkillCatalogEntries = 10_000
	maxManagedSkillTreeDepth      = 32
)

var (
	ErrInvalidSkillRevision = errors.New("skills: invalid revision")
	ErrSkillLimit           = errors.New("skills: revision limit exceeded")
	ErrSkillCatalogLimit    = errors.New("skills: catalog limit exceeded")
	ErrSkillDigestConflict  = errors.New("skills: content digest conflict")
	ErrSkillDigestRequired  = errors.New("skills: expected content digest is required")
	ErrSkillNameConflict    = errors.New("skills: name already exists in scope")
)

type revisionFile struct {
	Path    string
	Mode    fs.FileMode
	Content []byte
}

func sameSkillIdentity(left, right Skill) bool {
	return left.ID == right.ID && left.Scope == right.Scope && left.UserID == right.UserID && left.AgentID == right.AgentID && left.Name == right.Name
}

func canonicalManifest(skill Skill) ([]byte, error) {
	metadata, err := decodeStrictJSONObject(skill.Metadata)
	if err != nil {
		return nil, fmt.Errorf("%w: metadata: %w", ErrInvalidSkillRevision, err)
	}
	if !validManifestSkill(skill) {
		return nil, fmt.Errorf("%w: invalid identity or lifecycle metadata", ErrInvalidSkillRevision)
	}
	value := map[string]any{
		"agent_id":                 skill.AgentID,
		"created_at":               skill.CreatedAt.UTC().Format(time.RFC3339Nano),
		"description":              skill.Description,
		"disable_model_invocation": skill.DisableModelInvocation,
		"id":                       skill.ID,
		"lifecycle_version":        json.Number(strconv.FormatInt(skill.Version, 10)),
		"metadata":                 metadata,
		"name":                     skill.Name,
		"schema_version":           json.Number("1"),
		"scope":                    skill.Scope,
		"status":                   skill.Status,
		"updated_at":               skill.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"user_id":                  skill.UserID,
	}
	encoded, err := canonicalJSON(value)
	if err != nil {
		return nil, fmt.Errorf("%w: encode manifest: %w", ErrInvalidSkillRevision, err)
	}
	if len(encoded) > MaxManagedSkillManifestBytes {
		return nil, ErrSkillLimit
	}
	return encoded, nil
}

func validManifestSkill(skill Skill) bool {
	if skill.ID == "" || skill.Name == "" || skill.Version <= 0 || skill.CreatedAt.IsZero() || skill.UpdatedAt.IsZero() || skill.UpdatedAt.Before(skill.CreatedAt) {
		return false
	}
	if !utf8.ValidString(skill.ID + skill.Scope + skill.UserID + skill.AgentID + skill.Name + skill.Description + skill.Status) {
		return false
	}
	if skill.Status != SkillStatusActive && skill.Status != SkillStatusDeprecated {
		return false
	}
	switch skill.Scope {
	case "system":
		return skill.UserID == "" && skill.AgentID == ""
	case "system_agent":
		return skill.UserID == "" && skill.AgentID != ""
	case "user":
		return skill.UserID != "" && skill.AgentID == ""
	case "user_agent":
		return skill.UserID != "" && skill.AgentID != ""
	default:
		return false
	}
}

func decodeCanonicalManifest(source []byte) (Skill, error) {
	if len(source) == 0 || len(source) > MaxManagedSkillManifestBytes {
		return Skill{}, ErrSkillLimit
	}
	value, err := decodeStrictJSON(source)
	if err != nil {
		return Skill{}, fmt.Errorf("%w: decode manifest: %w", ErrInvalidSkillRevision, err)
	}
	object, ok := value.(map[string]any)
	if !ok || len(object) != 13 {
		return Skill{}, fmt.Errorf("%w: manifest fields", ErrInvalidSkillRevision)
	}
	want := []string{"agent_id", "created_at", "description", "disable_model_invocation", "id", "lifecycle_version", "metadata", "name", "schema_version", "scope", "status", "updated_at", "user_id"}
	for _, key := range want {
		if _, ok := object[key]; !ok {
			return Skill{}, fmt.Errorf("%w: missing manifest field %q", ErrInvalidSkillRevision, key)
		}
	}
	stringField := func(key string) (string, bool) { value, ok := object[key].(string); return value, ok }
	id, idOK := stringField("id")
	scope, scopeOK := stringField("scope")
	userID, userOK := stringField("user_id")
	agentID, agentOK := stringField("agent_id")
	name, nameOK := stringField("name")
	description, descriptionOK := stringField("description")
	status, statusOK := stringField("status")
	createdRaw, createdOK := stringField("created_at")
	updatedRaw, updatedOK := stringField("updated_at")
	disable, disableOK := object["disable_model_invocation"].(bool)
	schema, schemaOK := exactPositiveInteger(object["schema_version"])
	version, versionOK := exactPositiveInteger(object["lifecycle_version"])
	metadata, metadataOK := object["metadata"].(map[string]any)
	createdAt, createdErr := time.Parse(time.RFC3339Nano, createdRaw)
	updatedAt, updatedErr := time.Parse(time.RFC3339Nano, updatedRaw)
	if !idOK || !scopeOK || !userOK || !agentOK || !nameOK || !descriptionOK || !statusOK || !createdOK || !updatedOK || !disableOK || !schemaOK || schema != 1 || !versionOK || !metadataOK || createdErr != nil || updatedErr != nil || createdAt.Location() != time.UTC || updatedAt.Location() != time.UTC {
		return Skill{}, fmt.Errorf("%w: manifest field type or value", ErrInvalidSkillRevision)
	}
	metadataJSON, err := canonicalJSON(metadata)
	if err != nil {
		return Skill{}, fmt.Errorf("%w: metadata: %w", ErrInvalidSkillRevision, err)
	}
	skill := Skill{ID: id, Scope: scope, UserID: userID, AgentID: agentID, Name: name, Description: description, Status: status, DisableModelInvocation: disable, Metadata: metadataJSON, CreatedAt: createdAt.UTC(), UpdatedAt: updatedAt.UTC(), Version: version}
	canonical, err := canonicalManifest(skill)
	if err != nil || !bytes.Equal(canonical, source) {
		return Skill{}, fmt.Errorf("%w: manifest is not canonical", ErrInvalidSkillRevision)
	}
	return skill, nil
}

func exactPositiveInteger(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	integer, err := number.Int64()
	return integer, err == nil && integer > 0 && number.String() == strconv.FormatInt(integer, 10)
}

func decodeStrictJSONObject(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	value, err := decodeStrictJSON(raw)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return nil, errors.New("expected JSON object")
	}
	return object, nil
}

func decodeStrictJSON(source []byte) (any, error) {
	if !utf8.Valid(source) {
		return nil, errors.New("invalid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(source))
	decoder.UseNumber()
	value, err := decodeJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("trailing JSON value")
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
					return nil, errors.New("object key is not a string")
				}
				if _, duplicate := object[key]; duplicate {
					return nil, fmt.Errorf("duplicate object key %q", key)
				}
				value, err := decodeJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				object[key] = value
			}
			if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
				return nil, errors.New("unterminated object")
			}
			return object, nil
		case '[':
			array := []any{}
			for decoder.More() {
				value, err := decodeJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				array = append(array, value)
			}
			if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
				return nil, errors.New("unterminated array")
			}
			return array, nil
		default:
			return nil, errors.New("unexpected delimiter")
		}
	case string, bool, nil, json.Number:
		return token, nil
	default:
		return nil, fmt.Errorf("unsupported JSON token %T", token)
	}
}

func canonicalJSON(value any) ([]byte, error) {
	var output bytes.Buffer
	if err := writeCanonicalJSON(&output, value); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func writeCanonicalJSON(output *bytes.Buffer, value any) error {
	switch value := value.(type) {
	case nil:
		output.WriteString("null")
	case bool:
		if value {
			output.WriteString("true")
		} else {
			output.WriteString("false")
		}
	case string:
		encoded, _ := json.Marshal(value)
		output.Write(encoded)
	case json.Number:
		number, err := normalizeJSONNumber(value.String())
		if err != nil {
			return err
		}
		output.WriteString(number)
	case []any:
		output.WriteByte('[')
		for index, item := range value {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := writeCanonicalJSON(output, item); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		output.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				output.WriteByte(',')
			}
			encoded, _ := json.Marshal(key)
			output.Write(encoded)
			output.WriteByte(':')
			if err := writeCanonicalJSON(output, value[key]); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	default:
		return fmt.Errorf("unsupported canonical JSON value %T", value)
	}
	return nil
}

// normalizeJSONNumber canonicalizes exact decimals without float64 or an
// allocation proportional to a caller-controlled exponent.
func normalizeJSONNumber(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("invalid JSON number %q", raw)
	}
	index := 0
	negative := raw[index] == '-'
	if negative {
		index++
		if index == len(raw) {
			return "", fmt.Errorf("invalid JSON number %q", raw)
		}
	}
	integerStart := index
	switch {
	case raw[index] == '0':
		index++
		if index < len(raw) && raw[index] >= '0' && raw[index] <= '9' {
			return "", fmt.Errorf("invalid JSON number %q", raw)
		}
	case raw[index] >= '1' && raw[index] <= '9':
		for index < len(raw) && raw[index] >= '0' && raw[index] <= '9' {
			index++
		}
	default:
		return "", fmt.Errorf("invalid JSON number %q", raw)
	}
	integer := raw[integerStart:index]
	fraction := ""
	if index < len(raw) && raw[index] == '.' {
		index++
		start := index
		for index < len(raw) && raw[index] >= '0' && raw[index] <= '9' {
			index++
		}
		if index == start {
			return "", fmt.Errorf("invalid JSON number %q", raw)
		}
		fraction = raw[start:index]
	}
	exponent := new(big.Int)
	if index < len(raw) && (raw[index] == 'e' || raw[index] == 'E') {
		index++
		start := index
		if index < len(raw) && (raw[index] == '+' || raw[index] == '-') {
			index++
		}
		digits := index
		for index < len(raw) && raw[index] >= '0' && raw[index] <= '9' {
			index++
		}
		if index == digits {
			return "", fmt.Errorf("invalid JSON number %q", raw)
		}
		if _, ok := exponent.SetString(raw[start:index], 10); !ok {
			return "", fmt.Errorf("invalid JSON number %q", raw)
		}
	}
	if index != len(raw) {
		return "", fmt.Errorf("invalid JSON number %q", raw)
	}
	coefficient := strings.TrimLeft(integer+fraction, "0")
	if coefficient == "" {
		return "0", nil
	}
	trimmed := strings.TrimRight(coefficient, "0")
	trailingZeros := len(coefficient) - len(trimmed)
	coefficient = trimmed
	scale := new(big.Int).Set(exponent)
	scale.Sub(scale, big.NewInt(int64(len(fraction))))
	scale.Add(scale, big.NewInt(int64(trailingZeros)))
	scientificExponent := new(big.Int).Add(scale, big.NewInt(int64(len(coefficient)-1)))
	var output strings.Builder
	if negative {
		output.WriteByte('-')
	}
	output.WriteByte(coefficient[0])
	if len(coefficient) > 1 {
		output.WriteByte('.')
		output.WriteString(coefficient[1:])
	}
	if scientificExponent.Sign() != 0 {
		output.WriteByte('e')
		output.WriteString(scientificExponent.String())
	}
	return output.String(), nil
}

func validateSkillPath(filename string) error {
	if filename == "" || !utf8.ValidString(filename) || strings.ContainsRune(filename, '\x00') || strings.Contains(filename, `\`) || path.IsAbs(filename) || path.Clean(filename) != filename || filename == "." {
		return fmt.Errorf("%w: noncanonical path %q", ErrInvalidSkillRevision, filename)
	}
	if len(filename) > MaxManagedSkillPathBytes {
		return ErrSkillLimit
	}
	parts := strings.Split(filename, "/")
	if len(parts) > maxManagedSkillTreeDepth {
		return ErrSkillLimit
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".stella-") {
			return fmt.Errorf("%w: reserved path %q", ErrInvalidSkillRevision, filename)
		}
	}
	return nil
}

func validateRevisionFiles(files []revisionFile) ([]revisionFile, error) {
	if len(files) == 0 || len(files) > MaxManagedSkillFiles {
		return nil, ErrSkillLimit
	}
	seen := make(map[string]struct{}, len(files))
	var total int64
	hasMain := false
	for index := range files {
		if err := validateSkillPath(files[index].Path); err != nil {
			return nil, err
		}
		if files[index].Mode&^fs.FileMode(0o777) != 0 {
			return nil, fmt.Errorf("%w: special mode for %q", ErrInvalidSkillRevision, files[index].Path)
		}
		if _, duplicate := seen[files[index].Path]; duplicate {
			return nil, fmt.Errorf("%w: duplicate path %q", ErrInvalidSkillRevision, files[index].Path)
		}
		seen[files[index].Path] = struct{}{}
		if len(files[index].Content) > MaxManagedSkillFileBytes {
			return nil, ErrSkillLimit
		}
		total += int64(len(files[index].Content))
		if total > MaxManagedSkillAggregateBytes {
			return nil, ErrSkillLimit
		}
		hasMain = hasMain || files[index].Path == MainFile
	}
	if !hasMain {
		return nil, fmt.Errorf("%w: missing %s", ErrInvalidSkillRevision, MainFile)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func digestRevision(manifest []byte, files []revisionFile) (string, error) {
	files, err := validateRevisionFiles(files)
	if err != nil {
		return "", err
	}
	if len(manifest) == 0 || len(manifest) > MaxManagedSkillManifestBytes {
		return "", ErrSkillLimit
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("stella-skill-revision-v1\x00"))
	entries := make([]revisionFile, 0, len(files)+1)
	entries = append(entries, revisionFile{Path: SkillManifestFile, Mode: 0o644, Content: manifest})
	entries = append(entries, files...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	for _, file := range entries {
		writeDigestField(hash, []byte(file.Path))
		var number [8]byte
		binary.BigEndian.PutUint64(number[:], uint64(file.Mode.Perm()))
		_, _ = hash.Write(number[:])
		binary.BigEndian.PutUint64(number[:], uint64(len(file.Content)))
		_, _ = hash.Write(number[:])
		_, _ = hash.Write(file.Content)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validSkillDigest(digest string) bool {
	if len(digest) != sha256.Size*2 || strings.ToLower(digest) != digest {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func writeDigestField(writer io.Writer, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write(value)
}
