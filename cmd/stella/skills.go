package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
	skillstool "github.com/CherryHQ/stella/internal/tools/skills"
)

func skillsCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "skill",
		Aliases:  []string{"skills"},
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
		Action: skillsListAction,
	}
}

func skillsSearchCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "search",
		Usage:     "Search for skills (e.g. stella skills search react)",
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
				return fmt.Errorf("usage: stella skills search <query>")
			}

			limit := c.Int("limit")
			results, err := apiclient.Call[[]apitypes.SkillSearchResult](func(api *apiclient.Client) (*http.Response, error) {
				return api.SearchSkills(c.Context, &apiclient.SearchSkillsParams{
					Q:     query,
					Limit: &limit,
				})
			})
			if err != nil {
				return err
			}

			if len(results) == 0 {
				fmt.Println("No skills found.")
				return nil
			}

			fmt.Printf("Found %d skills:\n\n", len(results))
			for _, s := range results {
				fmt.Printf("  %s@%s\n", derefStr(s.Source), derefStr(s.SkillId))
				fmt.Printf("    %s (%d installs)\n", derefStr(s.Name), derefInt(s.Installs))
				fmt.Println()
			}
			fmt.Println("Install with: stella skills install <owner/repo@skill-name>")
			return nil
		},
	}
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
				return fmt.Errorf("usage: stella skills install <owner/repo@skill-name>")
			}

			fmt.Fprintf(os.Stderr, "Installing from %s...\n", source)

			me, err := apiclient.Call[apitypes.MeResponse](func(api *apiclient.Client) (*http.Response, error) {
				return api.GetMe(c.Context)
			})
			if err != nil {
				return err
			}
			scope := "user"
			result, err := apiclient.Call[map[string]string](func(api *apiclient.Client) (*http.Response, error) {
				return api.InstallSkill(c.Context, apiclient.InstallSkillJSONRequestBody{
					Source: source,
					Scope:  &scope,
					UserId: &me.Id,
				})
			})
			if err != nil {
				return err
			}

			fmt.Printf("Skill %q installed.\n", result["name"])
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
				return skillsListJSON(c)
			}
			return skillsListAction(c)
		},
	}
}

func skillsListAction(c *ucli.Context) error {
	skills, err := apiclient.Call[[]apitypes.Skill](func(api *apiclient.Client) (*http.Response, error) {
		return api.ListSkills(c.Context)
	})
	if err != nil {
		return err
	}
	if len(skills) == 0 {
		fmt.Println("No skills installed.")
		return nil
	}

	grouped := map[string][]apitypes.Skill{}
	var scopeOrder []string
	seen := map[string]bool{}
	for _, s := range skills {
		scope := derefStr(s.Scope)
		if !seen[scope] {
			seen[scope] = true
			scopeOrder = append(scopeOrder, scope)
		}
		grouped[scope] = append(grouped[scope], s)
	}

	for _, scope := range scopeOrder {
		fmt.Printf("%s:\n", scope)
		for _, s := range grouped[scope] {
			desc := derefStr(s.Description)
			if len(desc) > 80 {
				desc = desc[:77] + "..."
			}
			desc = strings.ReplaceAll(desc, "\n", " ")
			fmt.Printf("  %-25s %s\n", derefStr(s.Name), desc)
		}
		fmt.Println()
	}
	return nil
}

func skillsListJSON(c *ucli.Context) error {
	skills, err := apiclient.Call[[]apitypes.Skill](func(api *apiclient.Client) (*http.Response, error) {
		return api.ListSkills(c.Context)
	})
	if err != nil {
		return err
	}

	type entry struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Scope       string `json:"scope"`
		Status      string `json:"status"`
	}

	entries := make([]entry, len(skills))
	for i, s := range skills {
		entries[i] = entry{
			Name:        derefStr(s.Name),
			Description: derefStr(s.Description),
			Scope:       derefStr(s.Scope),
			Status:      derefStr(s.Status),
		}
	}

	out, _ := json.MarshalIndent(entries, "", "  ")
	fmt.Println(string(out))
	return nil
}

func skillsRemoveCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "remove",
		Aliases:   []string{"rm"},
		Usage:     "Remove an installed skill (e.g. stella skills remove my-skill)",
		ArgsUsage: "<name>",
		Action: func(c *ucli.Context) error {
			name := c.Args().First()
			if name == "" {
				return fmt.Errorf("usage: stella skills remove <name>")
			}

			// Check if it's a project skill first — those are read-only.
			cwd, _ := os.Getwd()
			projSkills, _, _ := skillstool.ListProjectSkills(cwd)
			for _, s := range projSkills {
				if s.Name == name {
					return fmt.Errorf("skill %q is a project skill — edit the files directly in git at %s/.agents/skills/%s", name, cwd, name)
				}
			}

			skills, err := apiclient.Call[[]apitypes.Skill](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListSkills(c.Context)
			})
			if err != nil {
				return err
			}

			var skill *apitypes.Skill
			for i := range skills {
				if derefStr(skills[i].Scope) == "user" && derefStr(skills[i].Name) == name {
					skill = &skills[i]
					break
				}
			}
			if skill == nil {
				return fmt.Errorf("skill %q not found in your user skills", name)
			}

			if err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
				return api.DeleteSkill(c.Context, derefStr(skill.Id))
			}); err != nil {
				return err
			}

			fmt.Printf("Skill %q removed.\n", name)
			return nil
		},
	}
}

func derefInt(i *int) int {
	if i == nil {
		return 0
	}
	return *i
}
