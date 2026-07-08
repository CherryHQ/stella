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

- Config format is backward-compatible; no migration needed
- Refresh the model cache from the Web UI if new models are available
- Builtin skills update automatically with the binary
