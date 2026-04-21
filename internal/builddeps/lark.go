package builddeps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"gopkg.in/yaml.v3"
)

const larkCLIRepo = "https://github.com/larksuite/cli"

type larkSkillMeta struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Metadata    map[string]any `yaml:"metadata"`
}

type larkSkillDoc struct {
	Name        string
	Description string
	File        string
}

func SyncSystemSkills(ctx context.Context, cfg Config) error {
	if err := syncLarkSkill(ctx, cfg); err != nil {
		return err
	}
	return nil
}

func syncLarkSkill(ctx context.Context, cfg Config) error {
	tmpDir, err := os.MkdirTemp("", "anna-lark-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	repoDir := filepath.Join(tmpDir, "repo")
	repo, err := git.PlainCloneContext(ctx, repoDir, false, &git.CloneOptions{URL: larkCLIRepo})
	if err != nil {
		return fmt.Errorf("clone %s: %w", larkCLIRepo, err)
	}
	if cfg.LarkRef != "" {
		hash, err := repo.ResolveRevision(plumbing.Revision(cfg.LarkRef))
		if err != nil {
			return fmt.Errorf("resolve lark ref %q: %w", cfg.LarkRef, err)
		}
		wt, err := repo.Worktree()
		if err != nil {
			return fmt.Errorf("open lark worktree: %w", err)
		}
		if err := wt.Checkout(&git.CheckoutOptions{Hash: *hash, Force: true}); err != nil {
			return fmt.Errorf("checkout lark ref %q: %w", cfg.LarkRef, err)
		}
	}
	head, err := repo.Head()
	if err != nil {
		return fmt.Errorf("read lark head: %w", err)
	}
	sourceRef := cfg.LarkRef
	if sourceRef == "" {
		sourceRef = head.Hash().String()[:12]
	}

	skillsDir := filepath.Join(repoDir, "skills")
	stagingDir := filepath.Join(tmpDir, "lark")
	if err := GenerateLarkSkill(skillsDir, stagingDir, sourceRef); err != nil {
		return err
	}
	target := filepath.Join(cfg.WorkDir, "internal", "resources", "skills", "system", "lark")
	return AtomicReplaceDir(stagingDir, target)
}

func GenerateLarkSkill(sourceSkillsDir, destDir, sourceRef string) error {
	entries, err := os.ReadDir(sourceSkillsDir)
	if err != nil {
		return fmt.Errorf("read source skills: %w", err)
	}
	refsDir := filepath.Join(destDir, "references")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		return fmt.Errorf("create references dir: %w", err)
	}
	var docs []larkSkillDoc
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillDir := filepath.Join(sourceSkillsDir, entry.Name())
		doc, err := migrateLarkSkill(skillDir, refsDir)
		if err != nil {
			return fmt.Errorf("migrate %s: %w", entry.Name(), err)
		}
		if doc.Name != "" {
			docs = append(docs, doc)
		}
	}
	sort.Slice(docs, func(i, j int) bool {
		if docs[i].Name == "lark-shared" {
			return true
		}
		if docs[j].Name == "lark-shared" {
			return false
		}
		return docs[i].Name < docs[j].Name
	})
	main := renderLarkAggregate(docs, sourceRef)
	if err := AtomicWriteFile(filepath.Join(destDir, "SKILL.md"), []byte(main), 0o644); err != nil {
		return fmt.Errorf("write aggregate skill: %w", err)
	}
	return nil
}

func migrateLarkSkill(skillDir, refsDir string) (larkSkillDoc, error) {
	skillName := filepath.Base(skillDir)
	skillPath := filepath.Join(skillDir, "SKILL.md")
	raw, err := os.ReadFile(skillPath)
	if err != nil {
		if os.IsNotExist(err) {
			return larkSkillDoc{}, nil
		}
		return larkSkillDoc{}, fmt.Errorf("read SKILL.md: %w", err)
	}
	meta, _ := parseLarkFrontmatter(string(raw))
	updated := updateLarkMarkdownReferences(string(raw), skillName)
	if err := AtomicWriteFile(filepath.Join(refsDir, skillName+".md"), []byte(updated), 0o644); err != nil {
		return larkSkillDoc{}, fmt.Errorf("write migrated skill markdown: %w", err)
	}
	originalRefs := filepath.Join(skillDir, "references")
	if entries, err := os.ReadDir(originalRefs); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(originalRefs, entry.Name()))
			if err != nil {
				return larkSkillDoc{}, fmt.Errorf("read reference %s: %w", entry.Name(), err)
			}
			if err := AtomicWriteFile(filepath.Join(refsDir, skillName, entry.Name()), data, 0o644); err != nil {
				return larkSkillDoc{}, fmt.Errorf("write reference %s: %w", entry.Name(), err)
			}
		}
	}
	return larkSkillDoc{Name: skillName, Description: meta.Description, File: skillName + ".md"}, nil
}

func parseLarkFrontmatter(content string) (larkSkillMeta, bool) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	if !strings.HasPrefix(content, "---\n") {
		return larkSkillMeta{}, false
	}
	rest := strings.TrimPrefix(content, "---\n")
	block, _, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return larkSkillMeta{}, false
	}
	var meta larkSkillMeta
	if err := yaml.Unmarshal([]byte(block), &meta); err != nil {
		return larkSkillMeta{}, false
	}
	return meta, true
}

var (
	larkSkillLinkRE = regexp.MustCompile(`\.\./(lark-[^/]+)/SKILL\.md`)
	larkRefLinkRE   = regexp.MustCompile(`\.\./(lark-[^/]+)/references/([^\)\]\s]+)`)
	larkOwnRefRE    = regexp.MustCompile(`\.\/references/([^\)\]\s]+)`)
	larkParenRefRE  = regexp.MustCompile(`\]\(references/([^\)\]\s]+)\)`)
	larkCodeRefRE   = regexp.MustCompile("`references/([^`\\s]+)`")
)

func updateLarkMarkdownReferences(content, skillName string) string {
	content = larkSkillLinkRE.ReplaceAllString(content, `./$1.md`)
	content = larkRefLinkRE.ReplaceAllString(content, `./$1/$2`)
	content = larkOwnRefRE.ReplaceAllStringFunc(content, func(match string) string {
		groups := larkOwnRefRE.FindStringSubmatch(match)
		if len(groups) != 2 {
			return match
		}
		refPath := groups[1]
		if strings.HasPrefix(refPath, skillName+"/") {
			return "./references/" + refPath
		}
		return "./" + skillName + "/" + refPath
	})
	content = larkParenRefRE.ReplaceAllStringFunc(content, func(match string) string {
		groups := larkParenRefRE.FindStringSubmatch(match)
		if len(groups) != 2 {
			return match
		}
		refPath := groups[1]
		if strings.Contains(refPath, "/") {
			return "](" + refPath + ")"
		}
		return "](./" + skillName + "/" + refPath + ")"
	})
	content = larkCodeRefRE.ReplaceAllStringFunc(content, func(match string) string {
		groups := larkCodeRefRE.FindStringSubmatch(match)
		if len(groups) != 2 {
			return match
		}
		refPath := groups[1]
		if strings.Contains(refPath, "/") {
			return "`" + refPath + "`"
		}
		return "`./" + skillName + "/" + refPath + "`"
	})
	return content
}

func renderLarkAggregate(docs []larkSkillDoc, sourceRef string) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: lark\n")
	b.WriteString("description: Aggregated Lark CLI skills synced from larksuite/cli.\n")
	b.WriteString("tags: [lark, feishu, workspace]\n")
	b.WriteString("metadata:\n")
	b.WriteString("  source_repo: larksuite/cli\n")
	b.WriteString("  source_ref: \"")
	b.WriteString(sourceRef)
	b.WriteString("\"\n")
	b.WriteString("  generated: true\n")
	b.WriteString("---\n\n")
	b.WriteString("# Lark\n\n")
	b.WriteString("This builtin skill aggregates upstream Lark CLI skills from `larksuite/cli`. Read `lark-shared` first for auth, permissions, and shared command rules.\n\n")
	b.WriteString("## Skills\n\n")
	b.WriteString("| Skill | Description | File |\n")
	b.WriteString("| --- | --- | --- |\n")
	for _, doc := range docs {
		desc := strings.ReplaceAll(doc.Description, "|", "\\|")
		if desc == "" {
			desc = "-"
		}
		fmt.Fprintf(&b, "| %s | %s | [references/%s](./references/%s) |\n", doc.Name, desc, doc.File, doc.File)
	}
	b.WriteString("\n## Usage\n\n")
	b.WriteString("1. Start with [references/lark-shared.md](./references/lark-shared.md).\n")
	b.WriteString("2. Pick the module that matches the workspace capability you need.\n")
	b.WriteString("3. Read the referenced command docs before executing any Lark CLI command.\n")
	return b.String()
}
