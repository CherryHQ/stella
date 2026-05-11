---
title: Installation
---

## Homebrew (macOS and Linux)

```bash
brew tap CherryHQ/stella
brew install stella
```

## Linux packages (.deb / .rpm)

Pre-built packages are available on the [Releases](https://github.com/CherryHQ/stella/releases) page. `bubblewrap` is declared as a dependency and will be installed automatically.

```bash
# Debian / Ubuntu
sudo apt install ./stella_*_linux_amd64.deb

# Fedora / RHEL
sudo dnf install ./stella_*_linux_amd64.rpm
```

## Binary

Download a pre-built binary from [GitHub Releases](https://github.com/CherryHQ/stella/releases) for linux, macOS, or Windows (amd64/arm64), then place it on your `$PATH`.

Self-update an existing install at any time:

```bash
stella upgrade
```

## Go

```bash
go install github.com/CherryHQ/stella@latest
```

## Run as a background service

### macOS — Homebrew

```bash
brew services start stella   # start on login, restart on crash
brew services stop stella
brew services restart stella
```

### macOS — manual

```bash
stella service install       # install LaunchAgent and start
stella service status
stella service logs --follow
stella service stop
stella service start
stella service uninstall
```

Logs are written to `~/Library/Logs/stella/stella.log`.

### Linux — systemd user mode (no root required)

The service runs as your user and starts on login. `bubblewrap` must be installed first (it is pulled in automatically by the Homebrew and package-manager installs above; for raw binary installs: `apt install bubblewrap` / `dnf install bubblewrap`).

```bash
stella service install
stella service status
stella service logs --follow
stella service stop
stella service start
stella service restart
stella service uninstall
```

The unit file is installed to `~/.config/systemd/user/stella.service`.

### Linux — systemd system mode (root required)

Runs as root, starts on boot.

```bash
sudo stella service install --system
stella service status
stella service logs --follow
sudo stella service uninstall --system
```

The unit file is installed to `/etc/systemd/system/stella.service`.
