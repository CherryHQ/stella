package reflect

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("anna/reflect")

// startCycleSpan starts a span covering one full review cycle across all agents.
func startCycleSpan(ctx context.Context, agentCount int) (context.Context, trace.Span) {
	ctx, span := tracer.Start(ctx, "reflect.cycle",
		trace.WithAttributes(
			attribute.String("anna.reflect.operation", "cycle"),
			attribute.Int("anna.reflect.agent_count", agentCount),
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
func startConversationSpan(ctx context.Context, c candidate) (context.Context, trace.Span) {
	ctx, span := tracer.Start(ctx, "reflect.review_conversation",
		trace.WithAttributes(
			attribute.String("gen_ai.conversation.id", c.session.ID),
			attribute.String("agent_id", c.session.AgentID),
			attribute.Int64("user_id", c.session.UserID),
			attribute.String("anna.chat.channel", c.session.Channel),
		),
	)
	return ctx, span
}

// recordError records an error on a span and sets its status.
func recordError(span trace.Span, err error) {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	span.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", err)))
}
