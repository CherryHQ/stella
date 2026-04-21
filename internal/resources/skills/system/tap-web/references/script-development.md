# Script Development Guide

Write and contribute site scripts that extract structured data from websites.

## Development workflow

1. Reverse the site's API with `tap browser network`
2. Test the request in `tap browser evaluate`
3. Write the script
4. Save it under `~/.config/tap/sites/{site}/{script}.js`
5. Test with `tap site ...`

## Reverse the API

Open the target site in the default browser context, then capture its API calls.

```bash
tap browser open https://www.example.com --show

# Trigger the page action you want to inspect, then:
tap browser network log --resource-type XHR,Fetch --timeout 15s

# Or wait for one specific request and capture its body
tap browser network wait --url-pattern "*/api/*" --body --timeout 30s
```

Focus on:
- request URL and params
- auth mechanism (cookies / CSRF / bearer token)
- response shape

## Test the request

```bash
tap browser evaluate "fetch('/api/items?q=test',{credentials:'include'}).then(r=>r.json()).then(d=>JSON.stringify(d))"
```

## Browser-auth hints

Do **not** tell users to run `tap login`.

If a site needs auth, use a visible browser flow instead:

```bash
tap attach chrome
tap browser open https://github.com/login --show
```

Then run browser-backed scripts with `tap site -b ...`.

## Error handling guidance

Return plain objects with `error` and optional `hint`:

```javascript
return {error: 'Missing argument: query'};
return {error: 'HTTP 401', hint: 'Open a visible browser and log in first'};
return {error: 'Needs DOM', hint: 'Retrying with browser engine'};
```

For WAF / login walls, prefer hints like:

```javascript
return {error: 'WAF challenge or login required', hint: 'Use tap attach chrome && tap browser open https://example.com/login --show'};
```

## Local testing

```bash
tap site <site/action>
tap site -b <site/action>
tap site info <site/action>
tap --local-only site <site/action>
```

## Contribution note

Scripts are contributed upstream to [bb-sites](https://github.com/epiral/bb-sites).
