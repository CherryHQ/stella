package main

import (
	"fmt"
	"net/http"
	"time"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/cli"
)

func tokenCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "token",
		Usage:    "Manage personal access tokens for external API access",
		Category: "Feature",
		Description: `Personal access tokens (PATs) are user-owned, statically scoped bearer
credentials for calling the stella HTTP API from scripts and integrations.
The plaintext token is shown only once, at creation. Grant least-privilege
scopes; list grantable scopes with "stella token scopes".`,
		Subcommands: []*ucli.Command{
			tokenListCommand(),
			tokenScopesCommand(),
			tokenCreateCommand(),
			tokenRevokeCommand(),
		},
	}
}

func tokenListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List personal access tokens",
		Flags: []ucli.Flag{cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			list, err := apiclient.Call[apitypes.PersonalAccessTokenList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListPersonalAccessTokens(c.Context)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, list)
			}
			o := cli.Stdout(c)
			toks := list.Tokens
			if len(toks) == 0 {
				o.Println("No personal access tokens.")
				return o.Err()
			}
			o.Printf("%-38s  %-20s  %-10s  %-12s  %s\n", "ID", "NAME", "STATUS", "EXPIRES", "SCOPES")
			for _, t := range toks {
				o.Printf("%-38s  %-20s  %-10s  %-12s  %v\n",
					t.Id, cli.Truncate(t.Name, 20), patStatus(t), patExpiry(t), t.Scopes)
			}
			return o.Err()
		},
	}
}

func tokenScopesCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "scopes",
		Usage: "List the scopes grantable to a personal access token",
		Flags: []ucli.Flag{cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			list, err := apiclient.Call[apitypes.PersonalAccessTokenScopeList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListTokenScopes(c.Context)
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

func tokenCreateCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "create",
		Usage:     "Create a personal access token (plaintext shown once)",
		ArgsUsage: "<name>",
		Flags: []ucli.Flag{
			&ucli.StringSliceFlag{
				Name:     "scope",
				Aliases:  []string{"s"},
				Usage:    "Grant a scope (repeatable), e.g. --scope goals:read --scope workflows:*",
				Required: true,
			},
			&ucli.DurationFlag{
				Name:  "ttl",
				Usage: "Lifetime before expiry (e.g. 720h). Omit for the server default; use --no-expiry to opt out.",
			},
			&ucli.BoolFlag{
				Name:  "no-expiry",
				Usage: "Create a token that never expires (opt-in)",
			},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			name := c.Args().First()
			if name == "" {
				return fmt.Errorf("usage: stella token create <name> --scope <scope> [--scope ...]")
			}
			body := apitypes.CreatePersonalAccessTokenRequest{
				Name:   name,
				Scopes: c.StringSlice("scope"),
			}
			switch {
			case c.Bool("no-expiry"):
				never := true
				body.NeverExpires = &never
			case c.IsSet("ttl"):
				exp := time.Now().UTC().Add(c.Duration("ttl"))
				body.ExpiresAt = &exp
			}
			resp, err := apiclient.Call[apitypes.CreatePersonalAccessTokenResponse](func(api *apiclient.Client) (*http.Response, error) {
				return api.CreatePersonalAccessToken(c.Context, body)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, resp)
			}
			o := cli.Stdout(c)
			o.Printf("Created token %q (%s).\n", resp.PersonalAccessToken.Name, resp.PersonalAccessToken.Id)
			o.Println("Copy it now; it will not be shown again:")
			o.Println(resp.Token)
			return o.Err()
		},
	}
}

func tokenRevokeCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "revoke",
		Usage:     "Revoke a personal access token by ID",
		ArgsUsage: "<id>",
		Flags:     []ucli.Flag{cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella token revoke <id>")
			}
			if err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
				return api.RevokePersonalAccessToken(c.Context, id)
			}); err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintDeleted(c, id)
			}
			o := cli.Stdout(c)
			o.Printf("Token %q revoked.\n", id)
			return o.Err()
		},
	}
}

func patStatus(t apitypes.PersonalAccessToken) string {
	switch {
	case t.RevokedAt != nil:
		return "revoked"
	case t.ExpiresAt != nil && !t.ExpiresAt.After(time.Now()):
		return "expired"
	default:
		return "active"
	}
}

func patExpiry(t apitypes.PersonalAccessToken) string {
	if t.ExpiresAt == nil {
		return "never"
	}
	return t.ExpiresAt.Format("2006-01-02")
}
