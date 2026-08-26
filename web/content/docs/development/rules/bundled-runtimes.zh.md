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

- `os.MkdirTemp` **根本没有 mode 参数**，它永远创建 `0700`。任何要通过 rename
  发布的暂存目录，必须先 chmod。
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
   SHA-256**）、以及负责下载并校验的 sync 函数。产物要写成**固定文件名**，版本
   戳进 gzip header comment，**不要把版本写进文件名**。下载产物落在
   `resources/binaries/binaries/<platform>/`，该目录被 gitignore，只提交
   `PLACEHOLDER`。
2. **`resources/binaries/embed_<os>_<arch>.go`**——把新归档**按精确文件名**加进
   `//go:embed` 行，每个有资产的平台都要加。**这正是"漏跑 generate 就编译不过"
   的机制所在。**
3. **`resources/binaries/embedded.go`**——在 `knownRuntimes()` 里加一条记录：安装
   名、归档名、解包函数。没有版本常量需要同步，`archiveVersion` 从产物里读回来。
4. **模式**——用 `toolDirMode` / `toolExecMode` / `toolDataMode`，不要写字面量。
5. **`plugins/sandbox/docker/Dockerfile`**——把工具加进跨 UID 冒烟测试。以安装者
   身份跑 `<tool> --version` 什么都证明不了，检查必须在无关 UID 下执行。
6. **测试**——`TestExtractedToolsShareOnePermissionContract` 会遍历整棵安装树，
   权限契约自动覆盖新运行时。修复和校验的两个测试写死了 xberg，如果新运行时是
   bundle 形态，需要一并扩展。

第 1 步之后要跑 `mise run generate`（或 `go generate ./resources/binaries/`）。
`mise run setup`、`build`、`test` 都已依赖它。

## Windows 与缺失资产

`syncXberg` 会跳过资产表里没有的平台；`syncMise` 则把同样的情况视为致命错误，
因为 Stella 支持的平台必须有 mise。新工具按哪条规则走，要在注释里写清楚。

Windows 没有 POSIX 模式位，`repairToolPermissions` 和 `VerifyTools` 的模式检查
在那里直接返回。

**运行时缺失是可见的，不是静默的。** `VerifyTools` 不再容忍空的内嵌 FS：精确文件名
embed 意味着「能编译出来的二进制，运行时一定在」。仍然合法的情况是某个平台没有
某个工具的资产——比如 Windows 上的 Xberg。此时 `ToolNames` 不报告它，而「这是否
致命」由消费方决定：

- Library 不注册任何 Xberg parser 路由，并打一条 warning 日志，列出受影响的媒体
  类型和补救命令 `stellad system-bundle install`（`cmd/stellad/commands.go`）。
- 这些类型的上传在 API 边界以 `library.ErrParserUnavailable` 失败，返回
  `503 "this deployment cannot process this file type"`——**刻意不用**那句通用的
  "temporarily unavailable"，后者会诱导一次永远不可能成功的重试。

## 调用内嵌运行时

安装和调用是两件事，`resources/binaries` 只负责前者。**任何解析不可信输入的调用，
都必须经由一个负责加固的包**——对 Xberg 而言是 `internal/xberg`：环境变量白名单、
禁用配置发现、输出上限。

不要手写 `exec.Cmd` 去调内嵌运行时。Vision 就这么干过，结果把守护进程的全部环境
变量（含 provider 凭据）继承给了一个正在解析用户图片的进程。

两个值得记住的细节：

- **守护进程自身的 PATH 不含 `$STELLA_HOME/bin`。** 用 `binaries.ToolPath` 解析，
  不要用 `exec.LookPath`。沙箱里是另一回事：Docker 镜像和
  `pkg/sandbox.HostEnvBuildPath` 都会把 `bin` 放进 PATH。
- **同级动态库是通过 `@loader_path` / `$ORIGIN` 解析的，不是通过工作目录。**
  把 `cmd.Dir` 设成二进制所在目录是为了配置发现，不是为了链接。
