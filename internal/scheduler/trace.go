package scheduler

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type schedulerJobSpanKey struct{}

func startSchedulerJobSpan(ctx context.Context, jobID, runID, agentID, dispatchKind string) (context.Context, trace.Span) {
	ctx, span := otel.Tracer("stella").Start(ctx, "scheduler.job",
		trace.WithAttributes(
			attribute.String("stella.scheduler.job_id", jobID),
			attribute.String("stella.scheduler.run_id", runID),
			attribute.String("stella.scheduler.agent_id", agentID),
			attribute.String("stella.scheduler.dispatch_kind", dispatchKind),
		))
	return context.WithValue(ctx, schedulerJobSpanKey{}, span), span
}

func schedulerJobSpanFromContext(ctx context.Context) trace.Span {
	span, _ := ctx.Value(schedulerJobSpanKey{}).(trace.Span)
	return span
}
