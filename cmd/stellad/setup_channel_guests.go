package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/internal/scheduler"
)

const channelGuestRetentionJobName = "channel-guest-retention"

func registerChannelGuestRetentionBuiltin(svc *scheduler.Service, retention channel.GuestRetention) error {
	if err := svc.RegisterBuiltin(scheduler.BuiltinJob{
		Name:     channelGuestRetentionJobName,
		Schedule: scheduler.Schedule{Every: (24 * time.Hour).String()},
		Handler: func(ctx context.Context, _ scheduler.Job) error {
			deleted, err := retention.PurgeExpired(ctx)
			if err != nil {
				return fmt.Errorf("run channel guest retention: %w", err)
			}
			slog.Info("channel guests: retention purge complete", "deleted", deleted)
			return nil
		},
	}); err != nil {
		return fmt.Errorf("register channel guest retention builtin: %w", err)
	}
	return nil
}
