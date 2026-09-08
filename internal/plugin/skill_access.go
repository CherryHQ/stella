package plugin

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"strings"
)

// PackageSkillFiles is an authority-bound, immutable copy of one declared
// package Skill. It contains no host path and is safe to hand to the Skill
// management layer after the Access check has completed.
type PackageSkillFiles struct {
	PluginID    string
	Digest      string
	Name        string
	Description string
	Files       map[string][]byte
	Modes       map[string]fs.FileMode
}

// ReadPackageSkill resolves one exact package revision and declared Skill.
// Visibility is inherited from GetDefinition; retired definitions and digest
// or declaration mismatches fail closed before any package bytes are opened.
func (b *Access) ReadPackageSkill(ctx context.Context, id, expectedDigest, name string) (PackageSkillFiles, error) {
	if err := b.ensureActive(); err != nil {
		return PackageSkillFiles{}, err
	}
	if !ValidContentDigest(expectedDigest) || strings.TrimSpace(name) == "" {
		return PackageSkillFiles{}, fmt.Errorf("%w: invalid package Skill reference", ErrInvalidDefinition)
	}
	definition, err := b.GetDefinition(ctx, id)
	if err != nil {
		return PackageSkillFiles{}, err
	}
	if err := ensureActiveDefinition(definition); err != nil {
		return PackageSkillFiles{}, err
	}
	payload, err := DecodeResourcePayload(definition.Spec, "plugin "+id+" definition")
	if err != nil {
		return PackageSkillFiles{}, err
	}
	if payload.Content == nil || payload.Content.Digest != expectedDigest {
		return PackageSkillFiles{}, fmt.Errorf("%w: package digest does not match definition", ErrConflict)
	}
	var declared *SkillResource
	for index := range payload.Skills {
		if payload.Skills[index].Name == name {
			candidate := payload.Skills[index]
			declared = &candidate
			break
		}
	}
	if declared == nil {
		return PackageSkillFiles{}, fmt.Errorf("%w: Skill %q is not declared by plugin %q", ErrNotFound, name, id)
	}

	var files map[string][]byte
	var modes map[string]fs.FileMode
	if definition.Source == SourceBuiltin {
		if b.service.builtinSkillReader == nil {
			return PackageSkillFiles{}, errors.New("plugin: builtin Skill reader unavailable")
		}
		files, modes, err = b.service.builtinSkillReader(ctx, id, name)
	} else {
		if b.service.contentStore == nil {
			return PackageSkillFiles{}, errors.New("plugin: package content store unavailable")
		}
		files, modes, err = b.service.contentStore.ReadPackageSkill(ctx, strings.TrimPrefix(expectedDigest, "sha256:"), name)
	}
	if err != nil {
		return PackageSkillFiles{}, err
	}
	if len(files) == 0 || len(files) != len(modes) {
		return PackageSkillFiles{}, fmt.Errorf("%w: package Skill files are incomplete", ErrInvalidDefinition)
	}
	return PackageSkillFiles{
		PluginID: id, Digest: expectedDigest, Name: name, Description: declared.Description,
		Files: cloneSkillBytes(files), Modes: maps.Clone(modes),
	}, nil
}

func cloneSkillBytes(files map[string][]byte) map[string][]byte {
	copy := make(map[string][]byte, len(files))
	for name, data := range files {
		copy[name] = append([]byte(nil), data...)
	}
	return copy
}
