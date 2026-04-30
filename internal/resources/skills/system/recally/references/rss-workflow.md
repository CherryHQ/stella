# RSS Entry Processing Workflow

## Process Pending Entries

After `anna recally feed poll --json`, iterate the `pending` array in each feed result:

1. **Fetch to file**: `tap fetch --json <entry.url> > /tmp/recally-article.md`
2. **Generate summary**: Read `/tmp/recally-article.md`, then produce Title, Author, Summary (2-4 sentences), Tags (3-7), Source Type = `rss`
3. **Save**:
   ```bash
   anna recally save --content-file /tmp/recally-article.md --url "<entry.url>" \
       --title "..." --summary "..." --tags "tag1" --tags "tag2" --source-type rss
   ```
4. **Mark saved**:
   ```bash
   anna recally feed mark <entry-id> --status saved --article-id <article-id>
   ```

## Error Handling

If fetch or save fails:

```bash
anna recally feed mark <entry-id> --status error --error "Failed to fetch: timeout"
```

Entries with `error` status and fewer than 3 attempts are retried on the next poll cycle.

## Skip Entries

For duplicates, off-topic, or paywalled entries:

```bash
anna recally feed mark <entry-id> --status skipped
```

## Batch Processing

> **Instruction**: Use `model_fast` for batch RSS summarization to control costs.

Process entries sequentially. On failure, mark as error and continue — do not abort the batch.
