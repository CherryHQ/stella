package skill

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/internal/authz"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// BuildAuthorizedPromptSection admits mutable Home metadata only after the
// runtime actor has authorized its PostgreSQL identity. Immutable project and
// release-builtin metadata remain filesystem/Registry reads.
func BuildAuthorizedPromptSection(ctx context.Context, build pkgplugins.SystemPromptContext, project *ProjectSnapshot, reader IdentityReader, authorizer SkillReadAuthorizer) (pkgplugins.SystemPromptSection, error) {
	if reader == nil || authorizer == nil {
		return pkgplugins.SystemPromptSection{}, fmt.Errorf("skills prompt requires identity reader and read authorizer")
	}
	var identities []Skill
	var masked []string
	if turn, ok := SkillTurnViewFromContext(ctx); ok {
		if err := ValidateSkillTurnSelection(turn); err != nil {
			return pkgplugins.SystemPromptSection{}, err
		}
		project = turn.ProjectSnapshot()
		identities = turn.ManagedIdentities()
		masked = turn.MaskedSkillNames()
	} else {
		var err error
		identities, err = listManagedIdentitiesWhenAvailable(ctx, reader, ViewContext{UserID: build.UserID, AgentID: build.AgentID})
		if err != nil {
			return pkgplugins.SystemPromptSection{}, err
		}
	}
	svc := NewService()
	var packages []PackageSkillRef
	if turn, ok := SkillTurnViewFromContext(ctx); ok {
		packages = turn.PackageSkills()
	}
	merged := filterDisabled(svc.ListMergedWithPackages(identities, project, packages, masked), build.DisabledSkillRefs)
	merged = filterMaskedSkills(merged, masked)
	decision, err := authorizer.BeginRead(ctx)
	if errors.Is(err, authz.ErrUnauthenticated) {
		decision, err = nil, nil
	}
	if err != nil {
		return pkgplugins.SystemPromptSection{}, err
	}
	if decision == nil {
		return pkgplugins.SystemPromptSection{}, ErrSkillReadUnavailable
	}
	if turn, ok := SkillTurnViewFromContext(ctx); ok {
		build = addTurnPackageVisibility(build, turn)
	}
	authorized := make([]ResolvedSkill, 0, len(merged))
	for _, rs := range merged {
		id, scope, userID, agentID, required := readAuthorizationTarget(rs)
		if !required {
			authorized = append(authorized, rs)
			continue
		}
		allowed, err := decision.AllowRead(ctx, id, scope, userID, agentID)
		if err != nil {
			return pkgplugins.SystemPromptSection{}, err
		}
		if !allowed {
			continue
		}
		if isFilePackageSkill(rs) {
			authorized = append(authorized, rs)
			continue
		}
		var revision ManagedRevision
		if turn, hasTurn := SkillTurnViewFromContext(ctx); hasTurn {
			var captured bool
			revision, captured = turn.ManagedRevision(rs.ID)
			if !captured && isFileSkillID(rs.ID) {
				return pkgplugins.SystemPromptSection{}, ErrInvalidSkillRevision
			}
			if !captured && validSkillDigest(rs.ContentDigest) {
				revision, err = reader.LoadExactRevision(ctx, resolvedIdentity(rs), rs.ContentDigest)
			} else if !captured {
				revision, err = reader.LoadCurrentRevision(ctx, resolvedIdentity(rs))
			}
		} else if validSkillDigest(rs.ContentDigest) {
			revision, err = reader.LoadExactRevision(ctx, resolvedIdentity(rs), rs.ContentDigest)
		} else {
			revision, err = reader.LoadCurrentRevision(ctx, resolvedIdentity(rs))
		}
		if err != nil {
			return pkgplugins.SystemPromptSection{}, err
		}
		if !sameSkillIdentity(resolvedIdentity(rs), revision.Skill) || !invocationVisible(revision.Skill) {
			continue
		}
		rs.Skill = revision.Skill
		authorized = append(authorized, rs)
	}
	return buildPromptSection(build, authorized)
}

func buildPromptSection(build pkgplugins.SystemPromptContext, merged []ResolvedSkill) (pkgplugins.SystemPromptSection, error) {
	// Apply plugin visibility filtering.
	visible := filterVisibleResolvedSkills(merged, build)
	all := make([]Skill, 0, len(visible))
	for _, rs := range visible {
		all = append(all, rs.Skill)
	}

	if len(all) == 0 {
		return pkgplugins.SystemPromptSection{}, nil
	}

	systemSkills := promptSystemSkills(all)

	var content strings.Builder
	if len(systemSkills) > 0 {
		content.WriteString("System skills are listed below. Load one with skill_load before following its instructions: name=\"<skill-name>\". ")
	} else {
		content.WriteString("Search installed skills before loading skill instructions. ")
	}
	content.WriteString("For project, user, or agent skills not listed here, call skill_installed_search with a compact task-oriented query in q, then load the selected skill with skill_load: name=\"<skill-name>\". ")
	content.WriteString("To load a specific file within a selected skill, call skill_load with name=\"<skill-name>\", path=\"<relative-path>\" ")
	content.WriteString("(path is relative to the skill root, e.g. \"references/api.md\").")
	if len(systemSkills) > 0 {
		content.WriteString("\n\n<system_skills>\n")
		for _, skill := range systemSkills {
			content.WriteString("  <skill>\n")
			content.WriteString("    <name>")
			content.WriteString(escapeXML(skill.Name))
			content.WriteString("</name>\n")
			content.WriteString("    <description>")
			content.WriteString(escapeXML(skill.Description))
			content.WriteString("</description>\n")
			content.WriteString("  </skill>\n")
		}
		content.WriteString("</system_skills>")
	}

	return pkgplugins.SystemPromptSection{
		Title:   "Skills",
		Content: content.String(),
	}, nil
}

func promptSystemSkills(skills []Skill) []Skill {
	out := make([]Skill, 0, len(skills))
	for _, skill := range skills {
		if skill.Scope != "system" || skill.Status == SkillStatusDeprecated || skill.DisableModelInvocation {
			continue
		}
		out = append(out, skill)
	}
	return out
}

func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}

func addTurnPackageVisibility(build pkgplugins.SystemPromptContext, turn SkillTurnView) pkgplugins.SystemPromptContext {
	build.RegisteredPluginIDs = append([]string(nil), build.RegisteredPluginIDs...)
	build.EnabledPluginIDs = append([]string(nil), build.EnabledPluginIDs...)
	for _, ref := range turn.PackageSkills() {
		if ref.Builtin || ref.Masked || ref.Disabled || !isFilePackageSkillRef(ref) {
			continue
		}
		build.RegisteredPluginIDs = append(build.RegisteredPluginIDs, ref.PackageID)
		build.EnabledPluginIDs = append(build.EnabledPluginIDs, ref.PackageID)
	}
	return build
}

func filterVisibleResolvedSkills(skills []ResolvedSkill, build pkgplugins.SystemPromptContext) []ResolvedSkill {
	if len(skills) == 0 {
		return nil
	}
	registered := make(map[string]struct{}, len(build.RegisteredPluginIDs))
	for _, id := range build.RegisteredPluginIDs {
		registered[id] = struct{}{}
	}
	enabled := make(map[string]struct{}, len(build.EnabledPluginIDs))
	for _, id := range build.EnabledPluginIDs {
		enabled[id] = struct{}{}
	}

	out := make([]ResolvedSkill, 0, len(skills))
	for _, skill := range skills {
		owner := skill.OwnerPluginID()
		if owner != "" {
			if _, ok := registered[owner]; !ok {
				continue
			}
			if _, ok := enabled[owner]; !ok {
				continue
			}
		}
		out = append(out, skill)
	}
	return out
}
