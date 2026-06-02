package main

import (
	"fmt"
	"net/http"
	"time"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
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
			jsonFlag(),
		},
		Action: func(c *ucli.Context) error {
			list, err := apiclient.Call[apitypes.OAuthProviderStatusList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListOAuthProviders(c.Context)
			})
			if err != nil {
				return err
			}
			if isJSON(c) {
				return printJSON(c, list)
			}
			providers := list.Providers
			o := stdout(c)
			if len(providers) == 0 {
				o.println("No OAuth providers configured.")
				return o.Err()
			}
			o.printf("%-20s  %-12s  %-12s  %s\n", "PROVIDER", "CONFIGURED", "CONNECTED", "USERNAME")
			for _, p := range providers {
				username := ""
				if p.Username != nil {
					username = *p.Username
				}
				o.printf("%-20s  %-12v  %-12v  %s\n",
					truncate(p.Provider, 20), p.Configured, p.Connected, username)
			}
			return o.Err()
		},
	}
}

func oauthConnectCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "connect",
		Usage:     "Start OAuth flow to connect a provider",
		ArgsUsage: "<provider>",
		Flags: []ucli.Flag{
			jsonFlag(),
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

			e := stderr(c)
			e.printf("Open this URL to authorize:\n  %s\n", flow.VerificationUri)
			if flow.UserCode != nil {
				e.printf("Enter code: %s\n", *flow.UserCode)
			}
			e.println("\nWaiting for authorization...")

			ticker := time.NewTicker(3 * time.Second)
			defer ticker.Stop()

			for {
				select {
				case <-c.Done():
					return c.Err()
				case <-ticker.C:
					status, err := apiclient.Call[apiclient.OAuthFlowStatus](func(api *apiclient.Client) (*http.Response, error) {
						return api.PollOAuthFlow(c.Context, provider, flow.FlowId)
					})
					if err != nil {
						return err
					}
					switch status.State {
					case "completed":
						if isJSON(c) {
							return printJSON(c, status)
						}
						o := stdout(c)
						o.printf("Connected to %s.\n", provider)
						return o.Err()
					case "failed":
						return fmt.Errorf("OAuth flow failed for %s", provider)
					case "expired":
						return fmt.Errorf("OAuth flow expired for %s", provider)
					}
				}
			}
		},
	}
}

func oauthStatusCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "status",
		Usage:     "Check connection status for a provider",
		ArgsUsage: "<provider>",
		Flags: []ucli.Flag{
			jsonFlag(),
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
			if isJSON(c) {
				return printJSON(c, status)
			}
			o := stdout(c)
			if status.Connected {
				username := ""
				if status.Username != nil {
					username = " (" + *status.Username + ")"
				}
				o.printf("%s: connected%s\n", provider, username)
			} else {
				o.printf("%s: not connected\n", provider)
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
			jsonFlag(),
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
			if isJSON(c) {
				return printJSON(c, map[string]any{"provider": provider, "connected": false})
			}
			o := stdout(c)
			o.printf("Disconnected from %s.\n", provider)
			return o.Err()
		},
	}
}
