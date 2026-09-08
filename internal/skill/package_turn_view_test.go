package skill

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/plugin"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func TestPackageSkillTurnViewPinsCapturedResourceFiles(t *testing.T) {
	db := dbtest.New(t)
	const userID = "00000000-0000-4000-8000-000000000125"
	const agentID = "package-turn-agent"
	if _, err := db.Exec(t.Context(), `INSERT INTO auth_user(id,email) VALUES($1,'package-turn@example.invalid')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO agent(id,name,workspace) VALUES($1,'Package turn','')`, agentID); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	manager, err := home.NewWorkspaceManager(db, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	pluginDir := filepath.Join(base, "users", userID, ".agents", "plugins", "demo")
	if err := os.MkdirAll(filepath.Join(pluginDir, "skills", "docs", "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"demo"}`
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	main := "---\nname: docs\ndescription: package docs\n---\nold body\n"
	mainPath := filepath.Join(pluginDir, "skills", "docs", MainFile)
	attachmentPath := filepath.Join(pluginDir, "skills", "docs", "references", "guide.md")
	if err := os.WriteFile(mainPath, []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(attachmentPath, []byte("old attachment"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewFileStore(nil, manager)
	resources, err := plugin.DiscoverResources(t.Context(), store.resourceRoots(ViewContext{UserID: userID, AgentID: agentID}))
	if err != nil || len(resources) != 1 {
		t.Fatalf("resource capture = %#v, err=%v", resources, err)
	}
	resource := resources[0]
	ref := PackageSkillRef{PackageID: resource.Key.ID(), PackageDigest: resource.Digest, Name: "docs", Path: "skills/docs/SKILL.md", Description: "package docs"}
	captured, err := CapturePackageSkillRef(resource, ref)
	if err != nil {
		t.Fatal(err)
	}
	view, err := NewSkillTurnView(nil, nil, []PackageSkillRef{captured}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tool := newProjectionTool(t, store, projectionSession{tempVisible: "/tmp", tempHost: t.TempDir()}, allowAllSkillReads{}).
		WithPluginVisibility([]string{resource.Key.ID()}, []string{resource.Key.ID()})
	turnCtx := WithSkillTurnView(t.Context(), view)
	old, err := skillAction(tool, "load").Execute(turnCtx, map[string]any{"name": "docs", "path": "references/guide.md"})
	if err != nil || !strings.Contains(old, "old attachment") {
		t.Fatalf("captured package load = %q, %v", old, err)
	}
	if err := os.WriteFile(mainPath, []byte(strings.Replace(main, "old body", "new body", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(attachmentPath, []byte("new attachment"), 0o644); err != nil {
		t.Fatal(err)
	}
	newResources, err := plugin.DiscoverResources(t.Context(), store.resourceRoots(ViewContext{UserID: userID, AgentID: agentID}))
	if err != nil || len(newResources) != 1 {
		t.Fatalf("new resource capture = %#v, err=%v", newResources, err)
	}
	newCaptured, err := CapturePackageSkillRef(newResources[0], PackageSkillRef{
		PackageID: newResources[0].Key.ID(), PackageDigest: newResources[0].Digest,
		Name: "docs", Path: "skills/docs/SKILL.md", Description: "package docs",
	})
	if err != nil {
		t.Fatal(err)
	}
	newView, err := NewSkillTurnView(nil, nil, []PackageSkillRef{newCaptured}, nil)
	if err != nil {
		t.Fatal(err)
	}
	newTurnCtx := WithSkillTurnView(t.Context(), newView)
	newMain, err := skillAction(tool, "load").Execute(newTurnCtx, map[string]any{"name": "docs"})
	if err != nil || !strings.Contains(newMain, "new body") || strings.Contains(newMain, "old body") {
		t.Fatalf("new captured package load = %q, %v", newMain, err)
	}
	newAttachment, err := skillAction(tool, "load").Execute(newTurnCtx, map[string]any{"name": "docs", "path": "references/guide.md"})
	if err != nil || !strings.Contains(newAttachment, "new attachment") || strings.Contains(newAttachment, "old attachment") {
		t.Fatalf("new captured package attachment = %q, %v", newAttachment, err)
	}
	if err := os.RemoveAll(pluginDir); err != nil {
		t.Fatal(err)
	}
	stillOld, err := skillAction(tool, "load").Execute(turnCtx, map[string]any{"name": "docs"})
	if err != nil || !strings.Contains(stillOld, "old body") || strings.Contains(stillOld, "new body") {
		t.Fatalf("captured package changed after source deletion = %q, %v", stillOld, err)
	}
	newStillNew, err := skillAction(tool, "load").Execute(newTurnCtx, map[string]any{"name": "docs", "path": "references/guide.md"})
	if err != nil || !strings.Contains(newStillNew, "new attachment") || strings.Contains(newStillNew, "old attachment") {
		t.Fatalf("new captured package changed after source deletion = %q, %v", newStillNew, err)
	}

	reader := &countingPackageReader{}
	missingView, err := NewSkillTurnView(nil, nil, []PackageSkillRef{ref}, nil)
	if err != nil {
		t.Fatal(err)
	}
	missingTool := newProjectionTool(t, store, projectionSession{tempVisible: "/tmp", tempHost: t.TempDir()}, allowAllSkillReads{}).
		WithPluginVisibility([]string{resource.Key.ID()}, []string{resource.Key.ID()}).WithPackageReader(reader)
	_, err = skillAction(missingTool, "load").Execute(WithSkillTurnView(t.Context(), missingView), map[string]any{"name": "docs"})
	if !errors.Is(err, ErrInvalidSkillRevision) || reader.calls != 0 {
		t.Fatalf("missing file package capture = %v, reader calls=%d; want fail closed without reader", err, reader.calls)
	}

	wrongOwner := captured
	wrongOwner.PackageID = plugin.ResourceKey{Scope: plugin.ScopeUser, UserID: "00000000-0000-4000-8000-000000000126", Kind: plugin.ResourcePlugin, Name: "demo"}.ID()
	if _, err := CapturePackageSkillRef(resource, wrongOwner); !errors.Is(err, ErrInvalidSkillRevision) {
		t.Fatalf("wrong owner error = %v, want ErrInvalidSkillRevision", err)
	}
	wrongDigest := captured
	wrongDigest.PackageDigest = "sha256:" + strings.Repeat("f", 64)
	if _, err := CapturePackageSkillRef(resource, wrongDigest); !errors.Is(err, ErrInvalidSkillRevision) {
		t.Fatalf("wrong digest error = %v, want ErrInvalidSkillRevision", err)
	}
	forgedResource := resource
	forgedResource.Digest = "sha256:" + strings.Repeat("e", 64)
	forgedDigest := captured
	forgedDigest.PackageDigest = forgedResource.Digest
	if _, err := CapturePackageSkillRef(forgedResource, forgedDigest); !errors.Is(err, ErrInvalidSkillRevision) {
		t.Fatalf("resource digest mismatch error = %v, want ErrInvalidSkillRevision", err)
	}
	wrongPath := captured
	wrongPath.Path = "skills/other/SKILL.md"
	if _, err := CapturePackageSkillRef(resource, wrongPath); !errors.Is(err, ErrInvalidSkillRevision) {
		t.Fatalf("wrong path error = %v, want ErrInvalidSkillRevision", err)
	}
	masked := ref
	masked.Masked = true
	maskedResource := resource
	maskedResource.Content = nil
	maskedResource.Package = nil
	maskedCaptured, err := CapturePackageSkillRef(maskedResource, masked)
	if err != nil || maskedCaptured.captured != nil || !maskedCaptured.Masked {
		t.Fatalf("masked package capture = %#v, %v; want evidence-only ref", maskedCaptured, err)
	}
}

type revocablePackageReads struct {
	denied bool
	calls  []string
}

func (r *revocablePackageReads) BeginRead(context.Context) (SkillReadDecision, error) { return r, nil }

func (r *revocablePackageReads) AllowRead(_ context.Context, id, scope, userID, agentID string) (bool, error) {
	r.calls = append(r.calls, fmt.Sprintf("%s|%s|%s|%s", id, scope, userID, agentID))
	return !r.denied, nil
}

func TestFilePackageTurnRechecksReadPEPAfterCapture(t *testing.T) {
	db := dbtest.New(t)
	const userID = "00000000-0000-4000-8000-000000000127"
	const agentID = "package-pep-agent"
	if _, err := db.Exec(t.Context(), `INSERT INTO auth_user(id,email) VALUES($1,'package-pep@example.invalid')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO agent(id,name,workspace) VALUES($1,'Package PEP','')`, agentID); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	manager, err := home.NewWorkspaceManager(db, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	pluginDir := filepath.Join(base, "users", userID, ".agents", "plugins", "pep-demo")
	if err := os.MkdirAll(filepath.Join(pluginDir, "skills", "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"pep-demo"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "skills", "docs", MainFile), []byte("---\nname: docs\ndescription: package docs\n---\npep bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewFileStore(nil, manager)
	resources, err := plugin.DiscoverResources(t.Context(), store.resourceRoots(ViewContext{UserID: userID, AgentID: agentID}))
	if err != nil || len(resources) != 1 {
		t.Fatalf("resource capture = %#v, err=%v", resources, err)
	}
	ref, err := CapturePackageSkillRef(resources[0], PackageSkillRef{
		PackageID: resources[0].Key.ID(), PackageDigest: resources[0].Digest,
		Name: "docs", Path: "skills/docs/SKILL.md", Description: "package docs",
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := NewSkillTurnView(nil, nil, []PackageSkillRef{ref}, nil)
	if err != nil {
		t.Fatal(err)
	}
	authorizer := &revocablePackageReads{}
	tool := newProjectionTool(t, store, projectionSession{tempVisible: "/tmp", tempHost: t.TempDir()}, authorizer).
		WithPluginVisibility(nil, nil)
	ctx := WithSkillTurnView(t.Context(), view)
	if err := os.RemoveAll(pluginDir); err != nil {
		t.Fatal(err)
	}
	search, err := skillAction(tool, "search").Execute(ctx, map[string]any{"q": "package docs"})
	if err != nil || !strings.Contains(search, `"name": "docs"`) {
		t.Fatalf("authorized file package search = %q, %v", search, err)
	}
	loaded, err := skillAction(tool, "load").Execute(ctx, map[string]any{"name": "docs"})
	if err != nil || !strings.Contains(loaded, "pep bytes") {
		t.Fatalf("authorized file package load = %q, %v", loaded, err)
	}
	if len(authorizer.calls) < 2 || !strings.HasPrefix(authorizer.calls[0], resources[0].Key.ID()+"|user|"+userID+"|") {
		t.Fatalf("file package read PEP calls = %v, want canonical resource owner", authorizer.calls)
	}
	beforePrompt := len(authorizer.calls)
	if _, err := BuildAuthorizedPromptSection(ctx, pkgplugins.SystemPromptContext{}, nil, &projectionReader{}, authorizer); err != nil {
		t.Fatal(err)
	}
	if len(authorizer.calls) <= beforePrompt {
		t.Fatalf("prompt did not recheck file package PEP: calls=%v", authorizer.calls)
	}

	authorizer.denied = true
	search, err = skillAction(tool, "search").Execute(ctx, map[string]any{"q": "package docs"})
	if err != nil || search != noInstalledSkills {
		t.Fatalf("revoked file package search = %q, %v; want hidden", search, err)
	}
	if out, err := skillAction(tool, "load").Execute(ctx, map[string]any{"name": "docs"}); out != "" || !errors.Is(err, errSkillNotFound) {
		t.Fatalf("revoked file package load = %q, %v; want hidden", out, err)
	}
	prompt, err := BuildAuthorizedPromptSection(ctx, pkgplugins.SystemPromptContext{}, nil, &projectionReader{}, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	if prompt.Title != "" || prompt.Content != "" {
		t.Fatalf("revoked file package prompt = %#v; want empty", prompt)
	}
}

type countingPackageReader struct{ calls int }

func (r *countingPackageReader) LoadPackageSkill(context.Context, PackageSkillRef) (PackageSkillRevision, error) {
	r.calls++
	return PackageSkillRevision{}, errors.New("poison legacy package reader")
}
