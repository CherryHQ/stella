package channel

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/jackc/pgx/v5/pgxpool"
)

// runBacklogMetrics registers observable gauges for the durable pipeline's
// queue depths: inbox events awaiting routing, queued/running agent runs, and
// outbox ops pending/unknown. Replicas all publish the same cluster-wide
// numbers — the tables are the shared truth.
func (c *Coordinator) runBacklogMetrics(ctx context.Context, db *pgxpool.Pool) {
	if db == nil {
		return
	}
	meter := otel.Meter("stella/channel")
	count := func(query string) metric.Int64ObservableGauge {
		g, err := meter.Int64ObservableGauge(query)
		if err != nil {
			return nil
		}
		return g
	}
	inbox := count("stella.channel.inbox.pending")
	runs := count("stella.agent.run.open")
	outbox := count("stella.channel.outbox.pending")
	unknown := count("stella.channel.outbox.unknown")
	if inbox == nil || runs == nil || outbox == nil || unknown == nil {
		return
	}
	query := func(qs ...string) []int64 {
		out := make([]int64, len(qs))
		for i, q := range qs {
			_ = db.QueryRow(ctx, q).Scan(&out[i])
		}
		return out
	}
	_, _ = meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		v := query(
			`SELECT count(*) FROM channel_inbox WHERE state IN ('received','ready')`,
			`SELECT count(*) FROM agent_run WHERE state IN ('queued','running')`,
			`SELECT count(*) FROM channel_outbox WHERE state IN ('pending','claimed')`,
			`SELECT count(*) FROM channel_outbox WHERE state = 'unknown'`,
		)
		o.ObserveInt64(inbox, v[0])
		o.ObserveInt64(runs, v[1])
		o.ObserveInt64(outbox, v[2])
		o.ObserveInt64(unknown, v[3])
		return nil
	}, inbox, runs, outbox, unknown)
	<-ctx.Done()
}
