package main

import (
	"fmt"
	"net/http"
	"os"
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
			skillsSearchCommand(),
			skillsInstallCommand(),
			skillsListCommand(),
			skillsRemoveCommand(),
		},
		Flags:  skillAgentFlags(),
		Action: skillsListAction,
	}
}

func skillAgentFlags() []ucli.Flag {
	return []ucli.Flag{
		&ucli.StringFlag{Name: "agent-id", Usage: "Agent ID (defaults to STELLA_AGENT_ID)"},
		&ucli.StringFlag{Name: "session-id", Usage: "Session ID for project skills (defaults to STELLA_SESSION_ID)"},
	}
}

func skillAgentContext(c *ucli.Context) (string, *apiclient.ListAgentSkillsParams, error) {
	agentID := c.String("agent-id")
	if agentID == "" {
		agentID = os.Getenv("STELLA_AGENT_ID")
	}
	if agentID == "" {
		return "", nil, fmt.Errorf("agent ID is required (pass --agent-id or run inside an agent session with STELLA_AGENT_ID)")
	}
	sessionID := c.String("session-id")
	if sessionID == "" {
		sessionID = os.Getenv("STELLA_SESSION_ID")
	}
	params := &apiclient.ListAgentSkillsParams{}
	if sessionID != "" {
		params.SessionId = &sessionID
	}
	return agentID, params, nil
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
		Flags:     append([]ucli.Flag{jsonFlag()}, skillAgentFlags()...),
		Action: func(c *ucli.Context) error {
			source := c.Args().First()
			if source == "" {
				return fmt.Errorf("usage: stella skill install <owner/repo@skill-name>")
			}

			agentID, _, err := skillAgentContext(c)
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
		Flags:  append([]ucli.Flag{jsonFlag()}, skillAgentFlags()...),
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
	agentID, params, err := skillAgentContext(c)
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
		Flags:     append([]ucli.Flag{jsonFlag()}, skillAgentFlags()...),
		Action: func(c *ucli.Context) error {
			name := c.Args().First()
			if name == "" {
				return fmt.Errorf("usage: stella skill remove <name>")
			}

			agentID, params, err := skillAgentContext(c)
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
				return api.DeleteAgentSkill(c.Context, agentID, derefStr(skill.Name), &apiclient.DeleteAgentSkillParams{SessionId: params.SessionId})
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
