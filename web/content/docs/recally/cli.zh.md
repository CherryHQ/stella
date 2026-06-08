---
title: Recally CLI
---

`stella recally` CLI 是 Recally 命令语法和示例的 source of truth。运行或编写 Recally 脚本前，先阅读对应命令的 help 输出。

## 从这里开始

```bash
stella recally --help
```

## 文章

```bash
stella recally save --help
stella recally list --help
stella recally search --help
stella recally read --help
stella recally update --help
stella recally delete --help
```

脚本里保存文章前，先看 `stella recally save --help`。它会显示当前参数顺序、metadata flags 和示例。

## Feeds

```bash
stella recally feed --help
stella recally feed add --help
stella recally feed list --help
stella recally feed poll --help
stella recally feed remove --help
stella recally feed entry add --help
stella recally feed mark --help
```

写轮询或 feed-entry 处理脚本前，先看对应 feed 命令的 help。

## Digest

```bash
stella recally digest --help
```
