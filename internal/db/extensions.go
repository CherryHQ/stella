package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ExtensionRequirement struct {
	Name            string
	Required        bool
	MinVersion      string
	RequiresPreload bool
}

var postgresExtensions = []ExtensionRequirement{
	{Name: "pg_trgm", Required: true},
	{Name: "vector"},
	{Name: "pg_search", RequiresPreload: true},
}

func ensureExtensions(ctx context.Context, conn *pgxpool.Conn) error {
	return ensureExtensionRequirements(ctx, conn, postgresExtensions)
}

func ensureExtensionRequirements(ctx context.Context, conn *pgxpool.Conn, requirements []ExtensionRequirement) error {
	for _, req := range requirements {
		if !req.Required {
			continue
		}
		if err := checkExtensionAvailable(ctx, conn, req); err != nil {
			return err
		}
		if req.RequiresPreload {
			if err := checkExtensionPreloaded(ctx, conn, req.Name); err != nil {
				return err
			}
		}
		if _, err := conn.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS "+pgx.Identifier{req.Name}.Sanitize()); err != nil {
			return fmt.Errorf("create PostgreSQL extension %q: %w", req.Name, err)
		}
	}
	return nil
}

func checkExtensionAvailable(ctx context.Context, conn *pgxpool.Conn, req ExtensionRequirement) error {
	var defaultVersion string
	err := conn.QueryRow(ctx, "SELECT default_version FROM pg_available_extensions WHERE name = $1", req.Name).Scan(&defaultVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return missingExtensionError(req.Name)
	}
	if err != nil {
		return fmt.Errorf("check PostgreSQL extension %q availability: %w", req.Name, err)
	}
	if req.MinVersion != "" && compareExtensionVersion(defaultVersion, req.MinVersion) < 0 {
		return fmt.Errorf("PostgreSQL extension %q default version %s is below required version %s", req.Name, defaultVersion, req.MinVersion)
	}
	return nil
}

func checkExtensionPreloaded(ctx context.Context, conn *pgxpool.Conn, name string) error {
	var preload string
	if err := conn.QueryRow(ctx, "SHOW shared_preload_libraries").Scan(&preload); err != nil {
		return fmt.Errorf("check PostgreSQL shared_preload_libraries for %q: %w", name, err)
	}
	if !preloadContains(preload, name) {
		return fmt.Errorf("external PostgreSQL is missing %s preload: install %s and set shared_preload_libraries='%s', then restart PostgreSQL", name, name, name)
	}
	return nil
}

func missingExtensionError(name string) error {
	switch name {
	case "pg_search":
		return fmt.Errorf("stella embedded PostgreSQL runtime is missing extension %q. Expected bundle: %s. External PostgreSQL must install pg_search and set shared_preload_libraries='pg_search', then restart PostgreSQL", name, postgresBundleID)
	case "vector":
		return fmt.Errorf("stella embedded PostgreSQL runtime is missing extension %q. Expected bundle: %s. External PostgreSQL must install pgvector before Stella starts", name, postgresBundleID)
	default:
		return fmt.Errorf("PostgreSQL extension %q is not available; install the extension files before starting Stella", name)
	}
}

func preloadContains(value, name string) bool {
	for part := range strings.SplitSeq(value, ",") {
		if strings.TrimSpace(part) == name {
			return true
		}
	}
	return false
}

func compareExtensionVersion(have, want string) int {
	h := splitVersion(have)
	w := splitVersion(want)
	for i := 0; i < len(h) || i < len(w); i++ {
		var hv, wv int
		if i < len(h) {
			hv = h[i]
		}
		if i < len(w) {
			wv = w[i]
		}
		if hv < wv {
			return -1
		}
		if hv > wv {
			return 1
		}
	}
	return 0
}

func splitVersion(version string) []int {
	parts := strings.FieldsFunc(version, func(r rune) bool {
		return r < '0' || r > '9'
	})
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		var n int
		_, _ = fmt.Sscanf(part, "%d", &n)
		out = append(out, n)
	}
	return out
}
