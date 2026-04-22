package builddeps

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

const larkCLIRepo = "https://github.com/larksuite/cli"

type larkSkillMeta struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type larkSkillDoc struct {
	Name        string
	Description string
	File        string
}

const annaLarkSharedDescription = "飞书/Lark CLI 共享基础（Anna 适配版）：说明 Anna 会话中的 wrapper + 环境变量认证模型、`--as user` / `--as bot` 选择、scope / Permission denied 处理，以及何时才需要回退到上游 `config init` / `auth login` 流程。"

func SyncSystemSkills(ctx context.Context, cfg Config) error {
	if err := syncLarkSkill(ctx, cfg); err != nil {
		return err
	}
	if err := syncTapWebSkill(ctx, cfg); err != nil {
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
			slog.Warn("skipping skill dir without SKILL.md", "dir", skillDir)
			return larkSkillDoc{}, nil
		}
		return larkSkillDoc{}, fmt.Errorf("read SKILL.md: %w", err)
	}
	content := string(raw)
	meta, _ := parseLarkFrontmatter(content)
	updated := updateLarkMarkdownReferences(content, skillName)
	if skillName == "lark-shared" {
		meta.Description = annaLarkSharedDescription
		updated = adaptLarkSharedForAnna(updated)
	}
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
	var meta larkSkillMeta
	if !unmarshalYAMLFrontmatter(content, &meta) {
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

func adaptLarkSharedForAnna(content string) string {
	const annaIntro = `## Anna 会话中的运行方式（优先遵循本节）

在 Anna 的沙盒会话里，
` + "`lark-cli`" + ` 走的是 **wrapper + 环境变量注入** 模式，不是上游文档默认假设的“先 ` + "`config init`" + `，再 ` + "`auth login`" + `”本地配置模式。

- 直接运行 ` + "`lark-cli ...`" + ` 即可。Anna 会把会话级 wrapper（通常位于 ` + "`.anna/bin/lark-cli`" + `）放到 ` + "`PATH`" + ` 前面，再转发到真实二进制（通常是 ` + "`$ANNA_HOME/bin/lark-cli`" + `，也可能是宿主机 ` + "`PATH`" + ` 上的 ` + "`lark-cli`" + `）。
- Anna 会在会话启动时注入 ` + "`LARKSUITE_CLI_USER_ACCESS_TOKEN`" + `、` + "`LARKSUITE_CLI_APP_ID`" + `、` + "`LARKSUITE_CLI_BRAND`" + `。因此 **在 Anna 会话里默认不要先执行 ` + "`lark-cli config init`" + ` 或 ` + "`lark-cli auth login`" + `**。
- 把后续文档里所有“先 ` + "`auth login --domain`" + ` / ` + "`auth login --scope`" + `”的要求，映射为：**用 credentials 工具确认 Lark 已连接（credentials status 指令），所需 scope 已在 Lark 应用侧开通，然后开启一个新的 Anna 会话重试**。
- 如果用户只是想在自己机器上单独配置一个**脱离 Anna** 的 ` + "`lark-cli`" + `，那才回退到上游原生的 ` + "`config init`" + ` / ` + "`auth login`" + ` 流程。

## Anna 中的身份选择

| 身份 | 在 Anna 中如何获得 | 适用场景 |
|------|------------------|---------|
| user 用户身份 | Anna 注入运行时用户令牌；优先使用 ` + "`--as user`" + `（或命令默认身份） | 访问用户自己的日历、云文档、任务、邮箱等个人资源 |
| bot 应用身份 | 需要用户在 Anna 之外显式准备 app 配置 / tenant token；Anna 当前**不自动注入** bot 所需的 app secret 或 config 文件 | 仅在用户明确说明已完成这套手动配置时使用 |

### 身份选择原则

- **默认优先 user**：大多数日历、云空间、文档、任务、邮箱等工作区请求，本质上都是“代表当前用户操作自己的资源”。
- **不要擅自假设 bot 可用**：如果文档或任务要求 ` + "`--as bot`" + `、` + "`tenant_access_token`" + `、appSecret 或本地 config 文件，而当前会话只有 Anna 注入的 user 运行时环境，就应先停下来说明这是**Anna 当前未自动接线**的手动配置路径。
- **Bot 看不到用户私有资源**：即便用户自己在 Anna 外部完成了 bot 配置，` + "`--as bot`" + ` 也仍然看不到用户的私有日历、个人云文档、邮箱等资源。

## Anna 中的认证与提权处理

### 未连接、过期或认证失败

- 如果 ` + "`lark-cli`" + ` 提示未登录、缺少 access token、401/expired，先用 credentials 工具检查 Lark 连接状态（credentials status / credentials oauth_start 指令）并引导用户重新连接 Lark。
- Lark user access token 约 2 小时过期；Anna 只在**会话启动时**刷新。已连接但中途过期时，直接开启一个新的 Anna 会话。
- 重新开启会话后仍失败，说明 refresh token 也可能失效或授权被撤销；此时应让用户断开并重新连接 Lark，而不是在会话里继续尝试 ` + "`auth login`" + `。

### 权限不足 / scope 不足

- 先查看错误里的 ` + "`permission_violations`" + `、` + "`console_url`" + `、` + "`hint`" + `。
- 如果缺的是**应用 scope**，把 ` + "`console_url`" + ` 提供给用户或管理员，让他们去 Lark 开发者后台开通对应权限。
- 应用 scope 开通后，在 Anna 会话里**不要**继续执行 ` + "`lark-cli auth login --scope ...`" + `；应让用户按需重新连接 Lark，并开启一个新的 Anna 会话后再重试。
- 只有当用户明确要求“配置一套独立于 Anna 的本地 ` + "`lark-cli`" + ` 环境”时，才执行上游文档里的 ` + "`config init`" + ` / ` + "`auth login`" + ` 指令。`

	content = rewriteLarkDescription(content, annaLarkSharedDescription)
	start := strings.Index(content, "## 配置初始化")
	end := strings.Index(content, "## 更新检查")
	if start >= 0 && end > start {
		return content[:start] + annaIntro + "\n\n" + content[end:]
	}
	if idx := strings.Index(content, "# lark-cli 共享规则"); idx >= 0 {
		idx += len("# lark-cli 共享规则")
		return content[:idx] + "\n\n" + annaIntro + content[idx:]
	}
	return content + "\n\n" + annaIntro + "\n"
}

func rewriteLarkDescription(content, description string) string {
	return regexp.MustCompile(`(?m)^description:\s*".*"$`).ReplaceAllString(content, `description: "`+description+`"`)
}

var larkAggregateTemplate = template.Must(template.New("lark-aggregate").Funcs(template.FuncMap{
	"escPipe": func(s string) string {
		if s == "" {
			return "-"
		}
		return strings.ReplaceAll(s, "|", "\\|")
	},
}).Parse(`---
name: lark
description: |
  Lark/Feishu CLI skills for Anna sessions. Covers workspace operations — calendar,
  docs, tasks, mail, and messenger — via the lark-cli tool. In Anna, lark-cli runs
  under a wrapper + env-var auth model: LARKSUITE_CLI_USER_ACCESS_TOKEN,
  LARKSUITE_CLI_APP_ID, and LARKSUITE_CLI_BRAND are injected at session start;
  no manual config init or auth login is
  needed. Always read lark-shared first for identity selection (--as user vs --as
  bot), scope / permission-denied handling, token refresh, and the Anna-specific
  rules that override upstream documentation.
tags: [lark, feishu, workspace]
metadata:
  source_repo: larksuite/cli
  source_ref: "{{.SourceRef}}"
  generated: true
---

# Lark

This skill aggregates Lark/Feishu CLI modules synced from ` + "`larksuite/cli`" + ` and adapted for Anna sessions.

**Anna auth model** — ` + "`lark-cli`" + ` runs via a session wrapper that injects ` + "`LARKSUITE_CLI_USER_ACCESS_TOKEN`" + `, ` + "`LARKSUITE_CLI_APP_ID`" + `, and ` + "`LARKSUITE_CLI_BRAND`" + ` at startup. Do not run ` + "`lark-cli config init`" + ` or ` + "`lark-cli auth login`" + ` unless the user explicitly wants a standalone local setup outside Anna.

**Identity** — default to ` + "`--as user`" + ` for personal resources (calendar, docs, tasks, mail). ` + "`--as bot`" + ` requires manual app configuration outside Anna and cannot see user-private resources.

**Token expiry** — user access tokens expire after ~2 hours. If mid-session auth fails, open a new Anna session. If that also fails, use the credentials tool (credentials status, then credentials oauth_start for lark) to reconnect.

## Modules

| Module | Description | Reference |
| --- | --- | --- |
{{- range .Docs}}
| {{.Name}} | {{escPipe .Description}} | [references/{{.File}}](./references/{{.File}}) |
{{- end}}

## Usage

1. Read [references/lark-shared.md](./references/lark-shared.md) first — it defines auth rules, identity selection, and scope handling that apply to every module.
2. Identify the module matching the capability you need (calendar, docs, tasks, mail, messenger, etc.).
3. Read the module's reference file before executing any command — it lists available subcommands, required flags, and known permission constraints.
4. If a command returns a permission or scope error, consult the ` + "`permission_violations`" + ` and ` + "`console_url`" + ` fields in the error before retrying.
`))

func renderLarkAggregate(docs []larkSkillDoc, sourceRef string) string {
	var buf bytes.Buffer
	if err := larkAggregateTemplate.Execute(&buf, struct {
		Docs      []larkSkillDoc
		SourceRef string
	}{Docs: docs, SourceRef: sourceRef}); err != nil {
		panic(fmt.Sprintf("renderLarkAggregate: %v", err))
	}
	return buf.String()
}
