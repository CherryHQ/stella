package access

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

var groupRecallTracer = otel.Tracer("stella/session")

// startGroupRecallSpan records recall behavior without attaching query text,
// message content, or stable group identifiers to telemetry.
func startGroupRecallSpan(ctx context.Context, operation string, requestedBound int) (context.Context, func(int, bool, error)) {
	ctx, span := groupRecallTracer.Start(ctx, "session.group_recall."+operation)
	span.SetAttributes(
		attribute.String("stella.memory.recall.operation", operation),
		attribute.Int("stella.memory.recall.requested_bound", requestedBound),
	)
	return ctx, func(resultCount int, truncated bool, err error) {
		span.SetAttributes(
			attribute.Int("stella.memory.recall.result_count", resultCount),
			attribute.Bool("stella.memory.recall.no_hit", resultCount == 0 && err == nil),
			attribute.Bool("stella.memory.recall.truncated", truncated),
		)
		if err != nil {
			failureKind := "operational"
			switch {
			case errors.Is(err, ErrNotFound):
				failureKind = "not_found_or_unauthorized"
			case errors.Is(err, ErrForbidden):
				failureKind = "forbidden"
			}
			span.RecordError(errors.New("group recall operation failed"))
			span.SetStatus(codes.Error, "group recall operation failed")
			span.SetAttributes(
				attribute.String("error.type", fmt.Sprintf("%T", err)),
				attribute.String("stella.memory.recall.failure_kind", failureKind),
			)
		}
		span.End()
	}
}
