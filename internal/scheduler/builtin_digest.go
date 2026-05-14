package scheduler

func init() {
	RegisterBuiltin(BuiltinJob{
		Name: "recally-digest",
		Message: `Load the recally skill. Then generate and send a daily reading digest:
1. Run "stella recally digest" to get today's digest data.
2. If saved_yesterday_count is 0 AND worth_revisiting_count is 0, stop — do NOT notify and do NOT save.
3. Format the digest narrative using the recally skill format:
   Reading Digest for [Date]
   📚 Yesterday's saves ([count]): [title] - [summary], ...
   📖 Your library: [total] articles ([unread] unread, [read] read, [starred] ⭐)
   🔔 Worth revisiting: [count] unread articles 3+ days old
   🏷️ Trending tags: tag1 (N), tag2 (N), ...
4. Call notify once with the formatted narrative.
5. Write the narrative to /tmp/stella_digest.txt then run: stella recally digest-save --narrative "$(cat /tmp/stella_digest.txt)"`,
		Schedule:    Schedule{Every: "24h"},
		SessionMode: SessionNew,
		ExecScope:   ExecScopeAllUsers,
	})
}
