package main

import (
	"fmt"
	"net/http"
	"strings"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/cli"
)

func skillsCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "skill",
		Usage:    "Search, install, and manage reusable skill bundles",
		Category: "Feature",
		Description: `Skills are reusable instruction sets that extend what the agent can do.
Use 'load' to read a skill before following it. Use 'search' and 'install'
to add skills from the registry.`,
		Subcommands: []*ucli.Command{
			skillsLoadCommand(),
			skillsSearchCommand(),
			skillsInstallCommand(),
			skillsListCommand(),
			skillsRemoveCommand(),
		},
		Action: skillsListAction,
	}
}

func skillAgentContext() (string, *apiclient.ListAgentSkillsParams, error) {
	agentID, err := scopedAgentIDFromEnv()
	if err != nil {
		return "", nil, err
	}
	return agentID, &apiclient.ListAgentSkillsParams{}, nil
}

func resolveSkill(c *ucli.Context, name string) (string, apitypes.Skill, error) {
	agentID, params, err := skillAgentContext()
	if err != nil {
		return "", apitypes.Skill{}, err
	}
	list, err := apiclient.Call[apitypes.SkillList](func(api *apiclient.Client) (*http.Response, error) {
		return api.ListAgentSkills(c.Context, agentID, params)
	})
	if err != nil {
		return "", apitypes.Skill{}, err
	}
	for _, s := range list.Skills {
		if cli.DerefStr(s.Name) == name {
			return agentID, s, nil
		}
	}
	return "", apitypes.Skill{}, fmt.Errorf("skill %q not found", name)
}

func fetchSkillFile(c *ucli.Context, agentID, name string, scope apitypes.SkillScope, path string) (string, error) {
	s := apiclient.GetAgentSkillFileParamsScope(scope)
	if path == "" {
		path = "SKILL.md"
	}
	file, err := apiclient.Call[apitypes.SkillFileResponse](func(api *apiclient.Client) (*http.Response, error) {
		return api.GetAgentSkillFile(c.Context, agentID, name, &apiclient.GetAgentSkillFileParams{
			Path:  path,
			Scope: &s,
		})
	})
	if err != nil {
		return "", err
	}
	return cli.DerefStr(file.Content), nil
}

func skillsLoadCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "load",
		Usage:     "Load skill content (SKILL.md or a referenced file)",
		ArgsUsage: "<name>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:  "path",
				Usage: "File path within the skill to load (e.g. references/api.md)",
			},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			name := c.Args().First()
			if name == "" {
				return fmt.Errorf("usage: stella skill load <name> [--path references/file.md]")
			}

			agentID, skill, err := resolveSkill(c, name)
			if err != nil {
				return err
			}

			path := c.String("path")
			content, err := fetchSkillFile(c, agentID, name, derefSkillScopeVal(skill.Scope), path)
			if err != nil {
				return err
			}

			if cli.IsJSON(c) {
				out := map[string]any{
					"name":    name,
					"content": content,
				}
				if path != "" {
					out["path"] = path
				}
				return cli.PrintJSON(c, out)
			}

			o := cli.Stdout(c)

			// When loading SKILL.md (default), print metadata header and
			// a hint about referenced files so the agent knows how to
			// access them.
			if path == "" {
				o.Printf("[skill: %s | scope: %s", name, derefSkillScope(skill.Scope))
				if cli.DerefStr(skill.Status) != "" {
					o.Printf(" | status: %s", cli.DerefStr(skill.Status))
				}
				o.Println("]")
				o.Println()
			}

			o.Println(content)

			if path == "" {
				files := skillFiles(skill)
				refs := filterReferences(files)
				if len(refs) > 0 {
					o.Println()
					o.Println("---")
					o.Printf("This skill has referenced files. Load them with:\n")
					for _, ref := range refs {
						o.Printf("  stella skill load %s --path %s\n", name, ref)
					}
				}
			}

			return o.Err()
		},
	}
}

func skillFiles(s apitypes.Skill) []string {
	if s.Files == nil {
		return nil
	}
	return *s.Files
}

func filterReferences(files []string) []string {
	var refs []string
	for _, f := range files {
		if f != "SKILL.md" {
			refs = append(refs, f)
		}
	}
	return refs
}

func derefSkillScopeVal(s *apitypes.SkillScope) apitypes.SkillScope {
	if s == nil {
		return ""
	}
	return *s
}

func skillsSearchCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "search",
		Usage:     "Search for skills (e.g. stella skill search react)",
		ArgsUsage: "<query>",
		Flags: []ucli.Flag{
			&ucli.IntFlag{
				Name:  "limit",
				Usage: "Max results to return",
				Value: 10,
			},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			query := c.Args().First()
			if query == "" {
				return fmt.Errorf("usage: stella skill search <query>")
			}

			limit := c.Int("limit")
			list, err := apiclient.Call[apitypes.SkillSearchResultList](func(api *apiclient.Client) (*http.Response, error) {
				return api.SearchSkills(c.Context, &apiclient.SearchSkillsParams{
					Q:     query,
					Limit: &limit,
				})
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, list)
			}
			results := list.Skills
			o := cli.Stdout(c)
			if len(results) == 0 {
				o.Println("No skills found.")
				return o.Err()
			}

			o.Printf("Found %d skills:\n\n", len(results))
			for _, s := range results {
				o.Printf("  %s@%s\n", cli.DerefStr(s.Source), cli.DerefStr(s.SkillId))
				o.Printf("    %s (%d installs)\n", cli.DerefStr(s.Name), cli.DerefInt(s.Installs))
				o.Println()
			}
			o.Println("Install with: stella skill install <owner/repo@skill-name>")
			return o.Err()
		},
	}
}

func skillsInstallCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "install",
		Usage:     "Install a skill (e.g. owner/repo@skill-name, GitHub/GitLab URL, or local path)",
		ArgsUsage: "<source>",
		Flags:     []ucli.Flag{cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			source := c.Args().First()
			if source == "" {
				return fmt.Errorf("usage: stella skill install <owner/repo@skill-name>")
			}

			agentID, _, err := skillAgentContext()
			if err != nil {
				return err
			}

			cli.Stderr(c).Printf("Installing from %s...\n", source)

			scope := apitypes.InstallSkillRequestScope("user")
			result, err := apiclient.Call[map[string]string](func(api *apiclient.Client) (*http.Response, error) {
				return api.InstallAgentSkill(c.Context, agentID, apiclient.InstallAgentSkillJSONRequestBody{
					Source: source,
					Scope:  &scope,
				})
			})
			if err != nil {
				return err
			}

			if cli.IsJSON(c) {
				return cli.PrintJSON(c, result)
			}
			o := cli.Stdout(c)
			o.Printf("Skill %q installed.\n", result["name"])
			return o.Err()
		},
	}
}

func skillsListCommand() *ucli.Command {
	return &ucli.Command{
		Name:   "list",
		Usage:  "List installed skills",
		Flags:  []ucli.Flag{cli.JSONFlag()},
		Action: skillsListAction,
	}
}

func derefSkillScope(s *apitypes.SkillScope) string {
	if s == nil {
		return ""
	}
	return string(*s)
}

func skillsListAction(c *ucli.Context) error {
	if c.Args().Present() {
		return fmt.Errorf("unknown command %q. Run 'stella skill --help' for usage", c.Args().First())
	}
	agentID, params, err := skillAgentContext()
	if err != nil {
		return err
	}
	list, err := apiclient.Call[apitypes.SkillList](func(api *apiclient.Client) (*http.Response, error) {
		return api.ListAgentSkills(c.Context, agentID, params)
	})
	if err != nil {
		return err
	}
	if cli.IsJSON(c) {
		return cli.PrintJSON(c, list)
	}
	skills := list.Skills
	o := cli.Stdout(c)
	if len(skills) == 0 {
		o.Println("No skills installed.")
		return o.Err()
	}

	grouped := map[string][]apitypes.Skill{}
	var scopeOrder []string
	seen := map[string]bool{}
	for _, s := range skills {
		scope := derefSkillScope(s.Scope)
		if !seen[scope] {
			seen[scope] = true
			scopeOrder = append(scopeOrder, scope)
		}
		grouped[scope] = append(grouped[scope], s)
	}

	for _, scope := range scopeOrder {
		o.Printf("%s:\n", scope)
		for _, s := range grouped[scope] {
			desc := cli.DerefStr(s.Description)
			if len(desc) > 80 {
				desc = desc[:77] + "..."
			}
			desc = strings.ReplaceAll(desc, "\n", " ")
			o.Printf("  %-25s %s\n", cli.DerefStr(s.Name), desc)
		}
		o.Println()
	}
	return o.Err()
}

func skillsRemoveCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "remove",
		Usage:     "Remove an installed skill (e.g. stella skill remove my-skill)",
		ArgsUsage: "<name>",
		Flags:     []ucli.Flag{cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			name := c.Args().First()
			if name == "" {
				return fmt.Errorf("usage: stella skill remove <name>")
			}

			agentID, params, err := skillAgentContext()
			if err != nil {
				return err
			}
			list, err := apiclient.Call[apitypes.SkillList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListAgentSkills(c.Context, agentID, params)
			})
			if err != nil {
				return err
			}
			skills := list.Skills

			var skill *apitypes.Skill
			for i := range skills {
				if derefSkillScope(skills[i].Scope) == "user" && cli.DerefStr(skills[i].Name) == name {
					skill = &skills[i]
					break
				}
			}
			if skill == nil {
				return fmt.Errorf("skill %q not found in your user skills", name)
			}

			if err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
				return api.DeleteAgentSkill(c.Context, agentID, cli.DerefStr(skill.Name), &apiclient.DeleteAgentSkillParams{})
			}); err != nil {
				return err
			}

			if cli.IsJSON(c) {
				return cli.PrintDeleted(c, name)
			}
			o := cli.Stdout(c)
			o.Printf("Skill %q removed.\n", name)
			return o.Err()
		},
	}
}
