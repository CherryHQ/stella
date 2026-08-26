---
title: 内嵌运行时
description: 通过 go:embed 打包进 stellad 并安装到 $STELLA_HOME/bin 的第三方 CLI 规则。
---

**内嵌运行时**指用 `go:embed` 编进 `stellad`、启动时解包到 `$STELLA_HOME/bin` 的
第三方 CLI。目前只有两个：`mise`（引导其余所有工具）和 `xberg`（为 Library 和
Vision 的 OCR 兜底抽取文档文本）。

内嵌是例外，不是默认。新增之前先读下面的「如何选择分发方式」。

## 如何选择分发方式

自上而下，停在第一个满足需求的选项：

| 方式           | 适用场景                                              | 落地位置                         |
| -------------- | ----------------------------------------------------- | -------------------------------- |
| **mise shim**  | 默认。Agent 在沙箱里调用的工具（`gh`、`fd`、`rg` 等） | `$STELLA_HOME/.mise-tools/shims` |
| **插件二进制** | 工具只属于某个插件，不属于平台                        | 由插件自己的 reconciler 安装     |
| **内嵌运行时** | `stellad` 自身要调用它，或它必须早于 mise 存在        | `$STELLA_HOME/bin`               |

内嵌会在每个平台上增加二进制体积，并把工具版本锁死在 Stella 发版上。**只有当守护
进程自身的代码路径依赖它存在时才内嵌**——没有 `xberg`，Library 直接拒收 PDF；没有
`mise`，任何工具都装不了。

## 权限契约

`$STELLA_HOME/bin` 下的一切，必须对**任何把 `bin` 放进 PATH 的 UID** 保持可读，
可执行文件保持可执行，而不只是对安装它的那个 UID。沙箱镜像的 UID 是构建参数，实际
运行时经常换成别的 UID，所以「仅属主可访问」的路径意味着：服务正常启动，第一次真正
使用时才失败。

`resources/binaries/embedded.go` 用 `toolDirMode`、`toolExecMode`、`toolDataMode`
一次性声明这些模式。两条安装路径共用它们，`VerifyTools` 负责校验，
`repairToolPermissions` 在每次启动时重新施加。

有两个系统调用，如果信任它们的默认行为就会静默违反契约：

- `os.MkdirTemp` **永远**创建 `0700`，既忽略 mode 参数也忽略 umask。任何要通过
  rename 发布的暂存目录，必须先 chmod。
- `os.OpenFile` 的 mode 会被 umask 收窄，`umask 077` 下 `0755` 会变成 `0700`。
  解出的文件要显式 chmod。

曾经真的发过一版 `0700` 的 bundle 目录：解包成功，可执行文件自身是 `0755`，但所有
非安装者 UID 的进程都在软链目标目录上撞到 `permission denied`，而同在 `bin` 下的
`mise`（真身文件）却一切正常。

## 两种形态

**单文件。** 上游发布就是一个静态可执行文件，以 `toolExecMode` 直接解进 `bin`。
`mise` 属于这种。

**Bundle 目录。** 上游发布还带运行期依赖——`xberg` 附带 6 个共享库，动态链接器从
可执行文件所在目录解析它们。解到 `bin/<name>-v<version>/`，再建一个相对软链
`bin/<name>` 指过去。

**不要为了「看起来和单文件一样」而把 bundle 摊平进 `bin`。** `bin` 在 PATH 上，
往里塞共享库会让不同工具之间撞名。版本化目录还让升级保持原子：解到临时兄弟目录，
再 rename。

## 新增一个内嵌运行时

1. **`resources/binaries/gen.go`**——加版本常量、按平台的资产表（**每个资产都要有
   SHA-256**）、以及负责下载并校验的 sync 函数。下载产物落在
   `resources/binaries/binaries/<platform>/`，该目录被 gitignore，只提交
   `PLACEHOLDER`。
2. **`resources/binaries/embedded.go`**——声明同一个版本号（两个常量靠人工同步，
   归档文件名是它们之间的契约），然后扩展 `embeddedToolName` 和 `extractTools`
   里的解包分支。
3. **模式**——用 `toolDirMode` / `toolExecMode` / `toolDataMode`，不要写字面量。
4. **`plugins/sandbox/docker/Dockerfile`**——把工具加进跨 UID 冒烟测试。以安装者
   身份跑 `<tool> --version` 什么都证明不了，检查必须在无关 UID 下执行。
5. **测试**——`resources/binaries/embedded_test.go` 会遍历整棵安装树校验契约，新
   运行时自动被覆盖。只有当工具需要版本探测时才另加测试。

第 1 步之后要跑 `mise run generate`（或 `go generate ./resources/binaries/`）。
单跑 `go build ./...` 不会下载任何东西，那样构建出来只内嵌了 `PLACEHOLDER`。

## Windows

`gen.go` 会跳过资产表里没有的平台；Windows 也没有 POSIX 模式位，因此
`repairToolPermissions` 和 `VerifyTools` 的模式检查在那里直接返回。**没有 Windows
资产的内嵌运行时必须显式降级**：当什么都没内嵌时 `VerifyTools` 会刻意跳过校验，
由调用方而不是解包层来决定「运行时缺失」是否致命。
