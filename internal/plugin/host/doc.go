// Package host is the process-wide plugin platform. Static Go registrations are
// validated and sealed at startup, after which only the dynamic desired-state
// surface stays open; a plugin reaches a host port only through a declared
// RequiredCapability that was checked against an injected backing service.
//
// It also owns StateStore, the pg-backed durable state a plugin sees through
// its scoped StateStore capability.
package host
