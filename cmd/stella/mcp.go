package main

import (
	"fmt"
	"net/http"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/cli"
)

func mcpCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "mcp",
		Usage:    "Manage external MCP (Model Context Protocol) servers agents can connect to",
		Category: "Feature",
		Description: `Register external MCP servers over HTTP-based transports (streamable HTTP
or SSE; stdio is not supported). Their tools are surfaced into the agent tool
registry, namespaced as mcp__<server>__<tool>. Bearer tokens are stored
encrypted in the vault under the same scope as the registration.

Scopes mirror skills and the vault: user, user_agent, system, system_agent.`,
		Subcommands: []*ucli.Command{
			mcpListCommand(),
			mcpAddCommand(),
			mcpRemoveCommand(),
		},
	}
}

func mcpScopeFlags() []ucli.Flag {
	return []ucli.Flag{
		&ucli.StringFlag{Name: "scope", Value: "user", Usage: "Registration scope: user, user_agent, system, system_agent"},
		&ucli.StringFlag{Name: "agent", Usage: "Agent id (required for *_agent scopes)"},
	}
}

func mcpListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List MCP server registrations in a scope",
		Flags: append(mcpScopeFlags(), cli.JSONFlag()),
		Action: func(c *ucli.Context) error {
			scope := apiclient.ListScopedMCPServersParamsScope(c.String("scope"))
			params := &apiclient.ListScopedMCPServersParams{Scope: &scope}
			if agent := c.String("agent"); agent != "" {
				params.AgentId = &agent
			}
			list, err := apiclient.Call[apitypes.MCPServerList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListScopedMCPServers(c.Context, params)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, list)
			}
			o := cli.Stdout(c)
			if len(list.Servers) == 0 {
				o.Println("No MCP servers.")
				return o.Err()
			}
			o.Printf("%-38s  %-18s  %-16s  %-8s  %s\n", "ID", "NAME", "TRANSPORT", "AUTH", "URL")
			for _, m := range list.Servers {
				o.Printf("%-38s  %-18s  %-16s  %-8s  %s\n",
					m.Id, cli.Truncate(m.Name, 18), string(m.Transport), string(m.AuthType), m.Url)
			}
			return o.Err()
		},
	}
}

func mcpAddCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "add",
		Usage:     "Register an MCP server",
		ArgsUsage: "<name>",
		Flags: append(mcpScopeFlags(),
			&ucli.StringFlag{Name: "url", Required: true, Usage: "MCP server endpoint URL"},
			&ucli.StringFlag{Name: "transport", Value: "streamable_http", Usage: "Transport: streamable_http or sse"},
			&ucli.StringFlag{Name: "auth", Value: "none", Usage: "Auth type: none or bearer"},
			&ucli.StringFlag{Name: "token", Usage: "Bearer token (stored encrypted in the vault; required when --auth bearer)"},
			cli.JSONFlag(),
		),
		Action: func(c *ucli.Context) error {
			name := c.Args().First()
			if name == "" {
				return fmt.Errorf("usage: stella mcp add <name> --url <url>")
			}
			body := apiclient.CreateScopedMCPServerJSONRequestBody{
				Scope: apitypes.CreateMCPServerRequestScope(c.String("scope")),
				Name:  name,
				Url:   c.String("url"),
			}
			if agent := c.String("agent"); agent != "" {
				body.AgentId = &agent
			}
			if transport := c.String("transport"); transport != "" {
				t := apitypes.CreateMCPServerRequestTransport(transport)
				body.Transport = &t
			}
			if auth := c.String("auth"); auth != "" {
				a := apitypes.CreateMCPServerRequestAuthType(auth)
				body.AuthType = &a
			}
			if token := c.String("token"); token != "" {
				body.Token = &token
			}
			created, err := apiclient.Call[apitypes.MCPServer](func(api *apiclient.Client) (*http.Response, error) {
				return api.CreateScopedMCPServer(c.Context, body)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, created)
			}
			o := cli.Stdout(c)
			o.Printf("MCP server %q registered (id %s).\n", created.Name, created.Id)
			return o.Err()
		},
	}
}

func mcpRemoveCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "remove",
		Usage:     "Remove an MCP server registration by id",
		ArgsUsage: "<id>",
		Flags:     append(mcpScopeFlags(), cli.JSONFlag()),
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella mcp remove <id>")
			}
			scope := apiclient.DeleteScopedMCPServerParamsScope(c.String("scope"))
			params := &apiclient.DeleteScopedMCPServerParams{Scope: &scope}
			if agent := c.String("agent"); agent != "" {
				params.AgentId = &agent
			}
			if err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
				return api.DeleteScopedMCPServer(c.Context, id, params)
			}); err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintDeleted(c, id)
			}
			o := cli.Stdout(c)
			o.Printf("MCP server %q removed.\n", id)
			return o.Err()
		},
	}
}
