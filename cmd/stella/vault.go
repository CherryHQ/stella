package main

import (
	"fmt"
	"io"
	"net/http"
	"os"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/vault"
)

func vaultCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "vault",
		Usage:    "Store and retrieve encrypted secrets (API keys, tokens) available to the agent at runtime",
		Category: "Feature",
		Description: `The vault stores API keys, tokens, and other secrets encrypted at rest
with an age key. Secrets are available to the agent at runtime without
exposing them in configuration files.`,
		Subcommands: []*ucli.Command{
			vaultKeygenCommand(),
			vaultListCommand(),
			vaultGetCommand(),
			vaultSetCommand(),
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
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			list, err := apiclient.Call[apitypes.VaultEntryList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListVaultEntries(c.Context)
			})
			if err != nil {
				return err
			}
			entries := list.Entries
			if c.Bool("json") {
				return printJSON(entries)
			}
			if len(entries) == 0 {
				fmt.Println("No vault entries.")
				return nil
			}
			fmt.Printf("%-30s  %-20s  %-20s\n", "NAME", "CREATED", "UPDATED")
			for _, e := range entries {
				fmt.Printf("%-30s  %-20s  %-20s\n",
					truncate(e.Name, 30), e.CreatedAt.Format("2006-01-02 15:04:05"), e.UpdatedAt.Format("2006-01-02 15:04:05"))
			}
			return nil
		},
	}
}

func vaultGetCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "get",
		Usage:     "Get a vault entry value",
		ArgsUsage: "<name>",
		Flags: []ucli.Flag{
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			name := c.Args().First()
			if name == "" {
				return fmt.Errorf("usage: stella vault get <name>")
			}
			entry, err := apiclient.Call[apiclient.VaultEntryValue](func(api *apiclient.Client) (*http.Response, error) {
				return api.GetVaultEntry(c.Context, name)
			})
			if err != nil {
				return err
			}
			if c.Bool("json") {
				return printJSON(entry)
			}
			fmt.Println(entry.Value)
			return nil
		},
	}
}

func vaultSetCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "set",
		Usage:     "Set a vault entry (use '-' as value to read from stdin)",
		ArgsUsage: "<name> <value>",
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
			if err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
				return api.SetVaultEntry(c.Context, name, apiclient.SetVaultEntryJSONRequestBody{Value: value})
			}); err != nil {
				return err
			}
			fmt.Printf("Vault entry %q set.\n", name)
			return nil
		},
	}
}

func vaultDeleteCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "delete",
		Usage:     "Delete a vault entry",
		ArgsUsage: "<name>",
		Action: func(c *ucli.Context) error {
			name := c.Args().First()
			if name == "" {
				return fmt.Errorf("usage: stella vault delete <name>")
			}
			if err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
				return api.DeleteVaultEntry(c.Context, name)
			}); err != nil {
				return err
			}
			fmt.Printf("Vault entry %q deleted.\n", name)
			return nil
		},
	}
}
