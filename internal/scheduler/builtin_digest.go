package scheduler

func init() {
	RegisterBuiltin(BuiltinJob{
		Name: "recally-digest",
		Message: `Load the recally skill. Then generate and send a daily reading digest:
1. Run "stella recally digest" to get today's digest data.
2. If saved_yesterday_count is 0 AND worth_revisiting_count is 0, stop — do NOT notify and do NOT save.
3. Check the user's language preference in memory. If no language preference is found, write the digest in English.
4. Write a newsletter-style narrative in the selected language. It should read like a personal curator wrote it — not a bullet list or a status report. Cover:
   - A short opening sentence that sets the tone for the day's reading (1–2 sentences).
   - For each article saved yesterday: weave the titles and summaries into 1–2 flowing sentences that explain why each piece is worth reading, grouping thematically where possible.
   - A brief "worth revisiting" section: mention the articles by title and why they might be relevant again now.
   - Close with the trending tags as a loose sentence: "Today's themes span X, Y, and Z."
   Keep the tone warm, curious, and concise — aim for 150–300 words total. No bullet points, no emoji, no section headers.
5. Call notify once with a short 1-sentence preview of the narrative (the opening sentence).
6. Save the full narrative through a private temporary file: tmpfile="$(mktemp -t stella_digest.XXXXXX)"; chmod 600 "$tmpfile"; write the narrative to "$tmpfile"; run stella recally digest-save --narrative "$(cat "$tmpfile")"; then rm -f "$tmpfile".`,
		Schedule:    Schedule{Every: "24h"},
		SessionMode: SessionNew,
		ExecScope:   ExecScopeAllUsers,
	})
}
