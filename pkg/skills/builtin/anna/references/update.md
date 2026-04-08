# Updating anna

## Check current version

```bash
anna version
```

## Self-update (recommended)

```bash
anna upgrade
anna upgrade --install-dir "$HOME/.local/bin"  # custom install path
```

Downloads the latest stable release from GitHub for your platform.

## Other methods

### Go install

```bash
go install github.com/vaayne/anna@latest
```

### From source

```bash
cd ~/path/to/anna
git pull origin main
go build -o anna .
# Move binary to your PATH
```

### GitHub releases

Download the latest binary from https://github.com/vaayne/anna/releases

Binaries available for: linux/darwin/windows x amd64/arm64.

### Docker

```bash
docker pull ghcr.io/vaayne/anna:latest
```

Tags: `latest` (stable), `vX.Y.Z` (specific release).

## After updating

- Config format is backward-compatible; no migration needed
- Run `anna models update` to refresh the model cache if new models are available
- Builtin skills update automatically with the binary
