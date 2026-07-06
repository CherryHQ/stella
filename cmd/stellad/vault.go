package main

import (
	"fmt"

	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/vault"
)

func vaultCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "vault",
		Usage:    "Manage daemon vault bootstrap utilities",
		Category: "System",
		Subcommands: []*ucli.Command{
			vaultKeygenCommand(),
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
