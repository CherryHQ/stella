# Share artifacts

Stella can turn generated files into public, read-only links. Use this when you want to send a report, prototype, image, or document to someone who does not have access to your Stella account.

## What you can share

### Session artifacts

You can share these file types from a session workspace:

- HTML pages (`.html`, `.htm`)
- Markdown documents (`.md`, `.mdx`, `.markdown`)
- Images (`.png`, `.jpg`, `.jpeg`, `.gif`, `.webp`, `.svg`, `.avif`, `.bmp`, `.ico`)
- PDFs (`.pdf`)

### Recally articles

You can also share saved articles from your Recally library.

A share link is a snapshot. If you edit the original file or article later, the public link still shows the version you shared. Create a new link when you want to publish an updated version.

## Share from the Web UI

1. Open a session.
2. Open the workspace panel.
3. Right-click a supported file.
4. Choose **Share**.
5. Pick an expiration: 1 hour, 1 day, 7 days, or never.
6. Click **Create link**.
7. Copy the public URL and send it to the people who should view it.

You can revoke the link from the same dialog after it is created.

## Ask Stella to share a file

When you ask Stella to share a generated file, Stella can use the CLI from inside the agent session:

```bash
stella share artifact assets/report.html
stella share artifact assets/report.html --expires-in 1d
stella share artifact assets/report.html --expires-in never
```

You can also share a Recally article by its ID:

```bash
stella share article <article-id>
```

The command prints the public URL. It uses the current agent and session automatically when it runs inside Stella. If you run it from your own shell, pass `--agent-id` and `--session-id`.

## Public link safety

Anyone with the URL can open the shared artifact until it expires or you revoke it. Treat share links like secrets.

HTML artifacts can run JavaScript so interactive reports and prototypes keep working. Stella opens shared HTML in a sandboxed viewer and serves it with restrictive browser security headers, but you should still avoid sharing secrets or private data in generated artifacts.

Markdown is rendered as a safe document instead of executing embedded HTML or scripts.

## Troubleshooting

### The Share option does not appear

Make sure the file is one of the supported formats listed above. For other files, download them or ask Stella to convert the content into HTML, Markdown, an image, or PDF.

### The link says the artifact is unavailable

The link may have expired or been revoked. Create a new share link from the original file.

### The HTML page behaves differently than the local preview

Shared HTML runs in a sandbox for safety. Some browser features may be blocked. If the page depends on those features, ask Stella to produce a simpler standalone HTML file.
