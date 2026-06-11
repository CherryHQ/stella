package scheduler

// RecallyRSSTemplate is the job template for recurring recally RSS polling.
// Users subscribe to this template instead of receiving it as a broadcast.
// Registered via (*Service).RegisterTemplate before Start.
var RecallyRSSTemplate = JobTemplate{
	Key:         "recally-rss",
	Name:        "recally-rss",
	Description: "Poll your Recally feeds every 6 hours and save new articles.",
	Message: `1. Discover new entries for every enabled feed. Load the recally skill, then dispatch by feed kind:
   - rss: run "stella recally feed poll --limit 20 --json" once (no feed-id polls every enabled feed; non-rss feeds are skipped server-side).
   - twitter / website (and other non-rss kinds): run "stella recally feed list --json", and for each feed whose kind is not "rss", follow that kind's workflow in the recally skill (e.g. the Twitter or website workflow) to list items and push them via "stella recally feed entry add" (Go dedups on guid).
2. For each pending entry (across all feeds), follow the save workflow defined in the recally skill (fetch → generate metadata → save with the entry's source type → mark).
3. Notify only when at least one article was saved: count articles saved in step 2. If zero, do NOT call the notify tool — stop here. If one or more, call notify once:
   - For each article (up to 5): Worth-Reading label (emoji + text), title, author, and the "# Summary" section from the structured summary.
   - If more than 5 were saved, list the remaining as title + author only.`,
	DefaultSchedule: Schedule{Every: "6h"},
	SessionMode:     SessionNew,
}

// RecallyRSSBuiltin is kept for compile-time compatibility during the Phase 1
// transition. Phase 2 will delete this symbol and the ExecScopeAllUsers path.
//
// Deprecated: use RecallyRSSTemplate and (*Service).RegisterTemplate instead.
var RecallyRSSBuiltin = BuiltinJob{
	Name: "recally-rss",
	Message: `1. Discover new entries for every enabled feed. Load the recally skill, then dispatch by feed kind:
   - rss: run "stella recally feed poll --limit 20 --json" once (no feed-id polls every enabled feed; non-rss feeds are skipped server-side).
   - twitter / website (and other non-rss kinds): run "stella recally feed list --json", and for each feed whose kind is not "rss", follow that kind's workflow in the recally skill (e.g. the Twitter or website workflow) to list items and push them via "stella recally feed entry add" (Go dedups on guid).
2. For each pending entry (across all feeds), follow the save workflow defined in the recally skill (fetch → generate metadata → save with the entry's source type → mark).
3. Notify only when at least one article was saved: count articles saved in step 2. If zero, do NOT call the notify tool — stop here. If one or more, call notify once:
   - For each article (up to 5): Worth-Reading label (emoji + text), title, author, and the "# Summary" section from the structured summary.
   - If more than 5 were saved, list the remaining as title + author only.`,
	Schedule:    Schedule{Every: "6h"},
	SessionMode: SessionNew,
	ExecScope:   ExecScopeAllUsers,
}
