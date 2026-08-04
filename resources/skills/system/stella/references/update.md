# Updating stella

## Check current version

```bash
stellad version
```

## Self-update (recommended)

```bash
stellad upgrade
stellad upgrade 0.50.0                             # install a specific release
stellad upgrade --install-dir "$HOME/.local/bin"  # custom install path
```

Downloads a stable release from GitHub for your platform (the latest by default, or the version you pass) and replaces the running `stellad` binary by default. Progress is shown while the archive downloads. If the target directory is not writable, rerun with the required OS permission or use `--install-dir`. If the binary is locked or busy, stop the running Stella process or service first, then retry.

## Other methods

### Go install

```bash
go install github.com/CherryHQ/stella/cmd/stellad@latest
```

### From source

```bash
cd ~/path/to/stella
git pull origin main
mise run setup
mise run build
# Move dist/bin/stellad to your PATH
```

### GitHub releases

Download the latest binary from https://github.com/CherryHQ/stella/releases

Binaries available for: linux/darwin/windows x amd64/arm64.

### Docker

```bash
docker pull ghcr.io/cherryhq/stella:latest
```

Tags: `latest` (stable), `vX.Y.Z` (specific release).

## After updating

- Back up PostgreSQL's Home registry and all durable Principal and Agent Home bytes before upgrading; database migrations run automatically when the new release starts
- Review release notes and resolve any startup-reported blockers before serving traffic
- Refresh the model cache from the Web UI if new models are available
- Builtin skills update with the binary through its immutable release bundle

## Skill upgrade and downgrade checks

Before upgrading, inspect legacy `$STELLA_HOME/.agents/skills`. Using the old working binary, import each custom Skill root through **Settings → Skills** as a managed global (`system`) Skill. Back up, verify, and remove other residual paths. The new binary lists every blocking path and stops without deleting or changing anything. Paths owned by the current release manifest are inert even when their contents or modes are stale; every other Skill root or residual path blocks startup.

Before downgrading to a binary that predates AgentSkillPolicy v1, re-enable every disabled Skill and explicitly clear dangling disablements in the Web UI. Older binaries ignore canonical policy, and ordinary Agent edits can overwrite the reused column. Retained bundle directories are derived and inert after rollback.

An explicit destructive user, group, or Agent delete is the only lifecycle that purges Homes. Routine upgrades and Helm uninstall do not purge them. If a physical purge is retained as `purge_failed`, use `stellad storage retry-purge --help` for retry syntax.
