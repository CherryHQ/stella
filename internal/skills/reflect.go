package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

var validReflectSkillNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

var (
	ErrSkillVersionConflict            = errors.New("skill version conflict")
	ErrSkillNotReflectOwned            = errors.New("skill is not reflect-owned")
	ErrSkillUsageChanged               = errors.New("skill usage changed")
	ErrSkillNotMutable                 = errors.New("skill is not mutable")
	ErrInvalidSkillFilePath            = errors.New("invalid skill file path")
	ErrInvalidManagedSkillFileMutation = errors.New("invalid managed skill file mutation")
)

type ReflectSkillCreate struct {
	UserID          string
	AgentID         string
	Name            string
	Description     string
	MainFileContent string
	Metadata        json.RawMessage
	// ChangelogMetadata is retained for plugin compatibility; Home records it
	// in managed Skill metadata rather than a PostgreSQL changelog row.
	ChangelogMetadata json.RawMessage
}

type ReflectSkillPatch struct {
	ID                     string
	UserID                 string
	AgentID                string
	ExpectedVersion        int64
	ExpectedDigest         string
	Description            *string
	Status                 *string
	DisableModelInvocation *bool
	MainFileContent        *string
	Metadata               json.RawMessage
	ChangelogMetadata      json.RawMessage
}

type ReflectSkillDelete struct {
	ID                           string
	UserID                       string
	AgentID                      string
	ExpectedVersion              int64
	ExpectedDigest               string
	ExpectedUsageLastUsedAt      time.Time
	ExpectedPairLatestActivityAt time.Time
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

func validateSkillFilePaths(files map[string]string) error {
	for raw := range files {
		clean := path.Clean(raw)
		if raw == "" || strings.ContainsRune(raw, '\x00') || strings.Contains(raw, "\\") || path.IsAbs(raw) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != raw {
			return fmt.Errorf("%w: %q must be a canonical relative path", ErrInvalidSkillFilePath, raw)
		}
	}
	return nil
}

func validateSkillDeletePaths(paths []string) error {
	for _, path := range paths {
		if path == MainFile {
			return errors.New("skills: SKILL.md cannot be deleted")
		}
		if err := validateSkillFilePaths(map[string]string{path: ""}); err != nil {
			return err
		}
	}
	return nil
}

func validateManagedSkillFileChanges(files map[string]string, deleteFiles []string) error {
	if err := validateSkillFilePaths(files); err != nil {
		return err
	}
	if err := validateSkillDeletePaths(deleteFiles); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(deleteFiles))
	for _, file := range deleteFiles {
		if _, duplicate := seen[file]; duplicate {
			return fmt.Errorf("%w: duplicate delete path %q", ErrInvalidManagedSkillFileMutation, file)
		}
		seen[file] = struct{}{}
		if _, both := files[file]; both {
			return fmt.Errorf("%w: path %q is both upserted and deleted", ErrInvalidManagedSkillFileMutation, file)
		}
	}
	return nil
}

func isMutableSkillScope(scope string) bool {
	return scope == "user" || scope == "user_agent" || scope == "system_agent"
}
