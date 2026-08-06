package groupingest

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var groupReflectTracer = otel.Tracer("stella/group-reflect")

func startGroupReflectRunSpan(ctx context.Context) (context.Context, trace.Span) {
	return groupReflectTracer.Start(ctx, "group_reflect.run")
}

func startGroupReflectWindowSpan(ctx context.Context, unit GroupReviewUnit) (context.Context, trace.Span) {
	return groupReflectTracer.Start(ctx, "group_reflect.window",
		trace.WithAttributes(
			attribute.String("group_id", unit.GroupID),
			attribute.Int64("stella.group_reflect.watermark", unit.ConsumedThroughSeq),
			attribute.Int("stella.group_reflect.fresh_messages", unit.FreshCount),
			attribute.Int("stella.group_reflect.prior_messages", unit.PriorCount),
			attribute.Int("stella.group_reflect.fresh_tokens", unit.FreshTokens),
			attribute.Int("stella.group_reflect.prior_tokens", unit.PriorTokens),
			attribute.Int("stella.group_reflect.skipped_messages", len(unit.SkippedSeqs)),
		),
	)
}

func recordGroupReflectError(span trace.Span, err error) {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	span.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", err)))
}
