package scheduler

func init() {
	RegisterBuiltin(BuiltinJob{
		Name: "recally-digest",
		Message: `Generate and send a daily reading digest:
1. Run "stella recally digest --json" to get today's digest data.
2. If saved_yesterday_count is 0 AND worth_revisiting_count is 0, stop — do NOT notify.
3. Otherwise, call notify once with a formatted summary:
   - Stats line: total articles, unread count, saved yesterday, worth revisiting counts.
   - Saved Yesterday (up to 5): article title + source type.
   - Worth Revisiting (up to 5): article title + source type.
   - Top tags (up to 5): tag name + count.`,
		Schedule:    Schedule{Every: "24h"},
		SessionMode: SessionNew,
		ExecScope:   ExecScopeAllUsers,
	})
}
