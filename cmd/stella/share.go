package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
)

func shareCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "share",
		Usage:    "Create public share links",
		Category: "Feature",
		Subcommands: []*ucli.Command{
			shareArtifactCommand(),
			shareArticleCommand(),
		},
	}
}

func shareArtifactCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "artifact",
		Usage:     "Create a public share link for a workspace artifact",
		ArgsUsage: "<path>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "agent-id", Usage: "Stella agent ID (defaults to STELLA_AGENT_ID)"},
			&ucli.StringFlag{Name: "session-id", Usage: "Stella session ID (defaults to STELLA_SESSION_ID)"},
			&ucli.StringFlag{Name: "expires-in", Usage: "Expiration: 1h, 1d, 7d, never", Value: "7d"},
		},
		Action: func(c *ucli.Context) error {
			path := c.Args().First()
			if path == "" {
				return fmt.Errorf("path is required")
			}
			agentID := c.String("agent-id")
			if agentID == "" {
				agentID = os.Getenv("STELLA_AGENT_ID")
			}
			if agentID == "" {
				return fmt.Errorf("agent ID is required (pass --agent-id or run inside an agent session with STELLA_AGENT_ID)")
			}
			sessionID := c.String("session-id")
			if sessionID == "" {
				sessionID = os.Getenv("STELLA_SESSION_ID")
			}
			if sessionID == "" {
				return fmt.Errorf("session ID is required (pass --session-id or run inside an agent session with STELLA_SESSION_ID)")
			}
			expiresIn := apitypes.CreateShareRequestExpiresIn(c.String("expires-in"))
			source := apitypes.CreateShareRequestSourceArtifact
			share, err := createShare(c.Context, apiclient.CreateShareJSONRequestBody{
				Source:    source,
				AgentId:   &agentID,
				SessionId: &sessionID,
				Path:      &path,
				ExpiresIn: &expiresIn,
			})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(c.App.Writer, share.Url)
			return err
		},
	}
}

func shareArticleCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "article",
		Usage:     "Create a public share link for a Recally article",
		ArgsUsage: "<article-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "expires-in", Usage: "Expiration: 1h, 1d, 7d, never", Value: "7d"},
		},
		Action: func(c *ucli.Context) error {
			articleID := c.Args().First()
			if articleID == "" {
				return fmt.Errorf("article ID is required")
			}
			expiresIn := apitypes.CreateShareRequestExpiresIn(c.String("expires-in"))
			source := apitypes.CreateShareRequestSourceArticle
			share, err := createShare(c.Context, apiclient.CreateShareJSONRequestBody{
				Source:    source,
				ArticleId: &articleID,
				ExpiresIn: &expiresIn,
			})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(c.App.Writer, share.Url)
			return err
		},
	}
}

func createShare(ctx context.Context, body apiclient.CreateShareJSONRequestBody) (apitypes.Share, error) {
	return apiclient.Call[apitypes.Share](func(api *apiclient.Client) (*http.Response, error) {
		return api.CreateShare(ctx, body)
	})
}
