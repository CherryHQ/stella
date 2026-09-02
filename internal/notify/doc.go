// Package notify routes a notification to the channels that should receive it.
// The Notifier interface is satisfied both by the Dispatcher and by individual
// channels, so callers never need to know the routing layer exists.
package notify
