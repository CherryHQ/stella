// Package email is the user-owned mail capability: account configuration held
// in the acting user's vault, plus the list/read/send use cases the HTTP layer
// and the agent tools share. Its public call contract lives in pkg/email;
// trusted identity and tool admission are supplied by internal/plugin/host.
package email
