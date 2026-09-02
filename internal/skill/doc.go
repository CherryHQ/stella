// Package skill is the managed Skill authority: the durable store of installed
// skills at exact revisions, project and system skills merged read-only from
// the filesystem, search and loading for a turn, and the management tools. Its
// access subpackage decides who may see or change a skill; policy holds the
// per-agent enabled-builtin-skill policy.
package skill
