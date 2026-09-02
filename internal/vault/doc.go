// Package vault stores per-user secrets encrypted at rest and hands them to the
// agent runtime as session environment variables. Vault owns the direct rules
// for its resource; transports and tools pass a trusted Authority.
package vault
