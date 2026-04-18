package skills

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

// skillStorePlatform is a minimal Platform implementation for prompt tests.
type skillStorePlatform struct {
	store pkgplugins.SkillStore
}

func (p *skillStorePlatform) Logger() *slog.Logger                        { return slog.Default() }
func (p *skillStorePlatform) ConfigStore() pkgplugins.ConfigStore         { return nil }
func (p *skillStorePlatform) StateStore() pkgplugins.StateStore           { return nil }
func (p *skillStorePlatform) Scheduler() pkgplugins.Scheduler             { return nil }
func (p *skillStorePlatform) Notifier() pkgplugins.Notifier               { return nil }
func (p *skillStorePlatform) Auth() pkgplugins.Auth                       { return nil }
func (p *skillStorePlatform) RuntimeLookup() pkgplugins.RuntimeLookup     { return nil }
func (p *skillStorePlatform) ChannelPlatform() pkgplugins.ChannelPlatform { return nil }
func (p *skillStorePlatform) ReflectPlatform() pkgplugins.ReflectPlatform { return nil }
func (p *skillStorePlatform) SkillStore() pkgplugins.SkillStore           { return p.store }

func TestBuildPromptSectionIncludesVisibleSkills(t *testing.T) {
	store, userID, _, projectRoot := newTestSkillStore(t)
	ctx := context.Background()

	// Seed a project-scoped skill
	_, err := store.Create(ctx, pkgplugins.Skill{
		Scope:       "project",
		Project:     projectRoot,
		Name:        "project-skill",
		Description: "Project skill",
		Status:      "active",
	}, map[string]string{pkgplugins.SkillMainFile: "# Project Skill"})
	if err != nil {
		t.Fatalf("create project skill: %v", err)
	}

	// Seed a user-scoped skill (draft — should still appear since ListSkillsVisible only filters deprecated)
	_, err = store.Create(ctx, pkgplugins.Skill{
		Scope:       "user",
		UserID:      userID,
		Name:        "user-skill",
		Description: "User skill",
		Status:      "draft",
	}, map[string]string{pkgplugins.SkillMainFile: "# User Skill"})
	if err != nil {
		t.Fatalf("create user skill: %v", err)
	}

	// Seed a deprecated skill — should NOT appear (filtered by ListSkillsVisible).
	// Note: we use Update to deprecate after creation since Create with deprecated
	// would still succeed but List won't return it.
	deprecatedID, err := store.Create(ctx, pkgplugins.Skill{
		Scope:       "user",
		UserID:      userID,
		Name:        "old-skill",
		Description: "Old skill",
		Status:      "active",
	}, map[string]string{pkgplugins.SkillMainFile: "# Old Skill"})
	if err != nil {
		t.Fatalf("create old skill: %v", err)
	}
	deprecated := "deprecated"
	if err := store.Update(ctx, deprecatedID, pkgplugins.SkillUpdatePatch{Status: &deprecated}); err != nil {
		t.Fatalf("deprecate old skill: %v", err)
	}

	platform := &skillStorePlatform{store: store}
	section, err := BuildPromptSection(ctx, pkgplugins.SystemPromptContext{
		Platform:    platform,
		UserID:      userID,
		ProjectRoot: projectRoot,
	})
	if err != nil {
		t.Fatal(err)
	}

	if section.Title != "Skills" {
		t.Fatalf("unexpected section title: %q", section.Title)
	}
	if !strings.Contains(section.Content, "<name>project-skill</name>") {
		t.Fatalf("expected project skill in prompt content: %s", section.Content)
	}
	if !strings.Contains(section.Content, "<name>user-skill</name>") {
		t.Fatalf("expected user skill in prompt content: %s", section.Content)
	}
	if strings.Contains(section.Content, "<name>old-skill</name>") {
		t.Fatalf("did not expect deprecated skill in prompt content: %s", section.Content)
	}
}

func TestBuildPromptSectionOmitsEmptySkillList(t *testing.T) {
	store, userID, _, projectRoot := newTestSkillStore(t)
	// Empty store — no skills.
	platform := &skillStorePlatform{store: store}
	section, err := BuildPromptSection(context.Background(), pkgplugins.SystemPromptContext{
		Platform:    platform,
		UserID:      userID,
		ProjectRoot: projectRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	if section.Title != "" || section.Content != "" {
		t.Fatalf("expected empty section, got %#v", section)
	}
}

func TestBuildPromptSectionNilPlatform(t *testing.T) {
	// nil Platform should return empty section without panic.
	section, err := BuildPromptSection(context.Background(), pkgplugins.SystemPromptContext{})
	if err != nil {
		t.Fatal(err)
	}
	if section.Title != "" || section.Content != "" {
		t.Fatalf("expected empty section for nil platform, got %#v", section)
	}
}
