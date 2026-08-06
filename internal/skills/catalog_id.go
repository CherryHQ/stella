package skills

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
)

// A length-framed ID makes every accepted owner/name byte sequence
// unambiguous without treating it as a filesystem path.
const (
	filesystemSkillIDPrefix  = "skill-v2-"
	maxFilesystemSkillIDSize = 512
)

func encodeFilesystemSkillID(scope, userID, agentID, name string) (string, error) {
	// Constructing the typed Home root performs the durable owner grammar and
	// canonical identity validation; empty-owner checks alone are insufficient.
	if _, err := homeCatalogSkillRoot(HomeCatalogRoot{Scope: scope, UserID: userID, AgentID: agentID}); err != nil {
		return "", err
	}
	if err := skillNameValidationError(name, name); err != nil {
		return "", err
	}
	parts := []string{scope, userID, agentID, name}
	var b strings.Builder
	for _, part := range parts {
		b.WriteString(strconv.Itoa(len(part)))
		b.WriteByte(':')
		b.WriteString(part)
	}
	id := filesystemSkillIDPrefix + base64.RawURLEncoding.EncodeToString([]byte(b.String()))
	if len(id) > maxFilesystemSkillIDSize {
		return "", errors.New("skills: filesystem Skill ID exceeds maximum length")
	}
	return id, nil
}

func decodeFilesystemSkillID(id string) (scope, userID, agentID, name string, err error) {
	if !strings.HasPrefix(id, filesystemSkillIDPrefix) {
		err = errors.New("skills: invalid filesystem Skill ID")
		return
	}
	if len(id) > maxFilesystemSkillIDSize {
		err = errors.New("skills: invalid filesystem Skill ID")
		return
	}
	payload, decodeErr := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, filesystemSkillIDPrefix))
	if decodeErr != nil {
		err = errors.New("skills: invalid filesystem Skill ID")
		return
	}
	rest := string(payload)
	parts := make([]string, 0, 4)
	for range 4 {
		i := strings.IndexByte(rest, ':')
		if i <= 0 {
			err = errors.New("skills: invalid filesystem Skill ID")
			return
		}
		n, parseErr := strconv.Atoi(rest[:i])
		if parseErr != nil || n < 0 || len(rest[i+1:]) < n {
			err = errors.New("skills: invalid filesystem Skill ID")
			return
		}
		parts = append(parts, rest[i+1:i+1+n])
		rest = rest[i+1+n:]
	}
	if rest != "" {
		err = errors.New("skills: invalid filesystem Skill ID")
		return
	}
	scope, userID, agentID, name = parts[0], parts[1], parts[2], parts[3]
	canonical, encodeErr := encodeFilesystemSkillID(scope, userID, agentID, name)
	if encodeErr != nil || canonical != id {
		err = errors.New("skills: invalid filesystem Skill ID")
	}
	return
}
