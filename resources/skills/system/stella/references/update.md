# Updating stella

## Check current version

```bash
stella version
```

## Self-update (recommended)

```bash
stella upgrade
stella upgrade --install-dir "$HOME/.local/bin"  # custom install path
```

Downloads the latest stable release from GitHub for your platform and replaces the running `stella` binary by default. If the target directory is not writable, rerun with the required OS permission or use `--install-dir`. If the binary is locked or busy, stop the running Stella process or service first, then retry.

## Other methods

### Go install

```bash
go install github.com/CherryHQ/stella@latest
```

### From source

```bash
cd ~/path/to/stella
git pull origin main
go build -o stella .
# Move binary to your PATH
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
