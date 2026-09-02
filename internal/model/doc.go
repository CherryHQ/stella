// Package model groups everything about which LLM runs, what it costs, and
// what it embeds. It holds no code of its own; each concern is a subpackage:
// catalog (the models.dev snapshot and overrides), resolve (provider override
// plus discovery merged over catalog metadata), usage (per-turn token and cost
// accounting), and embedding (embedding providers, indexing, and storage).
package model
