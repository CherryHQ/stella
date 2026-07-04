package main

import (
	"fmt"
	"io"
	"net/http"
	"os"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/cli"
	"github.com/CherryHQ/stella/internal/vault"
)

func vaultCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "vault",
		Usage:    "Store and retrieve encrypted secrets (API keys, tokens) available to the agent at runtime",
		Category: "Feature",
		Description: `The vault stores API keys, tokens, and other secrets encrypted at rest
with an age key. New secrets are not injected into sandbox environments unless
bound with --inject-always, --inject-agent, or --inject-project.`,
		Subcommands: []*ucli.Command{
			vaultKeygenCommand(),
			vaultListCommand(),
			vaultGetCommand(),
			vaultSetCommand(),
			vaultAuditCommand(),
			vaultDeleteCommand(),
		},
	}
}

func vaultKeygenCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "keygen",
		Usage: "Generate a STELLA_VAULT_KEY value",
		Action: func(_ *ucli.Context) error {
			key, err := vault.GenerateMasterIdentity()
			if err != nil {
				return err
			}
			fmt.Println(key)
			return nil
		},
	}
}

func vaultListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List vault entries",
		Flags: []ucli.Flag{
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			scope := apiclient.ListScopedVaultEntriesParamsScopeUser
			list, err := apiclient.Call[apitypes.VaultEntryList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListScopedVaultEntries(c.Context, &apiclient.ListScopedVaultEntriesParams{Scope: &scope})
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, list)
			}
			entries := list.Entries
			o := cli.Stdout(c)
			if len(entries) == 0 {
				o.Println("No vault entries.")
				return o.Err()
			}
			o.Printf("%-30s  %-14s  %-20s  %-20s\n", "NAME", "INJECTION", "CREATED", "UPDATED")
			for _, e := range entries {
				o.Printf("%-30s  %-14s  %-20s  %-20s\n",
					cli.Truncate(e.Name, 30), vaultInjectionLabel(e), e.CreatedAt.Format("2006-01-02 15:04:05"), e.UpdatedAt.Format("2006-01-02 15:04:05"))
			}
			return o.Err()
		},
	}
}

func vaultInjectionLabel(e apitypes.VaultEntry) string {
	if e.InjectAlways {
		return "always"
	}
	if len(e.InjectAgentIds) > 0 || len(e.InjectProjectIds) > 0 {
		return "bound"
	}
	return "off"
}

func vaultGetCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "get",
		Usage:     "Get a vault entry value",
		ArgsUsage: "<name>",
		Flags: []ucli.Flag{
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			name := c.Args().First()
			if name == "" {
				return fmt.Errorf("usage: stella vault get <name>")
			}
			scope := apiclient.GetScopedVaultEntryParamsScope("user")
			entry, err := apiclient.Call[apiclient.VaultEntryValue](func(api *apiclient.Client) (*http.Response, error) {
				return api.GetScopedVaultEntry(c.Context, name, &apiclient.GetScopedVaultEntryParams{Scope: &scope})
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, entry)
			}
			// Deliberate exception to the no-secrets rule: `vault get <name>` is
			// the explicit single-resource retrieval used for scripting.
			o := cli.Stdout(c)
			o.Println(entry.Value)
			return o.Err()
		},
	}
}

func vaultSetCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "set",
		Usage:     "Set a vault entry (use '-' as value to read from stdin)",
		ArgsUsage: "<name> <value>",
		Flags: []ucli.Flag{
			cli.JSONFlag(),
			&ucli.StringFlag{Name: "description", Usage: "Non-sensitive description shown to agents in the declarable secrets manifest"},
			&ucli.BoolFlag{Name: "inject-always", Usage: "Inject this entry into every sandbox env in its scope"},
			&ucli.StringSliceFlag{Name: "inject-agent", Usage: "Inject this entry for an agent ID (repeatable)"},
			&ucli.StringSliceFlag{Name: "inject-project", Usage: "Inject this entry for a project ID (repeatable)"},
		},
		Action: func(c *ucli.Context) error {
			name := c.Args().Get(0)
			value := c.Args().Get(1)
			if name == "" {
				return fmt.Errorf("usage: stella vault set <name> <value>")
			}
			switch value {
			case "-":
				data, err := io.ReadAll(os.Stdin)
				if err != nil {
					return fmt.Errorf("read stdin: %w", err)
				}
				value = string(data)
			case "":
				return fmt.Errorf("usage: stella vault set <name> <value>  (use '-' to read from stdin)")
			}
			scope := apitypes.SetVaultEntryRequestScopeUser
			req := apiclient.SetScopedVaultEntryJSONRequestBody{Value: value, Scope: &scope}
			if c.IsSet("description") {
				description := c.String("description")
				req.Description = &description
			}
			if c.Bool("inject-always") {
				injectAlways := true
				req.InjectAlways = &injectAlways
			}
			if agents := c.StringSlice("inject-agent"); len(agents) > 0 {
				req.InjectAgentIds = &agents
			}
			if projects := c.StringSlice("inject-project"); len(projects) > 0 {
				req.InjectProjectIds = &projects
			}
			if err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
				return api.SetScopedVaultEntry(c.Context, name, req)
			}); err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, map[string]any{"name": name, "set": true})
			}
			o := cli.Stdout(c)
			o.Printf("Vault entry %q set.\n", name)
			return o.Err()
		},
	}
}

func vaultAuditCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "audit",
		Usage: "List recent per-command vault secret uses",
		Flags: []ucli.Flag{
			cli.JSONFlag(),
			&ucli.IntFlag{Name: "limit", Value: 20, Usage: "Maximum rows to list"},
		},
		Action: func(c *ucli.Context) error {
			limit := c.Int("limit")
			list, err := apiclient.Call[apitypes.VaultExecSecretAuditList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListVaultExecSecretAudit(c.Context, &apiclient.ListVaultExecSecretAuditParams{Limit: &limit})
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, list)
			}
			o := cli.Stdout(c)
			if len(list.Entries) == 0 {
				o.Println("No vault secret uses.")
				return o.Err()
			}
			o.Printf("%-20s  %-20s  %-30s  %s\n", "CREATED", "AGENT", "SECRET", "COMMAND")
			for _, e := range list.Entries {
				o.Printf("%-20s  %-20s  %-30s  %s\n", e.CreatedAt.Format("2006-01-02 15:04:05"), cli.Truncate(e.AgentId, 20), cli.Truncate(e.Name, 30), cli.Truncate(e.Command, 80))
			}
			return o.Err()
		},
	}
}

func vaultDeleteCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "delete",
		Usage:     "Delete a vault entry",
		ArgsUsage: "<name>",
		Flags: []ucli.Flag{
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			name := c.Args().First()
			if name == "" {
				return fmt.Errorf("usage: stella vault delete <name>")
			}
			scope := apiclient.DeleteScopedVaultEntryParamsScopeUser
			if err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
				return api.DeleteScopedVaultEntry(c.Context, name, &apiclient.DeleteScopedVaultEntryParams{Scope: &scope})
			}); err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintDeleted(c, name)
			}
			o := cli.Stdout(c)
			o.Printf("Vault entry %q deleted.\n", name)
			return o.Err()
		},
	}
}
