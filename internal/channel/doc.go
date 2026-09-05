// Package channel is the host-side ingress orchestration for chat platforms:
// identity resolution from a platform account to a Stella user, slash commands,
// group dispatch, and session coordination. Platform adapters implement the
// public pkg/channel contracts; platform configuration and account evidence stay
// in those adapters, while this package consumes host-injected policy ports.
package channel
