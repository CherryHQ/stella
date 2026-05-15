package main

import (
	"fmt"
	"io"
	"os"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
)

func vaultCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "vault",
		Usage: "Manage encrypted secrets",
		Subcommands: []*ucli.Command{
			vaultListCommand(),
			vaultGetCommand(),
			vaultSetCommand(),
			vaultDeleteCommand(),
		},
	}
}

func vaultAPI() (*apiclient.Client, error) {
	return newAPIClient()
}

func vaultListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List vault entries",
		Flags: []ucli.Flag{
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			api, err := vaultAPI()
			if err != nil {
				return err
			}
			resp, err := api.ListVaultEntries(c.Context)
			if err != nil {
				return wrapServerErr(err)
			}
			defer resp.Body.Close() //nolint:errcheck
			var entries []apiclient.VaultEntry
			if err := decodeDataJSON(resp, &entries); err != nil {
				return err
			}
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
					truncate(e.Name, 30), truncate(e.CreatedAt, 20), truncate(e.UpdatedAt, 20))
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
			api, err := vaultAPI()
			if err != nil {
				return err
			}
			resp, err := api.GetVaultEntry(c.Context, name)
			if err != nil {
				return wrapServerErr(err)
			}
			defer resp.Body.Close() //nolint:errcheck
			var entry apiclient.VaultEntryValue
			if err := decodeDataJSON(resp, &entry); err != nil {
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
			api, err := vaultAPI()
			if err != nil {
				return err
			}
			resp, err := api.SetVaultEntry(c.Context, name, apiclient.SetVaultEntryJSONRequestBody{
				Value: value,
			})
			if err != nil {
				return wrapServerErr(err)
			}
			defer resp.Body.Close() //nolint:errcheck
			if err := decodeDataJSON(resp, nil); err != nil {
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
			api, err := vaultAPI()
			if err != nil {
				return err
			}
			resp, err := api.DeleteVaultEntry(c.Context, name)
			if err != nil {
				return wrapServerErr(err)
			}
			defer resp.Body.Close() //nolint:errcheck
			if err := decodeDataJSON(resp, nil); err != nil {
				return err
			}
			fmt.Printf("Vault entry %q deleted.\n", name)
			return nil
		},
	}
}
