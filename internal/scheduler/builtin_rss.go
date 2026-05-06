package scheduler

func init() {
	RegisterBuiltin(BuiltinJob{
		Name: "recally-rss",
		Message: `1. Poll all feeds: run "anna recally feed poll --limit 20 --json" (no feed-id polls every enabled feed). This returns new pending entries per feed.
2. For each pending entry, process sequentially:
   a. Fetch content to a temp file:
      f=/tmp/recally-$(echo -n "<entry.url>" | md5 | cut -c1-8).md
      tap fetch <entry.url> > $f
      If fetch fails, try "tap fetch --lp", then "curl -s https://r.jina.ai/<entry.url>", then "tap fetch -b".
   b. Read the file and generate: Title, Author, Summary (2-4 sentences), Tags (3-7 lowercase), Source Type = rss.
   c. Save the article:
      anna recally save --content-file $f --url "<entry.url>" --title "..." --author "..." --summary "..." --tags "tag1" --tags "tag2" --source-type rss
   d. Mark the entry as saved (positional args, not flags):
      anna recally feed mark <feed-id> <entry-id> --status saved --article-id <article-id>
   e. On failure, mark as error and continue the batch — do not abort:
      anna recally feed mark <feed-id> <entry-id> --status error --error "<reason>"
3. Use the notify tool to send the user a brief summary only if new articles were saved.`,
		Schedule:    Schedule{Every: "1h"},
		SessionMode: SessionNew,
		ExecScope:   ExecScopeAllUsers,
	})
}
