package main

import (
	"fmt"
	"net/http"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/cli"
)

func authCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "auth",
		Usage:    "Manage authentication and user identities",
		Category: "Admin",
		Subcommands: []*ucli.Command{
			authLinkUserCommand(),
		},
	}
}

func authLinkUserCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "link-user",
		Usage: "Link an OIDC login identity to a user (admin recovery)",
		Description: `Creates an OIDC login identity for an existing user, allowing them to
log in via the configured OIDC provider. Useful for migrating legacy
username/password accounts or recovering access after an identity loss.

Requires an active stella server and admin credentials.`,
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:     "user-id",
				Usage:    "User ID to link the identity to",
				Required: true,
			},
			&ucli.StringFlag{
				Name:     "provider",
				Usage:    "OIDC provider identifier (issuer URL or short name, e.g. 'local')",
				Required: true,
			},
			&ucli.StringFlag{
				Name:     "provider-subject",
				Usage:    "Subject claim from the provider (e.g. user ID at the provider)",
				Required: true,
			},
			&ucli.StringFlag{
				Name:     "email",
				Usage:    "Email address associated with this login identity",
				Required: true,
			},
			&ucli.StringFlag{
				Name:  "name",
				Usage: "Display name for this identity (optional)",
			},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			req := apitypes.LinkLoginIdentityRequest{
				Provider:        c.String("provider"),
				ProviderSubject: c.String("provider-subject"),
				Email:           c.String("email"),
			}
			if name := c.String("name"); name != "" {
				req.Name = &name
			}
			userID := c.String("user-id")

			identity, err := apiclient.Call[apitypes.LoginIdentity](func(api *apiclient.Client) (*http.Response, error) {
				return api.LinkAuthUserLoginIdentity(c.Context, userID, req)
			})
			if err != nil {
				return fmt.Errorf("link identity: %w", err)
			}

			if cli.IsJSON(c) {
				return cli.PrintJSON(c, identity)
			}
			o := cli.Stdout(c)
			o.Printf("Linked identity %s\n", identity.Id)
			o.Printf("  User:     %s\n", identity.UserId)
			o.Printf("  Provider: %s\n", identity.Provider)
			o.Printf("  Subject:  %s\n", identity.ProviderSubject)
			o.Printf("  Email:    %s\n", identity.Email)
			return o.Err()
		},
	}
}
