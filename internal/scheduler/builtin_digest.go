package scheduler

func init() {
	RegisterBuiltin(BuiltinJob{
		Name: "recally-digest",
		Message: `Load the recally skill. Then generate and send a daily reading digest:
1. Run "stella recally digest" to get today's digest data.
2. If saved_yesterday_count is 0 AND worth_revisiting_count is 0, stop — do NOT notify and do NOT save.
3. Write a newsletter-style narrative. It should read like a personal curator wrote it — not a bullet list or a status report. Cover:
   - A short opening sentence that sets the tone for the day's reading (1–2 sentences).
   - For each article saved yesterday: weave the titles and summaries into 1–2 flowing sentences that explain why each piece is worth reading, grouping thematically where possible.
   - A brief "worth revisiting" section: mention the articles by title and why they might be relevant again now.
   - Close with the trending tags as a loose sentence: "Today's themes span X, Y, and Z."
   Keep the tone warm, curious, and concise — aim for 150–300 words total. No bullet points, no emoji, no section headers.
4. Call notify once with a short 1-sentence preview of the narrative (the opening sentence).
5. Write the full narrative to /tmp/stella_digest.txt then run: stella recally digest-save --narrative "$(cat /tmp/stella_digest.txt)"`,
		Schedule:    Schedule{Every: "24h"},
		SessionMode: SessionNew,
		ExecScope:   ExecScopeAllUsers,
	})
}
