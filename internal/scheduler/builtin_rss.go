package scheduler

func init() {
	RegisterBuiltin(BuiltinJob{
		Name: "recally-rss",
		Message: `1. Poll all feeds: run "stella recally feed poll --limit 20 --json" (no feed-id polls every enabled feed).
2. Load the recally skill. For each pending entry, follow the RSS workflow defined in the recally skill (fetch → generate metadata → save → mark).
3. Notify only when at least one article was saved: count articles saved in step 2. If zero, do NOT call the notify tool — stop here. If one or more, call notify once:
   - For each article (up to 5): Worth-Reading label (emoji + text), title, author, and the "# Summary" section from the structured summary.
   - If more than 5 were saved, list the remaining as title + author only.`,
		Schedule:    Schedule{Every: "1h"},
		SessionMode: SessionNew,
		ExecScope:   ExecScopeAllUsers,
	})
}
