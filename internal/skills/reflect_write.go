package skills

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"regexp"
	"strings"
	"time"
)

var validReflectSkillNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

var (
	ErrSkillNotReflectOwned = errors.New("skill is not reflect-owned")
	ErrSkillUsageChanged    = errors.New("skill usage changed")
)

type ReflectSkillCreate struct {
	UserID            string
	AgentID           string
	Name              string
	Description       string
	MainFileContent   string
	Metadata          json.RawMessage
	ChangelogMetadata json.RawMessage
}

type ReflectSkillPatch struct {
	ID                     string
	UserID                 string
	AgentID                string
	ExpectedDigest         string
	Description            *string
	Status                 *string
	DisableModelInvocation *bool
	MainFileContent        *string
	Metadata               json.RawMessage
	ChangelogMetadata      json.RawMessage
}

type ReflectSkillDelete struct {
	ID                      string
	UserID                  string
	AgentID                 string
	ExpectedDigest          string
	ExpectedUsageLastUsedAt time.Time
}

func semanticJSONEqual(left, right json.RawMessage) (bool, error) {
	decode := func(raw json.RawMessage, label string) (any, error) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("skills: decode %s metadata: %w", label, err)
		}
		return value, nil
	}
	leftValue, err := decode(left, "existing")
	if err != nil {
		return false, err
	}
	rightValue, err := decode(right, "requested")
	if err != nil {
		return false, err
	}
	return semanticJSONValueEqual(leftValue, rightValue), nil
}

func normalizeReflectSkillChangelogMetadata(metadata json.RawMessage) (json.RawMessage, error) {
	if len(metadata) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &object); err != nil || object == nil {
		return nil, fmt.Errorf("skills: changelog metadata must be a JSON object")
	}
	return metadata, nil
}

func semanticJSONValueEqual(left, right any) bool {
	switch left := left.(type) {
	case json.Number:
		right, ok := right.(json.Number)
		if !ok {
			return false
		}
		leftNumber, leftOK := new(big.Rat).SetString(left.String())
		rightNumber, rightOK := new(big.Rat).SetString(right.String())
		return leftOK && rightOK && leftNumber.Cmp(rightNumber) == 0
	case map[string]any:
		right, ok := right.(map[string]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for key, leftValue := range left {
			rightValue, ok := right[key]
			if !ok || !semanticJSONValueEqual(leftValue, rightValue) {
				return false
			}
		}
		return true
	case []any:
		right, ok := right.([]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for index := range left {
			if !semanticJSONValueEqual(left[index], right[index]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(left, right)
	}
}

func validateReflectSkillName(name string) error {
	const maxSkillNameLength = 64
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("skills: name is required")
	}
	if len(name) > maxSkillNameLength {
		return fmt.Errorf("skills: name %q exceeds %d characters", name, maxSkillNameLength)
	}
	if !validReflectSkillNameRe.MatchString(name) || strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") || strings.Contains(name, "--") {
		return fmt.Errorf("skills: invalid skill name %q", name)
	}
	return nil
}
