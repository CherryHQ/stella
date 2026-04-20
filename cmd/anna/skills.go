package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/internal/config"
	appdb "github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/pluginhost"
	internalskills "github.com/vaayne/anna/internal/skills"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	builtinres "github.com/vaayne/anna/plugins/tools/builtin"
	skillstool "github.com/vaayne/anna/plugins/tools/skills"
	mcpskills "github.com/vaayne/mcphub/pkg/skills"
)

func skillsCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "skills",
		Usage: "Manage agent skills",
		Subcommands: []*ucli.Command{
			skillsSearchCommand(),
			skillsInstallCommand(),
			skillsListCommand(),
			skillsRemoveCommand(),
			skillsMigrateCommand(),
		},
		Action: func(c *ucli.Context) error {
			return skillsListAction(c.Context)
		},
	}
}

// openSkillStore opens the application DB and returns a SkillStore backed by it.
// The caller is responsible for closing db when done.
func openSkillStore() (pkgplugins.SkillStore, func(), error) {
	db, err := appdb.OpenDB(config.DBPath())
	if err != nil {
		return nil, nil, fmt.Errorf("open database: %w", err)
	}
	store := pluginhost.NewSkillStoreAdapter(internalskills.New(db))
	return store, func() { _ = db.Close() }, nil
}

func skillsSearchCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "search",
		Usage:     "Search for skills (e.g. anna skills search react)",
		ArgsUsage: "<query>",
		Flags: []ucli.Flag{
			&ucli.IntFlag{
				Name:  "limit",
				Usage: "Max results to return",
				Value: 10,
			},
		},
		Action: func(c *ucli.Context) error {
			query := c.Args().First()
			if query == "" {
				return fmt.Errorf("usage: anna skills search <query>")
			}

			ctx, cancel := context.WithTimeout(c.Context, 10*time.Second)
			defer cancel()

			results, err := mcpskills.Search(ctx, query, c.Int("limit"))
			if err != nil {
				return err
			}

			if len(results) == 0 {
				fmt.Println("No skills found.")
				return nil
			}

			fmt.Printf("Found %d skills:\n\n", len(results))
			for _, s := range results {
				fmt.Printf("  %s@%s\n", s.Source, s.SkillID)
				fmt.Printf("    %s (%d installs)\n", s.Name, s.Installs)
				fmt.Println()
			}
			fmt.Println("Install with: anna skills install <owner/repo@skill-name>")
			return nil
		},
	}
}

const cliSkillsUserID int64 = 1

func cliUserSkillsDir(snap *config.Snapshot) (string, error) {
	userDir, err := agent.SetupUserWorkspace(snap.AgentID, config.AnnaHome(), cliSkillsUserID)
	if err != nil {
		return "", err
	}
	return agent.UserSkillsDir(userDir), nil
}

func skillsInstallCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "install",
		Aliases:   []string{"add"},
		Usage:     "Install a skill (e.g. owner/repo@skill-name, GitHub/GitLab URL, or local path)",
		ArgsUsage: "<source>",
		Action: func(c *ucli.Context) error {
			source := c.Args().First()
			if source == "" {
				return fmt.Errorf("usage: anna skills install <owner/repo@skill-name>")
			}

			skillStore, closeDB, err := openSkillStore()
			if err != nil {
				return err
			}
			defer closeDB()

			fmt.Fprintf(os.Stderr, "Installing from %s into user scope (user=%d)...\n", source, cliSkillsUserID)

			name, err := skillstool.InstallToStore(c.Context, skillStore, source, "user", cliSkillsUserID, "")
			if err != nil {
				return err
			}

			fmt.Printf("Skill %q installed (scope=user, user_id=%d).\n", name, cliSkillsUserID)
			return nil
		},
	}
}

func skillsListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List installed skills",
		Flags: []ucli.Flag{
			&ucli.BoolFlag{
				Name:  "json",
				Usage: "Output as JSON",
			},
		},
		Action: func(c *ucli.Context) error {
			if c.Bool("json") {
				return skillsListJSON(c.Context)
			}
			return skillsListAction(c.Context)
		},
	}
}

func skillsListAction(ctx context.Context) error {
	loaded, err := loadInstalledSkills(ctx)
	if err != nil {
		return err
	}
	if len(loaded) == 0 {
		fmt.Println("No skills installed.")
		return nil
	}

	// Group by scope
	grouped := map[string][]pkgplugins.Skill{}
	var scopeOrder []string
	seen := map[string]bool{}
	for _, s := range loaded {
		if !seen[s.Scope] {
			seen[s.Scope] = true
			scopeOrder = append(scopeOrder, s.Scope)
		}
		grouped[s.Scope] = append(grouped[s.Scope], s)
	}

	for _, scope := range scopeOrder {
		fmt.Printf("%s:\n", scope)
		for _, s := range grouped[scope] {
			desc := s.Description
			if len(desc) > 80 {
				desc = desc[:77] + "..."
			}
			desc = strings.ReplaceAll(desc, "\n", " ")
			fmt.Printf("  %-25s %s\n", s.Name, desc)
		}
		fmt.Println()
	}
	return nil
}

func skillsListJSON(ctx context.Context) error {
	loaded, err := loadInstalledSkills(ctx)
	if err != nil {
		return err
	}

	type entry struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Scope       string `json:"scope"`
		Status      string `json:"status"`
	}

	entries := make([]entry, len(loaded))
	for i, s := range loaded {
		entries[i] = entry{
			Name:        s.Name,
			Description: s.Description,
			Scope:       s.Scope,
			Status:      s.Status,
		}
	}

	out, _ := json.MarshalIndent(entries, "", "  ")
	fmt.Println(string(out))
	return nil
}

func loadInstalledSkills(ctx context.Context) ([]pkgplugins.Skill, error) {
	skillStore, closeDB, err := openSkillStore()
	if err != nil {
		return nil, err
	}
	defer closeDB()

	vc := pkgplugins.SkillViewContext{
		UserID: cliSkillsUserID,
	}
	dbSkills, err := skillStore.List(ctx, vc)
	if err != nil {
		return nil, err
	}

	// Merge project skills from the current working directory.
	cwd, _ := os.Getwd()
	projSkills, _, _ := skillstool.ListProjectSkills(cwd)

	// Deduplicate: project skills shadow same-named DB skills.
	projNames := make(map[string]bool, len(projSkills))
	for _, s := range projSkills {
		projNames[s.Name] = true
	}

	all := make([]pkgplugins.Skill, 0, len(projSkills)+len(dbSkills))
	all = append(all, projSkills...)
	for _, s := range dbSkills {
		if !projNames[s.Name] {
			all = append(all, s)
		}
	}
	return all, nil
}

func skillsRemoveCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "remove",
		Aliases:   []string{"rm"},
		Usage:     "Remove an installed skill (e.g. anna skills remove my-skill)",
		ArgsUsage: "<name>",
		Action: func(c *ucli.Context) error {
			name := c.Args().First()
			if name == "" {
				return fmt.Errorf("usage: anna skills remove <name>")
			}

			// Check if it's a project skill first — those are read-only.
			cwd, _ := os.Getwd()
			projSkills, _, _ := skillstool.ListProjectSkills(cwd)
			for _, s := range projSkills {
				if s.Name == name {
					return fmt.Errorf("skill %q is a project skill — edit the files directly in git at %s/.agents/skills/%s", name, cwd, name)
				}
			}

			skillStore, closeDB, err := openSkillStore()
			if err != nil {
				return err
			}
			defer closeDB()

			vc := pkgplugins.SkillViewContext{
				UserID: cliSkillsUserID,
			}

			skill, err := skillStore.Resolve(c.Context, name, vc)
			if err != nil {
				return fmt.Errorf("resolve skill %q: %w", name, err)
			}
			if skill == nil {
				return fmt.Errorf("skill %q not found", name)
			}
			if skill.Scope == "system" {
				return fmt.Errorf("cannot remove system skill %q", name)
			}

			if err := skillStore.Delete(c.Context, skill.ID); err != nil {
				return fmt.Errorf("remove skill %q: %w", name, err)
			}

			fmt.Printf("Skill %q removed.\n", name)
			return nil
		},
	}
}

func skillsMigrateCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "migrate",
		Usage: "Import on-disk skills into the database (run once after upgrading from filesystem-based skills)",
		Action: func(c *ucli.Context) error {
			// Open DB directly so we can call SyncBuiltin on the SQLiteStore.
			db, err := appdb.OpenDB(config.DBPath())
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer func() { _ = db.Close() }()

			sqliteStore := internalskills.New(db)

			// 1. Sync builtins.
			builtinSkillsFS, ok := builtinres.SubFS(builtinres.KindSkill)
			if !ok {
				return fmt.Errorf("builtin skills FS unavailable")
			}
			if err := internalskills.SyncBuiltin(c.Context, sqliteStore, builtinSkillsFS); err != nil {
				return fmt.Errorf("sync builtins: %w", err)
			}

			// 2. Migrate on-disk skills.
			configStore, err := openStore()
			if err != nil {
				return err
			}
			snap, err := defaultSnapshot(c.Context, configStore)
			if err != nil {
				return err
			}
			userSkillsDir, err := cliUserSkillsDir(snap)
			if err != nil {
				return err
			}

			cfg := internalskills.MigrateFSConfig{
				AgentRoot:     snap.Workspace,
				AgentID:       snap.AgentID,
				UserSkillsDir: userSkillsDir,
				UserID:        cliSkillsUserID,
			}
			fsResult, err := internalskills.MigrateFilesystem(c.Context, sqliteStore, cfg)
			if err != nil {
				return fmt.Errorf("filesystem migration: %w", err)
			}

			fmt.Printf("Builtin sync: OK. Filesystem migration: imported=%d, skipped=%d.\n",
				fsResult.Imported, fsResult.Skipped)
			return nil
		},
	}
}
