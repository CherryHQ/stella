package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	mcpskills "github.com/vaayne/mcphub/pkg/skills"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// TestRemoteCatalogSearchToInstalledUpgradeLifecycle covers both advertised
// catalog mappings and carries a skills.sh-style Git source through fetch,
// canonical persistence, runtime load, upgrade, and stale-file pruning.
func TestRemoteCatalogSearchToInstalledUpgradeLifecycle(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	repoPath, repository := newSkillGitRepository(t)
	source := "file://" + filepath.ToSlash(repoPath)

	results, errs := searchRemoteCatalogs(
		context.Background(),
		"contract",
		10,
		func(_ context.Context, query string, limit int) ([]clawhubSearchResult, error) {
			if query != "contract" || limit != 10 {
				return nil, fmt.Errorf("clawhub query=%q limit=%d", query, limit)
			}
			return []clawhubSearchResult{{
				Slug:        "clawhub-contract",
				DisplayName: "ClawHub Contract",
				Summary:     "ClawHub mapping",
			}}, nil
		},
		func(_ context.Context, query string, limit int) ([]mcpskills.SearchResult, error) {
			if query != "contract" || limit != 10 {
				return nil, fmt.Errorf("skills.sh query=%q limit=%d", query, limit)
			}
			return []mcpskills.SearchResult{{
				Name:   "Remote Contract",
				Source: source,
			}}, nil
		},
	)
	if len(errs) != 0 {
		t.Fatalf("catalog errors = %v, want none", errs)
	}
	if len(results) != 2 {
		t.Fatalf("catalog results = %+v, want two providers", results)
	}
	var selected, clawhub skillSearchResult
	for _, result := range results {
		switch result.Provider {
		case "skills.sh":
			selected = result
		case "clawhub":
			clawhub = result
		}
	}
	if selected.Source != source {
		t.Fatalf("skills.sh source = %q, want %q", selected.Source, source)
	}
	if clawhub.Name != "clawhub-contract" || clawhub.Source != "clawhub:clawhub-contract" || clawhub.Description != "ClawHub mapping" {
		t.Fatalf("clawhub result = %+v, want normalized slug, source, and summary", clawhub)
	}

	pluginStore, userID, agentID := newTestSkillStore(t)
	store := New(pluginStore.db)
	ctx := context.Background()
	snapshot, err := InstallToStore(ctx, store, selected.Source, "user", userID, agentID)
	if err != nil {
		t.Fatalf("InstallToStore: %v", err)
	}
	if snapshot.Skill.Name != "remote-contract" {
		t.Fatalf("installed skill name = %q, want remote-contract", snapshot.Skill.Name)
	}

	service := NewService(pluginStore, "")
	view := pkgplugins.SkillViewContext{UserID: userID, AgentID: agentID}
	main, _, resolved, err := service.LoadFile(ctx, "remote-contract", MainFile, view, "")
	if err != nil {
		t.Fatalf("load installed SKILL.md: %v", err)
	}
	if resolved == nil || !strings.Contains(main, "version one") {
		t.Fatalf("loaded installed skill = %#v content=%q", resolved, main)
	}
	reference, _, _, err := service.LoadFile(ctx, "remote-contract", "references/old.md", view, "")
	if err != nil || reference != "old reference\n" {
		t.Fatalf("load installed reference = %q, err=%v", reference, err)
	}

	var metadata map[string]string
	if err := json.Unmarshal(snapshot.Skill.Metadata, &metadata); err != nil {
		t.Fatalf("decode install metadata: %v", err)
	}
	if metadata["source"] != source || metadata["version"] == "" {
		t.Fatalf("install metadata = %v, want source and resolved commit", metadata)
	}

	updateSkillGitRepository(t, repository, repoPath)
	upgrade, err := UpgradeInStore(ctx, pluginStore, snapshot.Skill.ID, snapshot.Skill.Metadata)
	if err != nil {
		t.Fatalf("UpgradeInStore: %v", err)
	}
	if !upgrade.Updated || upgrade.PreviousVersion == upgrade.Version {
		t.Fatalf("upgrade result = %+v, want changed version", upgrade)
	}

	main, _, _, err = service.LoadFile(ctx, "remote-contract", MainFile, view, "")
	if err != nil || !strings.Contains(main, "version two") {
		t.Fatalf("load upgraded SKILL.md = %q, err=%v", main, err)
	}
	files, _, err := service.ListFiles(ctx, "remote-contract", view, "")
	if err != nil {
		t.Fatalf("list upgraded files: %v", err)
	}
	if containsString(files, "references/old.md") || !containsString(files, "references/new.md") {
		t.Fatalf("upgraded files = %v, want new reference and no stale old reference", files)
	}
}

// newSkillGitRepository creates a real local Git remote so the contract uses
// go-git's clone/update path without an external network or marketplace account.
func newSkillGitRepository(t *testing.T) (string, *git.Repository) {
	t.Helper()
	path := t.TempDir()
	repository, err := git.PlainInit(path, false)
	if err != nil {
		t.Fatalf("init skill repository: %v", err)
	}
	writeSkillFixture(t, path, "version one", "references/old.md", "old reference\n")
	commitAll(t, repository, "initial skill")
	return path, repository
}

func updateSkillGitRepository(t *testing.T, repository *git.Repository, path string) {
	t.Helper()
	if err := os.Remove(filepath.Join(path, "references", "old.md")); err != nil {
		t.Fatalf("remove old reference: %v", err)
	}
	writeSkillFixture(t, path, "version two", "references/new.md", "new reference\n")
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatalf("skill worktree: %v", err)
	}
	if _, err := worktree.Remove("references/old.md"); err != nil {
		t.Fatalf("stage old reference removal: %v", err)
	}
	commitAll(t, repository, "upgrade skill")
}

func writeSkillFixture(t *testing.T, root, version, referencePath, reference string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "references"), 0o755); err != nil {
		t.Fatalf("create references: %v", err)
	}
	main := "---\nname: remote-contract\ndescription: Deterministic remote contract\n---\n\n# Remote Contract\n\n" + version + "\n"
	if err := os.WriteFile(filepath.Join(root, MainFile), []byte(main), 0o600); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(referencePath)), []byte(reference), 0o600); err != nil {
		t.Fatalf("write reference: %v", err)
	}
}

func commitAll(t *testing.T, repository *git.Repository, message string) {
	t.Helper()
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatalf("skill worktree: %v", err)
	}
	if err := worktree.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatalf("stage skill repository: %v", err)
	}
	_, err = worktree.Commit(message, &git.CommitOptions{Author: &object.Signature{
		Name:  "Stella Contract",
		Email: "contract@stella.test",
		When:  time.Now().UTC(),
	}})
	if err != nil {
		t.Fatalf("commit skill repository: %v", err)
	}
}

func containsString(values []string, want string) bool {
	return slices.Contains(values, want)
}
