package skills

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
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
func (p *skillStorePlatform) SkillStore() pkgplugins.SkillStore           { return p.store }

func TestBuildPromptSectionUsesSearchFirstInstructions(t *testing.T) {
	store, userID, _ := newTestSkillStore(t)
	ctx := context.Background()

	// Seed a project skill on disk (project skills are now FS-only).
	projectRoot := t.TempDir()
	projSkillDir := filepath.Join(projectRoot, ".agents", "skills", "project-skill")
	if err := os.MkdirAll(projSkillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projSkillDir, "SKILL.md"),
		[]byte("---\nname: project-skill\ndescription: Project skill\nstatus: active\n---\n# Project Skill\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// Seed an active user-scoped skill.
	_, err := store.Create(ctx, pkgplugins.Skill{
		Scope:       "user",
		UserID:      userID,
		Name:        "user-skill",
		Description: "User skill",
		Status:      "active",
	}, map[string]string{pkgplugins.SkillMainFile: "# User Skill"})
	if err != nil {
		t.Fatalf("create user skill: %v", err)
	}

	// Seed a deprecated skill — should NOT appear (filtered by ListSkillsVisible).
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
	// Deprecated is a legacy read-only state; seed it below the business API.
	if _, err := store.db.Exec(ctx, `UPDATE skill SET status = 'deprecated' WHERE id = $1`, deprecatedID); err != nil {
		t.Fatalf("seed deprecated old skill: %v", err)
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
	if !strings.Contains(section.Content, `action="search_installed"`) {
		t.Fatalf("expected search_installed instruction in prompt content: %s", section.Content)
	}
	if !strings.Contains(section.Content, `action="load"`) {
		t.Fatalf("expected load instruction in prompt content: %s", section.Content)
	}
	for _, leaked := range []string{"project-skill", "Project skill", "user-skill", "User skill", "old-skill"} {
		if strings.Contains(section.Content, leaked) {
			t.Fatalf("prompt content leaked skill catalog item %q: %s", leaked, section.Content)
		}
	}
}

func TestBuildPromptSectionIncludesBuiltinSkillList(t *testing.T) {
	store, userID, _ := newTestSkillStore(t)
	// Empty store and no project skills.
	platform := &skillStorePlatform{store: store}
	section, err := BuildPromptSection(context.Background(), pkgplugins.SystemPromptContext{
		Platform: platform,
		UserID:   userID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if section.Title != "Skills" || !strings.Contains(section.Content, "<name>stella</name>") {
		t.Fatalf("expected builtin Skills section, got %#v", section)
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

func TestBuildPromptSectionListsSystemSkillsAndSearchesOthers(t *testing.T) {
	store, userID, _ := newTestSkillStore(t)
	ctx := context.Background()

	for _, name := range []string{"stella", "code-review", "research"} {
		if _, err := store.Create(ctx, pkgplugins.Skill{
			Scope:       "system",
			Name:        name,
			Description: name + " skill",
			Status:      "active",
		}, map[string]string{pkgplugins.SkillMainFile: "# " + name}); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	if _, err := store.Create(ctx, pkgplugins.Skill{
		Scope:       "user",
		UserID:      userID,
		Name:        "user-skill",
		Description: "User skill",
		Status:      "active",
	}, map[string]string{pkgplugins.SkillMainFile: "# User Skill"}); err != nil {
		t.Fatalf("create user skill: %v", err)
	}

	platform := &skillStorePlatform{store: store}
	section, err := BuildPromptSection(ctx, pkgplugins.SystemPromptContext{
		Platform: platform,
		UserID:   userID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(section.Content, `action="search_installed"`) {
		t.Fatalf("expected search_installed instruction in prompt content: %s", section.Content)
	}
	for _, name := range []string{"stella", "code-review", "research"} {
		if !strings.Contains(section.Content, "<name>"+name+"</name>") {
			t.Fatalf("expected system skill %s in prompt content: %s", name, section.Content)
		}
	}
	if strings.Contains(section.Content, "user-skill") {
		t.Fatalf("prompt content should not enumerate non-system user skill: %s", section.Content)
	}
}

func TestBuildPromptSectionFiltersPluginOwnedSystemSkillsByPluginState(t *testing.T) {
	store, _, _ := newTestSkillStore(t)
	ctx := context.Background()

	meta, err := json.Marshal(map[string]any{"owner_plugin": "tool/lark-cli"})
	if err != nil {
		t.Fatalf("Marshal metadata: %v", err)
	}
	if _, err := store.Create(ctx, pkgplugins.Skill{
		Scope:       "system",
		Name:        "lark-cli",
		Description: "Lark skill",
		Status:      "active",
		Metadata:    meta,
	}, map[string]string{pkgplugins.SkillMainFile: "# Lark"}); err != nil {
		t.Fatalf("create lark system skill: %v", err)
	}

	platform := &skillStorePlatform{store: store}
	section, err := BuildPromptSection(ctx, pkgplugins.SystemPromptContext{
		Platform:            platform,
		RegisteredPluginIDs: []string{"tool/lark-cli"},
		EnabledPluginIDs:    nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(section.Content, "<name>lark-cli</name>") {
		t.Fatalf("expected disabled plugin-owned skill to be hidden: %s", section.Content)
	}
	if section.Title != "Skills" || strings.Contains(section.Content, "<name>lark-cli</name>") {
		t.Fatalf("expected plugin-owned builtin skill to be hidden: %#v", section)
	}

	section, err = BuildPromptSection(ctx, pkgplugins.SystemPromptContext{
		Platform:            platform,
		RegisteredPluginIDs: []string{"tool/lark-cli"},
		EnabledPluginIDs:    []string{"tool/lark-cli"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if section.Title != "Skills" {
		t.Fatalf("expected Skills section for enabled plugin-owned skill: %#v", section)
	}
	if !strings.Contains(section.Content, `action="search_installed"`) {
		t.Fatalf("expected search_installed instruction for enabled plugin-owned skill: %s", section.Content)
	}
	if !strings.Contains(section.Content, "<name>lark-cli</name>") {
		t.Fatalf("expected enabled plugin-owned system skill to be listed: %s", section.Content)
	}
}

func TestBuildPromptSectionFiltersRegistryPluginOwnedSkillByPluginState(t *testing.T) {
	store, _, _ := newTestSkillStore(t)
	platform := &skillStorePlatform{store: store}
	ctx := context.Background()

	section, err := BuildPromptSection(ctx, pkgplugins.SystemPromptContext{
		Platform:            platform,
		RegisteredPluginIDs: []string{"tool/lark-cli"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(section.Content, "<name>lark-cli</name>") {
		t.Fatalf("disabled Registry lark-cli leaked into prompt: %s", section.Content)
	}

	section, err = BuildPromptSection(ctx, pkgplugins.SystemPromptContext{
		Platform:            platform,
		RegisteredPluginIDs: []string{"tool/lark-cli"},
		EnabledPluginIDs:    []string{"tool/lark-cli"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(section.Content, "<name>lark-cli</name>") {
		t.Fatalf("enabled Registry lark-cli missing from prompt: %s", section.Content)
	}
}
