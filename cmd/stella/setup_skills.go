package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/config"
	skills "github.com/CherryHQ/stella/internal/skills"
	"github.com/CherryHQ/stella/resources"
)

const cliSkillsUserID = "1"

func cliUserSkillsDir(snap *config.Snapshot) (string, error) {
	userDir, err := agent.SetupUserWorkspace(snap.AgentID, config.StellaHome(), cliSkillsUserID)
	if err != nil {
		return "", err
	}
	return agent.UserSkillsDir(userDir), nil
}

type skillStores struct {
	raw      *skills.SQLiteStore
	diskSync *skills.DiskSyncStore
}

func setupSkillStores(ctx context.Context, db *sql.DB, orgID string) (skillStores, error) {
	raw := skills.New(db)
	raw.SetDefaultOrgID(orgID)
	diskSync := skills.NewDiskSyncStore(raw, func(scope, agentID string, userID string) string {
		base := config.StellaHome()
		switch scope {
		case "system":
			return filepath.Join(base, ".agents", "skills")
		case "agent":
			if agentID == "" {
				return ""
			}
			return filepath.Join(base, "workspaces", agentID, ".agents", "skills")
		case "user":
			if agentID == "" || userID == "" {
				return ""
			}
			return filepath.Join(base, "workspaces", agentID, "users", userID, ".agents", "skills")
		default:
			return ""
		}
	})

	builtinSkillsFS, ok := resources.SubFS(resources.KindSkill)
	if !ok {
		return skillStores{}, fmt.Errorf("builtin skills FS unavailable")
	}
	if err := skills.SyncBuiltin(ctx, diskSync, builtinSkillsFS, orgID); err != nil {
		return skillStores{}, fmt.Errorf("sync builtin skills: %w", err)
	}
	if err := diskSync.SyncAllToDisk(ctx); err != nil {
		return skillStores{}, fmt.Errorf("sync skills to disk: %w", err)
	}

	return skillStores{raw: raw, diskSync: diskSync}, nil
}

func migrateFilesystemSkills(ctx context.Context, rawStore *skills.SQLiteStore, snap *config.Snapshot) {
	userSkillsDir, err := cliUserSkillsDir(snap)
	if err != nil {
		slog.Warn("skip filesystem skill migration", "error", err)
		return
	}
	fsCfg := skills.MigrateFSConfig{
		AgentRoot:     snap.Workspace,
		AgentID:       snap.AgentID,
		UserSkillsDir: userSkillsDir,
		UserID:        cliSkillsUserID,
	}
	fsResult, fsErr := skills.MigrateFilesystem(ctx, rawStore, fsCfg)
	if fsErr != nil {
		slog.Warn("filesystem skill migration failed", "error", fsErr)
	} else if fsResult.Imported > 0 {
		slog.Info("migrated on-disk skills", "imported", fsResult.Imported, "skipped", fsResult.Skipped)
	}
}
