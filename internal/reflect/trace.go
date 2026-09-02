package reflect

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/CherryHQ/stella/internal/platform/observability"
)

var tracer = otel.Tracer("stella/reflect")

// startCycleSpan starts a span covering one full review cycle across all agents.
func startCycleSpan(ctx context.Context, agentCount int) (context.Context, trace.Span) {
	ctx, span := tracer.Start(ctx, "reflect.cycle",
		trace.WithAttributes(
			attribute.String("stella.reflect.operation", "cycle"),
			attribute.Int("stella.reflect.agent_count", agentCount),
		),
	)
	return ctx, span
}

// startAgentSpan starts a span covering the review of one agent's sessions.
func startAgentSpan(ctx context.Context, agentID string) (context.Context, trace.Span) {
	ctx, span := tracer.Start(ctx, "reflect.review_agent",
		trace.WithAttributes(
			attribute.String("agent_id", agentID),
		),
	)
	return ctx, span
}

// startConversationSpan starts a span covering a single conversation review.
func startConversationSpan(ctx context.Context, target reviewTarget) (context.Context, trace.Span) {
	ctx, span := tracer.Start(ctx, "reflect.review_conversation",
		trace.WithAttributes(
			attribute.String("gen_ai.conversation.id", target.session.ID),
			attribute.String("agent_id", target.session.AgentID),
			attribute.String("user_id", target.session.UserID),
			attribute.String("stella.chat.channel", target.session.Channel),
		),
	)
	return ctx, span
}

func startUsageCuratorSpan(ctx context.Context, pair usageCuratorPair, mode UsageCuratorMode) (context.Context, trace.Span) {
	return tracer.Start(ctx, "reflect.usage_curator",
		trace.WithAttributes(
			attribute.String("user_id", pair.UserID),
			attribute.String("agent_id", pair.AgentID),
			attribute.String("stella.reflect.curator.mode", string(mode)),
		),
	)
}

// recordError records an error on a span and sets its status.
func recordError(span trace.Span, err error) {
	observability.RecordSpanError(span, err, "reflect operation failed")
}
