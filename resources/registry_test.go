package resources

import (
	"io/fs"
	"path"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"gopkg.in/yaml.v3"
)

func TestDefaultLoadsBuiltinResources(t *testing.T) {
	r, err := Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}

	stella, ok := r.Get(KindSkill, "stella")
	if !ok {
		t.Fatal("expected builtin skill 'stella' to be loaded")
	}
	if stella.Name != "stella" {
		t.Errorf("skill name = %q, want %q", stella.Name, "stella")
	}
	if stella.Hash == "" {
		t.Error("skill hash is empty")
	}

	if _, ok := r.Get(KindSoul, "stella"); !ok {
		t.Error("expected builtin soul 'stella'")
	}
	if _, ok := r.Get(KindTemplate, "stella"); !ok {
		t.Error("expected builtin template 'stella'")
	}
	for _, id := range []string{"coder"} {
		if _, ok := r.Get(KindDelegate, id); !ok {
			t.Errorf("expected builtin delegate %q", id)
		}
	}
}

func TestLoadWithFixture(t *testing.T) {
	fs := fstest.MapFS{
		"skills/demo/SKILL.md":           &fstest.MapFile{Data: []byte("---\nname: demo\ndescription: Demo skill\ntags: [x, y]\n---\nbody\n")},
		"skills/system/tap-web/SKILL.md": &fstest.MapFile{Data: []byte("---\nname: tap-web\ndescription: Tap Web\n---\nbody\n")},
		"skills/system/tap-web/ref.md":   &fstest.MapFile{Data: []byte("ref\n")},
		"souls/terse.md":                 &fstest.MapFile{Data: []byte("---\nid: terse\nname: Terse\n---\nshort\n")},
		"delegates/runner.md":            &fstest.MapFile{Data: []byte("---\nname: runner\ntools: [bash]\nmax_turns: 5\n---\ngo\n")},
		"templates/blank.md":             &fstest.MapFile{Data: []byte("---\nid: blank\nname: Blank\nsoul_id: terse\n---\n")},
	}

	r, err := Load(fs)
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}

	demo, ok := r.Get(KindSkill, "demo")
	if !ok {
		t.Fatal("demo skill missing")
	}
	if _, ok := r.Get(KindSkill, "tap-web"); !ok {
		t.Fatal("nested tap-web skill missing")
	}
	if got := demo.Tags; len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Errorf("tags = %v, want [x y]", got)
	}

	runner, ok := r.Get(KindDelegate, "runner")
	if !ok {
		t.Fatal("runner delegate missing")
	}
	if runner.Metadata["max_turns"] != 5 {
		t.Errorf("max_turns = %v, want 5", runner.Metadata["max_turns"])
	}

	tpl, ok := r.Get(KindTemplate, "blank")
	if !ok {
		t.Fatal("blank template missing")
	}
	if tpl.Metadata["soul_id"] != "terse" {
		t.Errorf("soul_id = %v, want terse", tpl.Metadata["soul_id"])
	}
}

func TestListIsSortedByID(t *testing.T) {
	r, err := Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}
	list := r.List(KindDelegate)
	for i := 1; i < len(list); i++ {
		if list[i-1].ID > list[i].ID {
			t.Errorf("list not sorted: %q > %q", list[i-1].ID, list[i].ID)
		}
	}
}

// TestBuiltinSystemSkillConformance keeps the ten bundled system skills on one
// shared static contract. External command execution belongs to the separate
// Live scenario; this test proves each shipped skill declares a usable
// entrypoint, its prerequisite boundary, an observable result, and valid files.
func TestBuiltinSystemSkillConformance(t *testing.T) {
	type contract struct {
		id            string
		binary        string
		entrypoint    string
		prerequisite  string
		observable    string
		requiredFiles []string
		forbidden     []string
	}
	contracts := []contract{
		{
			id:           "email",
			entrypoint:   "native `email` tool",
			prerequisite: "Settings → Email",
			observable:   "duplicate-send suppression",
		},
		{
			id:           "html-artifact",
			entrypoint:   "One `.html` file",
			prerequisite: "self-contained",
			observable:   "Verify by opening in a browser",
		},
		{
			id:           "lark-cli",
			binary:       "lark-cli",
			entrypoint:   "`lark-cli`",
			prerequisite: "lark-cli itself owns setup, authorization, and refresh",
			observable:   "[event] ready",
			requiredFiles: []string{
				"references/lark-shared.md",
				"references/lark-event.md",
			},
			forbidden: []string{
				"LARKSUITE_CLI_USER_ACCESS_TOKEN",
			},
		},
		{
			id:           "python-script",
			binary:       "uv",
			entrypoint:   "`uv run --script",
			prerequisite: "Confirm inputs, outputs",
			observable:   ".agents/logs/",
			requiredFiles: []string{
				"references/script-template.md",
			},
		},
		{
			id:           "recally",
			entrypoint:   "native `recally` tool",
			prerequisite: "Fetch content first",
			observable:   "partial failures return per-item errors",
			requiredFiles: []string{
				"references/save-workflow.md",
				"references/rss-workflow.md",
				"references/twitter-workflow.md",
				"references/website-workflow.md",
			},
		},
		{
			id:           "scheduler",
			entrypoint:   "native `scheduler` tool",
			prerequisite: "Scheduler must be enabled",
			observable:   "report the existing job",
		},
		{
			id:           "skill-creator",
			entrypoint:   "python -m scripts.package_skill",
			prerequisite: "success criteria, and dependencies",
			observable:   "grading.json",
			requiredFiles: []string{
				"scripts/quick_validate.py",
				"scripts/package_skill.py",
				"eval-viewer/generate_review.py",
				"agents/grader.md",
			},
		},
		{
			id:           "stella",
			entrypoint:   "`stellad server`",
			prerequisite: "PostgreSQL",
			observable:   "Results are returned as JSON",
			requiredFiles: []string{
				"references/configuration.md",
				"references/update.md",
				"references/goals.md",
			},
		},
		{
			id:           "tap-web",
			binary:       "tap",
			entrypoint:   "`tap fetch`",
			prerequisite: "If `tap` is missing",
			observable:   "fails explicitly",
			requiredFiles: []string{
				"references/browser.md",
				"references/network.md",
				"references/script-development.md",
				"references/site-notes.md",
			},
		},
		{
			id:           "xberg",
			binary:       "xberg",
			entrypoint:   "xberg extract",
			prerequisite: "ONNX Runtime",
			observable:   "stdout (text) or JSON",
			requiredFiles: []string{
				"references/supported-formats.md",
			},
		},
	}

	registry, err := Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}
	gotIDs := make([]string, 0, len(registry.List(KindSkill)))
	for _, skill := range registry.List(KindSkill) {
		gotIDs = append(gotIDs, skill.ID)
	}
	wantIDs := make([]string, 0, len(contracts))
	for _, item := range contracts {
		wantIDs = append(wantIDs, item.id)
	}
	sort.Strings(gotIDs)
	sort.Strings(wantIDs)
	if strings.Join(gotIDs, "\x00") != strings.Join(wantIDs, "\x00") {
		t.Fatalf("builtin system skills = %v, conformance table = %v", gotIDs, wantIDs)
	}

	binaries := builtinManifestBinaries(t)
	for _, item := range contracts {
		t.Run(item.id, func(t *testing.T) {
			skill, ok := registry.Get(KindSkill, item.id)
			if !ok {
				t.Fatalf("registry missing %q", item.id)
			}
			if skill.Name != item.id || strings.TrimSpace(skill.Description) == "" || strings.TrimSpace(skill.Content) == "" {
				t.Fatalf("invalid registry resource: %+v", skill)
			}
			if item.binary != "" && !binaries[item.binary] {
				t.Fatalf("declared binary %q has no builtin manifest provisioner", item.binary)
			}

			root := path.Join("skills", "system", item.id)
			allText := readSkillTree(t, root)
			for label, marker := range map[string]string{
				"entrypoint":   item.entrypoint,
				"prerequisite": item.prerequisite,
				"observable":   item.observable,
			} {
				if !strings.Contains(allText, marker) {
					t.Errorf("%s contract missing marker %q", label, marker)
				}
			}
			for _, relative := range item.requiredFiles {
				fullPath := path.Join(root, relative)
				info, err := fs.Stat(fsys, fullPath)
				if err != nil {
					t.Errorf("required file %q: %v", relative, err)
					continue
				}
				if !info.Mode().IsRegular() {
					t.Errorf("required file %q is not regular", relative)
				}
			}
			for _, marker := range item.forbidden {
				if strings.Contains(allText, marker) {
					t.Errorf("skill contract retains forbidden legacy marker %q", marker)
				}
			}
		})
	}
}

func builtinManifestBinaries(t *testing.T) map[string]bool {
	t.Helper()
	var manifest struct {
		Plugins []struct {
			Binaries []struct {
				Name string `yaml:"name"`
			} `yaml:"binaries"`
		} `yaml:"plugins"`
	}
	if err := yaml.Unmarshal(BuiltinToolsYAML(), &manifest); err != nil {
		t.Fatalf("parse builtin tool manifest: %v", err)
	}
	out := map[string]bool{}
	for _, plugin := range manifest.Plugins {
		for _, binary := range plugin.Binaries {
			out[binary.Name] = true
		}
	}
	return out
}

func readSkillTree(t *testing.T, root string) string {
	t.Helper()
	var text strings.Builder
	err := fs.WalkDir(fsys, root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(fsys, filePath)
		if err != nil {
			return err
		}
		text.Write(data)
		text.WriteByte('\n')
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return text.String()
}
