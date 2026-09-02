// Package plugin groups the plugin machinery. It holds no code of its own; the
// concerns are subpackages: manifest (manifest-declared plugins, their mise
// runtimes, overrides, and reconciliation) and host (the capability-scoped
// process-wide plugin platform, including durable plugin state).
package plugin
