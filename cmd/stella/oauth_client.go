package main

import (
	"fmt"
	"net/http"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/cli"
)

func oauthServerCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "oauth-server",
		Usage:    "Manage OAuth2 clients and authorized apps (Stella as authorization server)",
		Category: "Feature",
		Description: `Stella is an OAuth2 authorization server: third-party apps obtain scoped,
revocable access to your account via authorization_code + PKCE. Register a
client here, then point the app at /oauth/authorize and /oauth/token. The
client secret is shown only once, at creation and on rotate.`,
		Subcommands: []*ucli.Command{
			oauthClientCommand(),
			oauthAppsCommand(),
		},
	}
}

func oauthClientCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "client",
		Usage: "Manage your registered OAuth2 clients",
		Subcommands: []*ucli.Command{
			oauthClientListCommand(),
			oauthClientScopesCommand(),
			oauthClientCreateCommand(),
			oauthClientRotateCommand(),
			oauthClientDisableCommand(),
		},
	}
}

func oauthClientListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List your OAuth2 clients",
		Flags: []ucli.Flag{cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			list, err := apiclient.Call[apitypes.OAuthClientList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListOAuthClients(c.Context)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, list)
			}
			o := cli.Stdout(c)
			if len(list.OauthClients) == 0 {
				o.Println("No OAuth2 clients.")
				return o.Err()
			}
			o.Printf("%-42s  %-20s  %-12s  %-9s  %s\n", "CLIENT ID", "NAME", "TYPE", "STATUS", "SCOPES")
			for _, cl := range list.OauthClients {
				status := "active"
				if cl.Disabled {
					status = "disabled"
				}
				o.Printf("%-42s  %-20s  %-12s  %-9s  %v\n",
					cl.ClientId, cli.Truncate(cl.Name, 20), cl.ClientType, status, cl.Scopes)
			}
			return o.Err()
		},
	}
}

func oauthClientScopesCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "scopes",
		Usage: "List the scopes grantable to an OAuth2 client",
		Flags: []ucli.Flag{cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			list, err := apiclient.Call[apitypes.OAuthClientScopeList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListOAuthClientScopes(c.Context)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, list)
			}
			o := cli.Stdout(c)
			o.Printf("%-18s  %s\n", "SCOPE", "DESCRIPTION")
			for _, s := range list.Scopes {
				o.Printf("%-18s  %s\n", s.Id, s.Description)
			}
			return o.Err()
		},
	}
}

func oauthClientCreateCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "create",
		Usage:     "Register an OAuth2 client (secret shown once)",
		ArgsUsage: "<name>",
		Flags: []ucli.Flag{
			&ucli.StringSliceFlag{
				Name:     "redirect-uri",
				Aliases:  []string{"r"},
				Usage:    "Allowed redirect URI (repeatable)",
				Required: true,
			},
			&ucli.StringSliceFlag{
				Name:     "scope",
				Aliases:  []string{"s"},
				Usage:    "Grant a scope (repeatable), e.g. --scope goals:read",
				Required: true,
			},
			&ucli.BoolFlag{
				Name:  "public",
				Usage: "Register a public client (no secret; PKCE required)",
			},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			name := c.Args().First()
			if name == "" {
				return fmt.Errorf("usage: stella oauth client create <name> --redirect-uri <uri> --scope <scope>")
			}
			body := apitypes.CreateOAuthClientRequest{
				Name:         name,
				RedirectUris: c.StringSlice("redirect-uri"),
				Scopes:       c.StringSlice("scope"),
			}
			ct := apitypes.CreateOAuthClientRequestClientTypeConfidential
			if c.Bool("public") {
				ct = apitypes.CreateOAuthClientRequestClientTypePublic
			}
			body.ClientType = &ct
			resp, err := apiclient.Call[apitypes.CreateOAuthClientResponse](func(api *apiclient.Client) (*http.Response, error) {
				return api.CreateOAuthClient(c.Context, body)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, resp)
			}
			o := cli.Stdout(c)
			o.Printf("Created client %q.\n", resp.OauthClient.Name)
			o.Printf("client_id: %s\n", resp.OauthClient.ClientId)
			if resp.ClientSecret != nil && *resp.ClientSecret != "" {
				o.Println("Copy the client secret now; it will not be shown again:")
				o.Println(*resp.ClientSecret)
			}
			return o.Err()
		},
	}
}

func oauthClientRotateCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "rotate-secret",
		Usage:     "Rotate a confidential client's secret (new secret shown once)",
		ArgsUsage: "<client_id>",
		Flags:     []ucli.Flag{cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella oauth client rotate-secret <client_id>")
			}
			resp, err := apiclient.Call[apitypes.RotateOAuthClientSecretResponse](func(api *apiclient.Client) (*http.Response, error) {
				return api.RotateOAuthClientSecret(c.Context, id)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, resp)
			}
			o := cli.Stdout(c)
			o.Println("New client secret (copy it now; it will not be shown again):")
			o.Println(resp.ClientSecret)
			return o.Err()
		},
	}
}

func oauthClientDisableCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "disable",
		Usage:     "Disable an OAuth2 client by client_id",
		ArgsUsage: "<client_id>",
		Flags:     []ucli.Flag{cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella oauth client disable <client_id>")
			}
			if err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
				return api.DisableOAuthClient(c.Context, id)
			}); err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintDeleted(c, id)
			}
			o := cli.Stdout(c)
			o.Printf("Client %q disabled.\n", id)
			return o.Err()
		},
	}
}

func oauthAppsCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "apps",
		Usage: "List and revoke apps you have authorized",
		Subcommands: []*ucli.Command{
			{
				Name:  "list",
				Usage: "List apps you have authorized",
				Flags: []ucli.Flag{cli.JSONFlag()},
				Action: func(c *ucli.Context) error {
					list, err := apiclient.Call[apitypes.AuthorizedAppList](func(api *apiclient.Client) (*http.Response, error) {
						return api.ListAuthorizedApps(c.Context)
					})
					if err != nil {
						return err
					}
					if cli.IsJSON(c) {
						return cli.PrintJSON(c, list)
					}
					o := cli.Stdout(c)
					if len(list.AuthorizedApps) == 0 {
						o.Println("No authorized apps.")
						return o.Err()
					}
					o.Printf("%-42s  %-20s  %s\n", "CLIENT ID", "NAME", "SCOPES")
					for _, a := range list.AuthorizedApps {
						o.Printf("%-42s  %-20s  %v\n", a.ClientId, cli.Truncate(a.ClientName, 20), a.Scopes)
					}
					return o.Err()
				},
			},
			{
				Name:      "revoke",
				Usage:     "Revoke your grant to an app by client_id",
				ArgsUsage: "<client_id>",
				Flags:     []ucli.Flag{cli.JSONFlag()},
				Action: func(c *ucli.Context) error {
					id := c.Args().First()
					if id == "" {
						return fmt.Errorf("usage: stella oauth apps revoke <client_id>")
					}
					if err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
						return api.RevokeAuthorizedApp(c.Context, id)
					}); err != nil {
						return err
					}
					if cli.IsJSON(c) {
						return cli.PrintDeleted(c, id)
					}
					o := cli.Stdout(c)
					o.Printf("Revoked access for %q.\n", id)
					return o.Err()
				},
			},
		},
	}
}
