package main

import (
	"fmt"
	"net/http"
	"time"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
)

func oauthCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "oauth",
		Usage:    "Manage OAuth provider connections",
		Category: "Configuration",
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
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			providers, err := apiCall[[]apiclient.OAuthProviderStatus](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListOAuthProviders(c.Context)
			})
			if err != nil {
				return err
			}
			if c.Bool("json") {
				return printJSON(providers)
			}
			if len(providers) == 0 {
				fmt.Println("No OAuth providers configured.")
				return nil
			}
			fmt.Printf("%-20s  %-12s  %-12s  %s\n", "PROVIDER", "CONFIGURED", "CONNECTED", "USERNAME")
			for _, p := range providers {
				username := ""
				if p.Username != nil {
					username = *p.Username
				}
				fmt.Printf("%-20s  %-12v  %-12v  %s\n",
					truncate(p.Provider, 20), p.Configured, p.Connected, username)
			}
			return nil
		},
	}
}

func oauthConnectCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "connect",
		Usage:     "Start OAuth flow to connect a provider",
		ArgsUsage: "<provider>",
		Action: func(c *ucli.Context) error {
			provider := c.Args().First()
			if provider == "" {
				return fmt.Errorf("usage: stella oauth connect <provider>")
			}
			flow, err := apiCall[apiclient.OAuthFlowStatus](func(api *apiclient.Client) (*http.Response, error) {
				return api.StartOAuthFlow(c.Context, provider)
			})
			if err != nil {
				return err
			}

			fmt.Printf("Open this URL to authorize:\n  %s\n", flow.VerificationUri)
			if flow.UserCode != nil {
				fmt.Printf("Enter code: %s\n", *flow.UserCode)
			}
			fmt.Println("\nWaiting for authorization...")

			ticker := time.NewTicker(3 * time.Second)
			defer ticker.Stop()

			for {
				select {
				case <-c.Done():
					return c.Err()
				case <-ticker.C:
					status, err := apiCall[apiclient.OAuthFlowStatus](func(api *apiclient.Client) (*http.Response, error) {
						return api.PollOAuthFlow(c.Context, provider, flow.FlowId)
					})
					if err != nil {
						return err
					}
					switch status.State {
					case "completed":
						fmt.Printf("Connected to %s.\n", provider)
						return nil
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
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			provider := c.Args().First()
			if provider == "" {
				return fmt.Errorf("usage: stella oauth status <provider>")
			}
			status, err := apiCall[apiclient.OAuthConnectedResponse](func(api *apiclient.Client) (*http.Response, error) {
				return api.GetOAuthConnected(c.Context, provider)
			})
			if err != nil {
				return err
			}
			if c.Bool("json") {
				return printJSON(status)
			}
			if status.Connected {
				username := ""
				if status.Username != nil {
					username = " (" + *status.Username + ")"
				}
				fmt.Printf("%s: connected%s\n", provider, username)
			} else {
				fmt.Printf("%s: not connected\n", provider)
			}
			return nil
		},
	}
}

func oauthDisconnectCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "disconnect",
		Usage:     "Disconnect an OAuth provider",
		ArgsUsage: "<provider>",
		Action: func(c *ucli.Context) error {
			provider := c.Args().First()
			if provider == "" {
				return fmt.Errorf("usage: stella oauth disconnect <provider>")
			}
			if err := apiDo(func(api *apiclient.Client) (*http.Response, error) {
				return api.DisconnectOAuth(c.Context, provider)
			}); err != nil {
				return err
			}
			fmt.Printf("Disconnected from %s.\n", provider)
			return nil
		},
	}
}
