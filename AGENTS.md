# anna agent instructions

## Local test policy

- Do not run Go tests with `-race` locally by default; it is too slow for normal development.
- Use `mise run test` and `mise run test:coverage` for local verification.
- Reserve race-enabled test runs for CI only.
- When CI needs race detection, use the dedicated race-enabled mise tasks rather than adding `-race` back to the default local tasks.
