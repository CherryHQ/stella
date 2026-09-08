# Updating stella

## Check current version

```bash
stellad version
```

## Self-update (recommended)

```bash
stellad upgrade
stellad upgrade 0.50.0                             # install a specific release
stellad upgrade 0.66.0-rc.1                         # install a specific RC
stellad upgrade --channel stable                    # leave an RC and use latest stable
stellad upgrade --install-dir "$HOME/.local/bin"  # custom install path
```

Downloads a stable release from GitHub for your platform (the latest by default, or the version you pass) and replaces the running `stellad` binary by default. An RC build requires an explicit version or `--channel stable`, so a plain upgrade cannot silently downgrade it to an older stable release. Progress is shown while the archive downloads. If the target directory is not writable, rerun with the required OS permission or use `--install-dir`. If the binary is locked or busy, stop the running Stella process or service first, then retry.

## Other methods

### From source

```bash
cd ~/path/to/stella
git pull origin main
mise run setup
mise run build
# Move dist/bin/stellad to your PATH
```

`go install` is not a supported method. The binary embeds generated API code,
the built Web UI, and the bundled runtimes, none of which are tracked in the
repository, so the module does not compile on its own.

### GitHub releases

Download the latest binary from https://github.com/CherryHQ/stella/releases

Binaries available for: linux/darwin/windows x amd64/arm64.

### Docker

```bash
docker pull ghcr.io/cherryhq/stella:latest
```

Tags: `latest` (stable), `vX.Y.Z` (specific stable release), `vX.Y.Z-rc.N` (release candidate, pin explicitly).

## After updating

- Back up PostgreSQL and all durable workspace bytes before upgrading; database migrations run automatically when the new release starts
- Review release notes and resolve any startup-reported blockers before serving traffic
- Refresh the model cache from the Web UI if new models are available
- Builtin skills update with the binary through its immutable release bundle

## Resource upgrade checks

Before upgrading, back up PostgreSQL and the durable resource roots. The release
migration publishes complete packages and standalone Skills/MCP files into the
four typed scopes, records source and target digests, and stops on a conflict.
It does not overwrite a different file tree, resurrect a missing declaration,
or use old database rows as a runtime fallback.

After the cutover, edit the files directly through the Web UI, API, or an
intended writable root. Changes take effect on the next turn. A copied package
is independent and does not follow later source edits. Delete an MCP declaration
when the resource should disappear; use OAuth Disconnect separately when local
access must be revoked. Disconnect does not guarantee remote provider revocation.
Resource bytes and installation caches remain until Stella can prove the relevant
process and descendants have stopped. The local backend enforces its sandbox
policy but cannot prove detached descendants stopped; the `none` backend
provides no reliable process isolation.

Explicit destructive user, group, and Agent deletion fence execution before removing the database owner. Workspace bytes and inodes remain, but subsequent access fails owner validation. For live owners, the sole `WorkspaceManager` creates missing deterministic roots and rejects non-directories, symlinks, unsafe IDs, and trusted-root replacement. Any filesystem entry at `agents/{id}` reserves that Agent ID. Run restore and root cleanup while Stella is stopped. Routine upgrades and Helm uninstall do not delete workspace bytes. This is a trusted-host, single-replica POSIX contract; multi-replica, Kubernetes, and S3 authority require a future redesign.
