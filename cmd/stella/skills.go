package main

import (
	"fmt"
	"net/http"
	"strings"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
)

func skillsCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "skill",
		Usage:    "Search, install, and manage reusable skill bundles",
		Category: "Feature",
		Description: `Skills are reusable prompt-and-tool bundles that extend what the agent
can do. Use this command to search the skill registry, install new
skills, and manage the ones already installed.`,
		Subcommands: []*ucli.Command{
			skillsInspectCommand(),
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
		if derefStr(s.Name) == name {
			return agentID, s, nil
		}
	}
	return "", apitypes.Skill{}, fmt.Errorf("skill %q not found", name)
}

func fetchSkillContent(c *ucli.Context, agentID, name string, scope apitypes.SkillScope) (string, error) {
	s := apiclient.GetAgentSkillFileParamsScope(scope)
	path := "SKILL.md"
	file, err := apiclient.Call[apitypes.SkillFileResponse](func(api *apiclient.Client) (*http.Response, error) {
		return api.GetAgentSkillFile(c.Context, agentID, name, &apiclient.GetAgentSkillFileParams{
			Path:  path,
			Scope: &s,
		})
	})
	if err != nil {
		return "", err
	}
	return derefStr(file.Content), nil
}

func skillsInspectCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "inspect",
		Usage:     "Show skill metadata and SKILL.md content",
		ArgsUsage: "<name>",
		Flags:     []ucli.Flag{jsonFlag()},
		Action: func(c *ucli.Context) error {
			name := c.Args().First()
			if name == "" {
				return fmt.Errorf("usage: stella skill inspect <name>")
			}

			agentID, skill, err := resolveSkill(c, name)
			if err != nil {
				return err
			}

			content, err := fetchSkillContent(c, agentID, name, derefSkillScopeVal(skill.Scope))
			if err != nil {
				return fmt.Errorf("failed to read SKILL.md: %w", err)
			}

			if isJSON(c) {
				return printJSON(c, map[string]any{
					"name":        derefStr(skill.Name),
					"scope":       derefSkillScope(skill.Scope),
					"description": derefStr(skill.Description),
					"status":      derefStr(skill.Status),
					"content":     content,
				})
			}

			o := stdout(c)
			o.printf("Name:        %s\n", derefStr(skill.Name))
			o.printf("Scope:       %s\n", derefSkillScope(skill.Scope))
			o.printf("Description: %s\n", derefStr(skill.Description))
			if derefStr(skill.Status) != "" {
				o.printf("Status:      %s\n", derefStr(skill.Status))
			}
			o.println()
			o.println("--- SKILL.md ---")
			o.println(content)
			return o.Err()
		},
	}
}

func skillsLoadCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "load",
		Usage:     "Load skill content for the agent to read and follow",
		ArgsUsage: "<name>",
		Flags:     []ucli.Flag{jsonFlag()},
		Action: func(c *ucli.Context) error {
			name := c.Args().First()
			if name == "" {
				return fmt.Errorf("usage: stella skill load <name>")
			}

			agentID, skill, err := resolveSkill(c, name)
			if err != nil {
				return err
			}

			content, err := fetchSkillContent(c, agentID, name, derefSkillScopeVal(skill.Scope))
			if err != nil {
				return fmt.Errorf("failed to read SKILL.md: %w", err)
			}

			if isJSON(c) {
				return printJSON(c, map[string]string{
					"name":    name,
					"content": content,
				})
			}

			o := stdout(c)
			o.println(content)
			return o.Err()
		},
	}
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
			jsonFlag(),
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
			if isJSON(c) {
				return printJSON(c, list)
			}
			results := list.Skills
			o := stdout(c)
			if len(results) == 0 {
				o.println("No skills found.")
				return o.Err()
			}

			o.printf("Found %d skills:\n\n", len(results))
			for _, s := range results {
				o.printf("  %s@%s\n", derefStr(s.Source), derefStr(s.SkillId))
				o.printf("    %s (%d installs)\n", derefStr(s.Name), derefInt(s.Installs))
				o.println()
			}
			o.println("Install with: stella skill install <owner/repo@skill-name>")
			return o.Err()
		},
	}
}

func skillsInstallCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "install",
		Usage:     "Install a skill (e.g. owner/repo@skill-name, GitHub/GitLab URL, or local path)",
		ArgsUsage: "<source>",
		Flags:     []ucli.Flag{jsonFlag()},
		Action: func(c *ucli.Context) error {
			source := c.Args().First()
			if source == "" {
				return fmt.Errorf("usage: stella skill install <owner/repo@skill-name>")
			}

			agentID, _, err := skillAgentContext()
			if err != nil {
				return err
			}

			stderr(c).printf("Installing from %s...\n", source)

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

			if isJSON(c) {
				return printJSON(c, result)
			}
			o := stdout(c)
			o.printf("Skill %q installed.\n", result["name"])
			return o.Err()
		},
	}
}

func skillsListCommand() *ucli.Command {
	return &ucli.Command{
		Name:   "list",
		Usage:  "List installed skills",
		Flags:  []ucli.Flag{jsonFlag()},
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
	if isJSON(c) {
		return printJSON(c, list)
	}
	skills := list.Skills
	o := stdout(c)
	if len(skills) == 0 {
		o.println("No skills installed.")
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
		o.printf("%s:\n", scope)
		for _, s := range grouped[scope] {
			desc := derefStr(s.Description)
			if len(desc) > 80 {
				desc = desc[:77] + "..."
			}
			desc = strings.ReplaceAll(desc, "\n", " ")
			o.printf("  %-25s %s\n", derefStr(s.Name), desc)
		}
		o.println()
	}
	return o.Err()
}

func skillsRemoveCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "remove",
		Usage:     "Remove an installed skill (e.g. stella skill remove my-skill)",
		ArgsUsage: "<name>",
		Flags:     []ucli.Flag{jsonFlag()},
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
				if derefSkillScope(skills[i].Scope) == "user" && derefStr(skills[i].Name) == name {
					skill = &skills[i]
					break
				}
			}
			if skill == nil {
				return fmt.Errorf("skill %q not found in your user skills", name)
			}

			if err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
				return api.DeleteAgentSkill(c.Context, agentID, derefStr(skill.Name), &apiclient.DeleteAgentSkillParams{})
			}); err != nil {
				return err
			}

			if isJSON(c) {
				return printDeleted(c, name)
			}
			o := stdout(c)
			o.printf("Skill %q removed.\n", name)
			return o.Err()
		},
	}
}

func derefInt(i *int) int {
	if i == nil {
		return 0
	}
	return *i
}
