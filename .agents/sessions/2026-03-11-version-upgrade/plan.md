# Plan: Version And Self-Upgrade Commands

## Overview

Add two top-level CLI commands:

- `anna version` prints the current application version
- `anna upgrade` downloads the latest stable GitHub release binary for the current platform and installs it into a local bin directory

### Goals

- Expose the running version without requiring config or network access
- Support self-upgrade from GitHub releases on darwin/linux/windows archive assets
- Default installs to `${HOME}/.local/bin` while allowing override via flag
- Keep upgrade logic isolated from runtime assistant setup

### Success Criteria

- [ ] `anna version` prints a build version string
- [ ] Release builds can inject semantic versions at build time
- [ ] `anna upgrade` detects current platform and installs the matching binary
- [ ] `anna upgrade` reports when already on the latest version
- [ ] Helper logic is covered by focused unit tests
- [ ] README/deployment docs mention the new commands

### Out of Scope

- In-place upgrade of package-manager installs
- Prerelease selection or channels
- Signature verification

## Technical Approach

### Architecture

Add a new top-level command module that owns version reporting and release-upgrade helpers. Use the GitHub Releases API to resolve the latest release, then map the current `GOOS/GOARCH` to the existing GoReleaser asset naming convention.

### Components

- **Build version metadata**: package-level variable with `dev` fallback
- **Version command**: prints version only
- **Upgrade command**: fetches release metadata, selects asset, downloads archive, extracts binary, installs to target directory
- **Upgrade helpers**: version normalization, asset matching, archive extraction, atomic replace

## Implementation Steps

### Phase 1: Version Plumbing

1. Add build version metadata and `version` command wiring (files: `main.go`, `version_cmd.go`, `mise.toml`, `.goreleaser.yaml`)

### Phase 2: Self-Upgrade

1. Implement GitHub release fetch, asset selection, archive extraction, and install flow (files: `version_cmd.go`)
2. Add unit tests for version/asset helpers and upgrade behavior scaffolding (files: `version_cmd_test.go`)

### Phase 3: Documentation

1. Document `version` and `upgrade` usage in CLI/deployment docs (files: `README.md`, `docs/deployment.md`)

## Testing Strategy

### Unit Tests

- Version normalization and comparison
- Asset name matching for archive suffixes
- Archive extraction for tar.gz and zip
- Upgrade no-op when already current

### Edge Cases

- Missing matching asset for current platform
- Missing home directory resolution
- Network/API failures
- Install directory creation and executable mode preservation

## Considerations

### Security

- Only download from the configured GitHub release asset URL
- Reject archives that do not contain the expected binary name

### Risks & Mitigations

| Risk | Likelihood | Impact | Mitigation |
| ---- | ---------- | ------ | ---------- |
| Asset naming mismatch | Medium | High | Match against current GoReleaser naming and cover with tests |
| Replace current binary fails on some platforms | Medium | Medium | Install to target directory explicitly and use temp file + rename |

## Open Questions

- [x] Default install directory uses `${HOME}/.local/bin`
- [x] Latest stable release excludes prereleases

## Implementation Progress

Pending implementation.
