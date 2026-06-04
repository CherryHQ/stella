package main

import (
	"context"
	"fmt"
	"net/http"

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
			&ucli.StringFlag{Name: "expires-in", Usage: "Expiration: 1h, 1d, 7d, never", Value: "7d"},
			jsonFlag(),
		},
		Action: func(c *ucli.Context) error {
			path := c.Args().First()
			if path == "" {
				return fmt.Errorf("path is required")
			}
			claims, err := scopedTokenClaimsFromEnv()
			if err != nil {
				return err
			}
			agentID := claims.AgentID
			if agentID == "" {
				return fmt.Errorf("agent ID is required in STELLA_TOKEN")
			}
			sessionID := claims.SessionID
			if sessionID == "" {
				return fmt.Errorf("session ID is required in STELLA_TOKEN")
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
			if isJSON(c) {
				return printJSON(c, share)
			}
			o := stdout(c)
			o.println(share.Url)
			return o.Err()
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
			jsonFlag(),
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
			if isJSON(c) {
				return printJSON(c, share)
			}
			o := stdout(c)
			o.println(share.Url)
			return o.Err()
		},
	}
}

func createShare(ctx context.Context, body apiclient.CreateShareJSONRequestBody) (apitypes.Share, error) {
	return apiclient.Call[apitypes.Share](func(api *apiclient.Client) (*http.Response, error) {
		return api.CreateShare(ctx, body)
	})
}
