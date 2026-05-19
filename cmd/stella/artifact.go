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

func artifactCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "artifact",
		Usage:    "Manage generated artifacts",
		Category: "Feature",
		Subcommands: []*ucli.Command{
			artifactShareCommand(),
		},
	}
}

func artifactShareCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "share",
		Usage:     "Create a public share link for a workspace artifact",
		ArgsUsage: "<path>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "session-id", Usage: "Stella session ID (defaults to STELLA_SESSION_ID)"},
			&ucli.StringFlag{Name: "expires-in", Usage: "Expiration: 1h, 1d, 7d, never", Value: "7d"},
		},
		Action: func(c *ucli.Context) error {
			path := c.Args().First()
			if path == "" {
				return fmt.Errorf("path is required")
			}
			sessionID := c.String("session-id")
			if sessionID == "" {
				sessionID = os.Getenv("STELLA_SESSION_ID")
			}
			if sessionID == "" {
				return fmt.Errorf("session ID is required (pass --session-id or run inside an agent session with STELLA_SESSION_ID)")
			}
			expiresIn := apitypes.CreateArtifactShareRequestExpiresIn(c.String("expires-in"))
			share, err := createArtifactShare(c.Context, sessionID, path, expiresIn)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(c.App.Writer, share.Url)
			return err
		},
	}
}

func createArtifactShare(ctx context.Context, sessionID, path string, expiresIn apitypes.CreateArtifactShareRequestExpiresIn) (apitypes.ArtifactShare, error) {
	return apiclient.Call[apitypes.ArtifactShare](func(api *apiclient.Client) (*http.Response, error) {
		return api.CreateArtifactShare(ctx, apiclient.CreateArtifactShareJSONRequestBody{
			SessionId: sessionID,
			Path:      path,
			ExpiresIn: &expiresIn,
		})
	})
}
