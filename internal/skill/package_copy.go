package skill

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/CherryHQ/stella/internal/authz"
)

var ErrPackageCopyUnavailable = errors.New("package Skill copy is unavailable")

// ManagedPackageCopy identifies one immutable package Skill and its managed
// destination. The source digest is a required CAS pin, and the destination
// identity is authorized through the existing ManageScope path.
type ManagedPackageCopy struct {
	SourcePluginID        string
	ExpectedPackageDigest string
	SkillName             string
	Scope                 string
	TargetAgentID         string
}

// CopyPackageSkill makes an independent managed copy. Later package upgrades
// and edits to the copy therefore have no shared mutable state.
func (m *Management) CopyPackageSkill(ctx context.Context, authority authz.Authority, in ManagedPackageCopy) (SkillSnapshot, error) {
	userID, agentID, err := m.manageScope(ctx, authority, in.Scope, in.TargetAgentID)
	if err != nil {
		return SkillSnapshot{}, err
	}
	if m == nil || m.packageReader == nil {
		return SkillSnapshot{}, ErrPackageCopyUnavailable
	}
	if !validInventoryComponent(in.SourcePluginID) || !validPackageDigest(in.ExpectedPackageDigest) || !validInventoryComponent(in.SkillName) {
		return SkillSnapshot{}, ErrInvalidSkillRevision
	}
	revision, err := m.packageReader.ReadPackageSkill(ctx, authority, in.SourcePluginID, in.ExpectedPackageDigest, in.SkillName)
	if err != nil {
		return SkillSnapshot{}, err
	}
	if revision.Ref.PackageID != in.SourcePluginID || revision.Ref.PackageDigest != in.ExpectedPackageDigest || revision.Ref.Name != in.SkillName {
		return SkillSnapshot{}, ErrInvalidSkillRevision
	}
	if len(revision.Files) == 0 || len(revision.Files) != len(revision.Modes) {
		return SkillSnapshot{}, ErrInvalidSkillRevision
	}
	files := make(map[string]ManagedSkillFile, len(revision.Files))
	for filename, content := range revision.Files {
		mode, ok := revision.Modes[filename]
		if !ok || mode&fs.ModeType != 0 {
			return SkillSnapshot{}, ErrInvalidSkillRevision
		}
		if err := validateSkillPath(filename); err != nil {
			return SkillSnapshot{}, err
		}
		files[filename] = ManagedSkillFile{Content: bytes.Clone(content), Mode: mode}
	}
	if _, ok := files[MainFile]; !ok || len(files[MainFile].Content) == 0 {
		return SkillSnapshot{}, fmt.Errorf("%w: package Skill must include %s", ErrInvalidSkillRevision, MainFile)
	}
	metadata, err := packageCopyMetadata(in.SourcePluginID, in.ExpectedPackageDigest, in.SkillName)
	if err != nil {
		return SkillSnapshot{}, err
	}
	return m.store.CreateManagedSkillWithFiles(ctx, Skill{
		Scope: in.Scope, UserID: userID, AgentID: agentID, Name: in.SkillName,
		Description: revision.Ref.Description, Metadata: metadata, Status: SkillStatusActive,
	}, files)
}

func packageCopyMetadata(pluginID, digest, name string) (json.RawMessage, error) {
	// Keep Source human-readable for the existing UI while retaining structured
	// fields for future provenance and copy/update diagnostics.
	return json.Marshal(map[string]any{
		"source":                fmt.Sprintf("plugin:%s/%s@%s", pluginID, name, digest),
		"source_plugin_id":      pluginID,
		"source_package_digest": digest,
		"source_skill":          name,
	})
}
