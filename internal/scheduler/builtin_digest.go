package scheduler

func init() {
	RegisterBuiltin(BuiltinJob{
		Name: "recally-digest",
		Message: `Load the recally skill. Then generate and send a daily reading digest:
1. Run "stella recally digest" to get today's digest.
2. If saved_yesterday_count is 0 AND worth_revisiting_count is 0, stop — do NOT notify.
3. Otherwise, call notify once using the digest format defined in the recally skill:
   Reading Digest for [Date]
   📚 Yesterday's saves ([count]): [title] - [summary], ...
   📖 Your library: [total] articles ([unread] unread, [read] read, [starred] ⭐)
   🔔 Worth revisiting: [count] unread articles 3+ days old
   🏷️ Trending tags: tag1 (N), tag2 (N), ...`,
		Schedule:    Schedule{Every: "24h"},
		SessionMode: SessionNew,
		ExecScope:   ExecScopeAllUsers,
	})
}
