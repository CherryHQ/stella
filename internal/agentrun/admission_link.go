package agentrun

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type admissionLinkKey struct{}

// WithAdmissionLink couples a durable source claim to the next admitted Run.
// link must validate its source owner and expiry and persist the Run ID using
// tx; an error rolls back both writes. It must not commit or perform external
// work. Lease contexts remove the callback so nested Runs cannot reuse it.
func WithAdmissionLink(ctx context.Context, link func(context.Context, pgx.Tx, Guard) error) context.Context {
	return context.WithValue(ctx, admissionLinkKey{}, link)
}

func withoutAdmissionLink(ctx context.Context) context.Context {
	return WithAdmissionLink(ctx, nil)
}
