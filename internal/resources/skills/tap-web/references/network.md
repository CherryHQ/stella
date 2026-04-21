# Network Capture & Interception

Capture, inspect, and intercept network requests on the current browser tab via CDP.

- **Network domain**: `wait`, `log`, `body`
- **Fetch domain**: `intercept`, `clear`

## Commands

```bash
tap browser network wait --url-pattern "*/api/*" --body --timeout 30s
tap browser network body <requestId>
tap browser network log --resource-type XHR,Fetch --timeout 30s
tap browser network intercept --block --url-pattern "*.ads.*"
tap browser network clear
```

## Quick workflow

```bash
tap browser open https://example.com --show
tap browser network wait --url-pattern "*/api/*" --body --timeout 30s
```

Or stream requests while interacting with the page:

```bash
tap browser network log --resource-type XHR,Fetch --timeout 15s
```

## Notes

- Always set `--timeout` on `log`, or it will run until interrupted.
- Prefer `network wait --body` over scraping DOM when the site has clean JSON APIs.
- Network interception requires Chrome/CDP; Lightpanda does not support these domains.
- Commands operate on the resolved current tab. Hidden `--session` / `--tab` overrides still exist for advanced use, but they are not part of the common UX.

## Example: capture SPA data

```bash
tap browser open https://x.com/elonmusk --show
tap browser network wait --url-pattern "*/UserTweets*" --body --format json
```

## Example: capture after a form action

```bash
# terminal 1
tap browser network wait --url-pattern "*/api/search*" --body --timeout 30s

# terminal 2
tap browser fill "#search" "golang"
tap browser keypress Enter
```
