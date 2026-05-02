package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/recally"
)

func recallyCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "recally",
		Usage: "Reading assistant - save, organize, and recall web content",
		Subcommands: []*ucli.Command{
			recallySaveCommand(),
			recallyListCommand(),
			recallySearchCommand(),
			recallyReadCommand(),
			recallyUpdateCommand(),
			recallyDeleteCommand(),
			recallyFeedCommand(),
			recallyDigestCommand(),
		},
	}
}

// resolveUserID authenticates the caller via ANNA_TOKEN and returns their user ID.
func resolveUserID(ctx context.Context, svc *auth.TokenService) (int64, error) {
	token := os.Getenv("ANNA_TOKEN")
	if token == "" {
		return 0, fmt.Errorf("ANNA_TOKEN env var is required")
	}
	user, err := svc.Authenticate(ctx, token)
	if err != nil {
		return 0, fmt.Errorf("ANNA_TOKEN authentication failed: %w", err)
	}
	return user.ID, nil
}

// openRecally opens the DB, authenticates via ANNA_TOKEN, and returns a ready Store.
func openRecally(ctx context.Context) (*recally.Store, int64, *sql.DB, error) {
	dbPath := config.DBPath()
	rawDB, err := db.OpenDB(dbPath)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("open database: %w", err)
	}
	authStore := db.NewAuthStore(rawDB)
	tokenSvc := auth.NewTokenService(authStore, nil)
	userID, err := resolveUserID(ctx, tokenSvc)
	if err != nil {
		_ = rawDB.Close()
		return nil, 0, nil, err
	}
	return recally.NewStore(rawDB), userID, rawDB, nil
}
