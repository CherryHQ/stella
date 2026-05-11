---
title: 安装
---

## Homebrew（macOS 和 Linux）

```bash
brew tap CherryHQ/stella
brew install stella
```

## 二进制文件

从 [GitHub Releases](https://github.com/CherryHQ/stella/releases) 下载适用于 linux、macOS 或 Windows（amd64/arm64）的预编译二进制文件，然后将其放置在 `$PATH` 中。

随时自动更新现有安装：

```bash
stella upgrade
```

## Go

```bash
go install github.com/CherryHQ/stella@latest
```

## 作为后台服务运行

### macOS — Homebrew

```bash
brew services start stella   # 登录时启动，崩溃后自动重启
brew services stop stella
brew services restart stella
```

### macOS — 手动

```bash
stella service install       # 安装 LaunchAgent 并启动
stella service status
stella service logs --follow
stella service stop
stella service start
stella service uninstall
```

日志写入 `~/Library/Logs/stella/stella.log`。

### Linux — systemd 用户模式（无需 root）

服务以当前用户身份运行，登录时自动启动。

```bash
stella service install
stella service status
stella service logs --follow
stella service stop
stella service start
stella service restart
stella service uninstall
```

Unit 文件安装至 `~/.config/systemd/user/stella.service`。

### Linux — systemd 系统模式（需要 root）

以 root 身份运行，开机自动启动。

```bash
sudo stella service install --system
stella service status
stella service logs --follow
sudo stella service uninstall --system
```

Unit 文件安装至 `/etc/systemd/system/stella.service`。
