package main

import (
	"fmt"
	"net/http"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/cli"
)

func oauthCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "oauth",
		Usage:    "Connect external services (GitHub, Google, etc.) via OAuth",
		Category: "Feature",
		Description: `Connect external services (GitHub, Google, etc.) via OAuth so the agent
can access them on your behalf. Use "providers" to see what is available,
"connect" to start a new flow, and "status" to check existing connections.`,
		Subcommands: []*ucli.Command{
			oauthProvidersCommand(),
			oauthConnectCommand(),
			oauthStatusCommand(),
			oauthDisconnectCommand(),
		},
	}
}

func oauthProvidersCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "providers",
		Usage: "List available OAuth providers",
		Flags: []ucli.Flag{
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			list, err := apiclient.Call[apitypes.OAuthProviderStatusList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListOAuthProviders(c.Context)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, list)
			}
			providers := list.Providers
			o := cli.Stdout(c)
			if len(providers) == 0 {
				o.Println("No OAuth providers configured.")
				return o.Err()
			}
			o.Printf("%-20s  %-12s  %-12s  %s\n", "PROVIDER", "CONFIGURED", "CONNECTED", "USERNAME")
			for _, p := range providers {
				username := ""
				if p.Username != nil {
					username = *p.Username
				}
				o.Printf("%-20s  %-12v  %-12v  %s\n",
					cli.Truncate(p.Provider, 20), p.Configured, p.Connected, username)
			}
			return o.Err()
		},
	}
}

func oauthConnectCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "connect",
		Usage:     "Print OAuth authorization URL and code to authorize a provider",
		ArgsUsage: "<provider>",
		Flags: []ucli.Flag{
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			provider := c.Args().First()
			if provider == "" {
				return fmt.Errorf("usage: stella oauth connect <provider>")
			}
			flow, err := apiclient.Call[apiclient.OAuthFlowStatus](func(api *apiclient.Client) (*http.Response, error) {
				return api.StartOAuthFlow(c.Context, provider)
			})
			if err != nil {
				return err
			}

			if cli.IsJSON(c) {
				return cli.PrintJSON(c, flow)
			}

			o := cli.Stdout(c)
			o.Printf("To authorize, open this URL and enter the code:\n  URL:  %s\n", flow.VerificationUri)
			if flow.UserCode != nil {
				o.Printf("  Code: %s\n", *flow.UserCode)
			}
			return o.Err()
		},
	}
}

func oauthStatusCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "status",
		Usage:     "Check connection status for a provider",
		ArgsUsage: "<provider>",
		Flags: []ucli.Flag{
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			provider := c.Args().First()
			if provider == "" {
				return fmt.Errorf("usage: stella oauth status <provider>")
			}
			status, err := apiclient.Call[apiclient.OAuthConnectedResponse](func(api *apiclient.Client) (*http.Response, error) {
				return api.GetOAuthConnected(c.Context, provider)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, status)
			}
			o := cli.Stdout(c)
			if status.Connected {
				username := ""
				if status.Username != nil {
					username = " (" + *status.Username + ")"
				}
				o.Printf("%s: connected%s\n", provider, username)
			} else {
				o.Printf("%s: not connected\n", provider)
			}
			return o.Err()
		},
	}
}

func oauthDisconnectCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "disconnect",
		Usage:     "Disconnect an OAuth provider",
		ArgsUsage: "<provider>",
		Flags: []ucli.Flag{
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			provider := c.Args().First()
			if provider == "" {
				return fmt.Errorf("usage: stella oauth disconnect <provider>")
			}
			if err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
				return api.DisconnectOAuth(c.Context, provider)
			}); err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, map[string]any{"provider": provider, "connected": false})
			}
			o := cli.Stdout(c)
			o.Printf("Disconnected from %s.\n", provider)
			return o.Err()
		},
	}
}
