package delegate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTestFile(t *testing.T, path string, data []byte, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, perm); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func mkdirAll(t *testing.T, path string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(path, perm); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
}

func TestParseAgentFrontmatter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		content  string
		wantFM   delegateFrontmatter
		wantBody string
		wantErr  bool
	}{
		{
			name:     "basic frontmatter",
			content:  "---\nname: test-agent\ndescription: A test agent\n---\nSystem prompt body.",
			wantFM:   delegateFrontmatter{Name: "test-agent", Description: "A test agent"},
			wantBody: "\nSystem prompt body.",
		},
		{
			name:    "with tools and model",
			content: "---\nname: coder\ndescription: Code helper\nmodel: claude-haiku\ntools:\n  - read\n  - edit\n---\nYou are a coder.",
			wantFM: delegateFrontmatter{
				Name:        "coder",
				Description: "Code helper",
				Model:       "claude-haiku",
				Tools:       []string{"read", "edit"},
				HasTools:    true,
			},
			wantBody: "\nYou are a coder.",
		},
		{
			name:    "empty tools explicitly set",
			content: "---\nname: reader\ndescription: Read only\ntools: []\n---\nBody.",
			wantFM: delegateFrontmatter{
				Name:        "reader",
				Description: "Read only",
				HasTools:    true,
			},
			wantBody: "\nBody.",
		},
		{
			name:    "no frontmatter",
			content: "Just plain text.",
			wantErr: true,
		},
		{
			name:    "unclosed frontmatter",
			content: "---\nname: broken\n",
			wantErr: true,
		},
		{
			name:    "with timeout",
			content: "---\nname: slow\ndescription: Slow agent\ntimeout: 5m\n---\nSlow body.",
			wantFM: delegateFrontmatter{
				Name:        "slow",
				Description: "Slow agent",
				Timeout:     "5m",
			},
			wantBody: "\nSlow body.",
		},
		{
			name:    "crlf line endings",
			content: "---\r\nname: win\r\ndescription: Windows agent\r\n---\r\nWindows body.",
			wantFM: delegateFrontmatter{
				Name:        "win",
				Description: "Windows agent",
			},
			wantBody: "\nWindows body.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fm, body, err := parseDelegateFrontmatter(tt.content)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if fm.Name != tt.wantFM.Name {
				t.Errorf("Name = %q, want %q", fm.Name, tt.wantFM.Name)
			}
			if fm.Description != tt.wantFM.Description {
				t.Errorf("Description = %q, want %q", fm.Description, tt.wantFM.Description)
			}
			if fm.Model != tt.wantFM.Model {
				t.Errorf("Model = %q, want %q", fm.Model, tt.wantFM.Model)
			}
			if fm.HasTools != tt.wantFM.HasTools {
				t.Errorf("HasTools = %v, want %v", fm.HasTools, tt.wantFM.HasTools)
			}
			if fm.Timeout != tt.wantFM.Timeout {
				t.Errorf("Timeout = %q, want %q", fm.Timeout, tt.wantFM.Timeout)
			}
			if body != tt.wantBody {
				t.Errorf("body = %q, want %q", body, tt.wantBody)
			}
		})
	}
}

func TestLoadPresetFromFile(t *testing.T) {
	t.Parallel()

	t.Run("valid preset", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "test-agent.md")
		content := "---\nname: test-agent\ndescription: A test agent\nmodel: claude-haiku\ntimeout: 2m\n---\nYou are a test agent."
		writeTestFile(t, path, []byte(content), 0o644)

		p, ok := loadPresetFromFile(context.Background(), path, "project")
		if !ok {
			t.Fatal("expected ok")
		}
		if p.Name != "test-agent" {
			t.Errorf("Name = %q", p.Name)
		}
		if p.Description != "A test agent" {
			t.Errorf("Description = %q", p.Description)
		}
		if p.Model != "claude-haiku" {
			t.Errorf("Model = %q", p.Model)
		}
		if p.Timeout != 2*time.Minute {
			t.Errorf("Timeout = %v", p.Timeout)
		}
		if p.System != "You are a test agent." {
			t.Errorf("System = %q", p.System)
		}
		if p.Source != "project" {
			t.Errorf("Source = %q", p.Source)
		}
	})

	t.Run("name from filename", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "my-agent.md")
		content := "---\ndescription: Nameless agent\n---\nBody."
		writeTestFile(t, path, []byte(content), 0o644)

		p, ok := loadPresetFromFile(context.Background(), path, "common")
		if !ok {
			t.Fatal("expected ok")
		}
		if p.Name != "my-agent" {
			t.Errorf("Name = %q, want %q", p.Name, "my-agent")
		}
	})

	t.Run("missing description rejected", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "no-desc.md")
		content := "---\nname: bad\n---\nBody."
		writeTestFile(t, path, []byte(content), 0o644)

		_, ok := loadPresetFromFile(context.Background(), path, "project")
		if ok {
			t.Fatal("expected rejection for missing description")
		}
	})

	t.Run("invalid timeout rejected", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "bad-timeout.md")
		content := "---\nname: bad\ndescription: Bad timeout\ntimeout: not-a-duration\n---\nBody."
		writeTestFile(t, path, []byte(content), 0o644)

		_, ok := loadPresetFromFile(context.Background(), path, "project")
		if ok {
			t.Fatal("expected rejection for invalid timeout")
		}
	})

	t.Run("nonexistent file", func(t *testing.T) {
		t.Parallel()
		_, ok := loadPresetFromFile(context.Background(), "/nonexistent/path.md", "project")
		if ok {
			t.Fatal("expected failure for nonexistent file")
		}
	})
}

func TestLoadPresetsFromDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Create two valid preset files.
	writeTestFile(t, filepath.Join(dir, "alpha.md"), []byte("---\nname: alpha\ndescription: Alpha agent\n---\nAlpha body."), 0o644)
	writeTestFile(t, filepath.Join(dir, "beta.md"), []byte("---\nname: beta\ndescription: Beta agent\n---\nBeta body."), 0o644)

	// Create a non-md file (should be ignored).
	writeTestFile(t, filepath.Join(dir, "readme.txt"), []byte("not a preset"), 0o644)

	// Create a hidden file (should be ignored).
	writeTestFile(t, filepath.Join(dir, ".hidden.md"), []byte("---\nname: hidden\ndescription: Hidden\n---\nBody."), 0o644)

	// Create a subdirectory (should be ignored).
	mkdirAll(t, filepath.Join(dir, "subdir"), 0o755)

	presets := loadPresetsFromDir(context.Background(), dir, "test")
	if len(presets) != 2 {
		t.Fatalf("expected 2 presets, got %d", len(presets))
	}

	names := map[string]bool{}
	for _, p := range presets {
		names[p.Name] = true
		if p.Source != "test" {
			t.Errorf("preset %q Source = %q, want %q", p.Name, p.Source, "test")
		}
	}
	if !names["alpha"] || !names["beta"] {
		t.Errorf("expected alpha and beta, got %v", names)
	}
}

func TestLoadPresetsFromDirNonexistent(t *testing.T) {
	t.Parallel()
	presets := loadPresetsFromDir(context.Background(), "/nonexistent/dir", "test")
	if len(presets) != 0 {
		t.Errorf("expected 0 presets for nonexistent dir, got %d", len(presets))
	}
}

func TestLoadDelegatePresetsDeduplication(t *testing.T) {
	t.Parallel()

	// Create two directories with overlapping preset names.
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	mkdirAll(t, filepath.Join(dir1, ".agents", "delegates"), 0o755)
	mkdirAll(t, filepath.Join(dir2, ".agents", "delegates"), 0o755)

	// Same name in both — project should win.
	writeTestFile(t, filepath.Join(dir1, ".agents", "delegates", "shared.md"),
		[]byte("---\nname: shared\ndescription: Project version\n---\nProject body."), 0o644)
	writeTestFile(t, filepath.Join(dir2, ".agents", "delegates", "shared.md"),
		[]byte("---\nname: shared\ndescription: Agent version\n---\nAgent body."), 0o644)

	// Unique to workspace.
	writeTestFile(t, filepath.Join(dir2, ".agents", "delegates", "unique.md"),
		[]byte("---\nname: unique\ndescription: Only in workspace\n---\nUnique body."), 0o644)

	presets := loadDelegatePresets(context.Background(), "", dir2, "", dir1)
	if len(presets) != 2 {
		t.Fatalf("expected 2 presets, got %d", len(presets))
	}

	// "shared" should come from project (higher priority).
	for _, p := range presets {
		if p.Name == "shared" && p.Source != "project" {
			t.Errorf("shared preset Source = %q, want %q", p.Source, "project")
		}
	}
}

func TestPresetRegistry(t *testing.T) {
	t.Parallel()

	presets := []DelegatePreset{
		{Name: "alpha", Description: "Alpha"},
		{Name: "beta", Description: "Beta"},
	}
	reg := NewPresetRegistry(presets)

	if names := reg.Names(); len(names) != 2 || names[0] != "alpha" || names[1] != "beta" {
		t.Errorf("Names() = %v", names)
	}

	if p, ok := reg.Lookup("alpha"); !ok || p.Description != "Alpha" {
		t.Errorf("Lookup(alpha) = %v, %v", p, ok)
	}

	if _, ok := reg.Lookup("nonexistent"); ok {
		t.Error("expected Lookup(nonexistent) to return false")
	}

	if all := reg.All(); len(all) != 2 {
		t.Errorf("All() len = %d", len(all))
	}
}

func TestLoadDelegatePresetsAllFourTiers(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	agentRoot := t.TempDir()
	userRoot := filepath.Join(t.TempDir(), "users", "7")
	stellaHome := t.TempDir()

	mkdirAll(t, filepath.Join(cwd, ".agents", "delegates"), 0o755)
	mkdirAll(t, filepath.Join(agentRoot, ".agents", "delegates"), 0o755)
	mkdirAll(t, filepath.Join(userRoot, ".agents", "delegates"), 0o755)
	mkdirAll(t, filepath.Join(stellaHome, ".agents", "delegates"), 0o755)

	preset := func(name, desc string) []byte {
		return []byte("---\nname: " + name + "\ndescription: " + desc + "\n---\nBody.")
	}

	writeTestFile(t, filepath.Join(stellaHome, ".agents", "delegates", "stella-only.md"), preset("stella-only", "From stella"), 0o644)
	writeTestFile(t, filepath.Join(stellaHome, ".agents", "delegates", "overlap.md"), preset("overlap", "Stella loses"), 0o644)

	writeTestFile(t, filepath.Join(agentRoot, ".agents", "delegates", "agent-only.md"), preset("agent-only", "From agent root"), 0o644)
	writeTestFile(t, filepath.Join(agentRoot, ".agents", "delegates", "overlap.md"), preset("overlap", "Agent loses"), 0o644)

	writeTestFile(t, filepath.Join(userRoot, ".agents", "delegates", "user-only.md"), preset("user-only", "From user root"), 0o644)
	writeTestFile(t, filepath.Join(userRoot, ".agents", "delegates", "overlap.md"), preset("overlap", "User loses"), 0o644)

	writeTestFile(t, filepath.Join(cwd, ".agents", "delegates", "project-only.md"), preset("project-only", "From project"), 0o644)
	writeTestFile(t, filepath.Join(cwd, ".agents", "delegates", "overlap.md"), preset("overlap", "Project wins"), 0o644)

	presets := loadDelegatePresets(context.Background(), stellaHome, agentRoot, userRoot, cwd)
	if len(presets) != 5 {
		t.Fatalf("expected 5 presets, got %d", len(presets))
	}

	byName := map[string]DelegatePreset{}
	for _, p := range presets {
		byName[p.Name] = p
	}
	if byName["stella-only"].Source != "stella" {
		t.Errorf("stella-only Source = %q", byName["stella-only"].Source)
	}
	if byName["agent-only"].Source != "agent" {
		t.Errorf("agent-only Source = %q", byName["agent-only"].Source)
	}
	if byName["user-only"].Source != "user" {
		t.Errorf("user-only Source = %q", byName["user-only"].Source)
	}
	if byName["project-only"].Source != "project" {
		t.Errorf("project-only Source = %q", byName["project-only"].Source)
	}
	if byName["overlap"].Source != "project" {
		t.Errorf("overlap Source = %q, want %q", byName["overlap"].Source, "project")
	}
	if byName["overlap"].Description != "Project wins" {
		t.Errorf("overlap Description = %q, want %q", byName["overlap"].Description, "Project wins")
	}
}

func TestLoadDelegatePresets_publicWrapper(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	delegatesDir := filepath.Join(dir, ".agents", "delegates")
	mkdirAll(t, delegatesDir, 0o755)
	writeTestFile(t, filepath.Join(delegatesDir, "helper.md"),
		[]byte("---\nname: helper\ndescription: A helper delegate\n---\nHelp the user."), 0o644)

	presets := LoadDelegatePresets(LoadDelegatePresetsConfig{StellaHome: dir})
	if len(presets) != 1 {
		t.Fatalf("expected 1 preset, got %d", len(presets))
	}
	if presets[0].Name != "helper" {
		t.Errorf("Name = %q, want helper", presets[0].Name)
	}
}

func TestLoadDelegatePresetsPathDedup(t *testing.T) {
	t.Parallel()

	// Pass the same dir as both stellaHome and agentRoot; both scan
	// .agents/delegates/ so filepath.Abs dedup should load it once.
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, ".agents", "delegates"), 0o755)
	writeTestFile(t, filepath.Join(dir, ".agents", "delegates", "test.md"),
		[]byte("---\nname: test\ndescription: Test delegate\n---\nBody."), 0o644)

	presets := loadDelegatePresets(context.Background(), dir, dir, "", "")
	if len(presets) != 1 {
		t.Errorf("expected 1 preset (dedup), got %d", len(presets))
	}
}
