package main

import (
	"fmt"
	"time"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
)

func oauthCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "oauth",
		Usage: "Manage OAuth provider connections",
		Subcommands: []*ucli.Command{
			oauthProvidersCommand(),
			oauthConnectCommand(),
			oauthStatusCommand(),
			oauthDisconnectCommand(),
		},
	}
}

func oauthAPI() (*apiclient.Client, error) {
	return newAPIClient()
}

func oauthProvidersCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "providers",
		Usage: "List available OAuth providers",
		Flags: []ucli.Flag{
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			api, err := oauthAPI()
			if err != nil {
				return err
			}
			resp, err := api.ListOAuthProviders(c.Context)
			if err != nil {
				return wrapServerErr(err)
			}
			defer resp.Body.Close() //nolint:errcheck
			var providers []apiclient.OAuthProviderStatus
			if err := decodeDataJSON(resp, &providers); err != nil {
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
			api, err := oauthAPI()
			if err != nil {
				return err
			}
			resp, err := api.StartOAuthFlow(c.Context, provider)
			if err != nil {
				return wrapServerErr(err)
			}
			defer resp.Body.Close() //nolint:errcheck
			var flow apiclient.OAuthFlowStatus
			if err := decodeDataJSON(resp, &flow); err != nil {
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
					pollResp, err := api.PollOAuthFlow(c.Context, provider, flow.FlowId)
					if err != nil {
						return wrapServerErr(err)
					}
					var status apiclient.OAuthFlowStatus
					if err := decodeDataJSON(pollResp, &status); err != nil {
						pollResp.Body.Close() //nolint:errcheck
						return err
					}
					pollResp.Body.Close() //nolint:errcheck

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
			api, err := oauthAPI()
			if err != nil {
				return err
			}
			resp, err := api.GetOAuthConnected(c.Context, provider)
			if err != nil {
				return wrapServerErr(err)
			}
			defer resp.Body.Close() //nolint:errcheck
			var status apiclient.OAuthConnectedResponse
			if err := decodeDataJSON(resp, &status); err != nil {
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
			api, err := oauthAPI()
			if err != nil {
				return err
			}
			resp, err := api.DisconnectOAuth(c.Context, provider)
			if err != nil {
				return wrapServerErr(err)
			}
			defer resp.Body.Close() //nolint:errcheck
			if err := decodeDataJSON(resp, nil); err != nil {
				return err
			}
			fmt.Printf("Disconnected from %s.\n", provider)
			return nil
		},
	}
}
