// Package store is stellad's assembly layer: DBStore implements config.Store by
// composing the domain packages over one pgx pool. It lives under cmd/stellad
// because the composition root is its only consumer — it is wiring, not a
// domain.
package store
