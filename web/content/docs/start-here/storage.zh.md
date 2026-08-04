---
title: 存储与持久化
---

Stella 写入磁盘的所有内容都位于 `$STELLA_HOME` 下（默认为 `~/.stella`，可用 `STELLA_HOME` 覆盖）。在拥有持久磁盘的单机上，你完全无需关心本页内容。但在 Kubernetes、多副本部署，或任何 pod 磁盘是临时性的环境中，你就需要了解：每个目录要么是必须保留的**持久数据**，要么是能自我重建的**派生缓存**，要么是可安全丢弃的**临时数据**。

本页对每个目录进行分类，并说明各自所需的卷与备份处理方式。下文引用的环境变量（`STELLA_DATABASE_URL`、`STELLA_BLOB_S3_*`）参见[部署页面的环境变量表](/docs/start-here/deployment#环境变量)。

## 分类速览

| `$STELLA_HOME` 下的路径                                                                         | 存放内容                                     | 分类       | Kubernetes / 临时磁盘处理方式                                   |
| ----------------------------------------------------------------------------------------------- | -------------------------------------------- | ---------- | --------------------------------------------------------------- |
| `postgres/`                                                                                     | 内嵌 PostgreSQL 集群——事实来源               | 持久数据   | 持久卷**并**备份。设置 `STELLA_DATABASE_URL` 时不存在。         |
| `users/{id}/data/`                                                                              | 用户 Principal Home：用户数据与上传文件      | 持久数据\* | 持久卷**并**固定到单一副本；只有资产可被镜像。                  |
| `users/group-{id}/data/`                                                                        | 群组 Principal Home：群组数据与上传文件      | 持久数据\* | 同按用户 Principal 数据。                                       |
| `users/{principal}/agents/{id}/`                                                                | 每 Principal 的 Agent Home：工作区与项目文件 | 持久数据   | 持久卷**并**固定到单一副本。不在任何地方镜像。                  |
| `library/`                                                                                      | 旧版文章镜像（正被迁移进 PostgreSQL）        | 遗留       | 保留在卷上，直到回填报告缺失为零，之后可归档或删除。            |
| `bundles/{revision}/`                                                                           | 与发行版完全一致的 builtin Skill bundle      | 派生缓存   | 从匹配的二进制重新安装；不要修改。                              |
| `.agents/skills/`                                                                               | 遗留 Skill 清单                              | 迁移门槛   | 自定义根必须先导入或安全删除。                                  |
| `.agents/db-skills/`、`agents/{agent-id}/.agents/skills/`                                       | 窄范围的 system 与 system-Agent Skill 根     | 派生缓存   | 由 PostgreSQL 派生、加载时重新 materialize；临时磁盘即可。      |
| `users/{principal}/data/.agents/skills/`、`users/{principal}/agents/{agent-id}/.agents/skills/` | Principal 与 Agent 的可变 Skill 镜像         | 派生缓存   | 由 PostgreSQL 派生、加载时重新 materialize；临时磁盘即可。      |
| `bin/`                                                                                          | 内嵌工具与 `stella` CLI                      | 派生缓存   | 临时磁盘即可。启动时重新解压。                                  |
| `.mise-tools/`、`users/{id}/.mise-tools/`                                                       | 沙箱工具链                                   | 派生缓存   | 临时磁盘即可。按需重新安装。                                    |
| `pg-runtime/`                                                                                   | 下载并解压的内嵌 PostgreSQL runtime          | 派生缓存   | 临时磁盘即可。用 `stellad postgres download` 重新下载。 |
| `users/{id}/data/.cache/`                                                                       | 每用户工具缓存                               | 派生缓存   | 临时磁盘即可。                                                  |
| `cache/sandbox-tmp/`                                                                            | Docker 沙箱临时目录                          | 临时数据   | 临时磁盘即可；启动时会删除遗留目录。                            |
| `dumps/`                                                                                        | 收到信号时写出的诊断转储                     | 临时数据   | 临时磁盘即可。仅用于诊断。                                      |

\* Principal 数据和 Agent Home 仍是持久数据。Principal Home 内的上传资产一旦配置 S3 镜像即可成为可恢复缓存——参见[用户资产](#用户资产持久或镜像)。

## PostgreSQL 是事实来源（持久数据）

PostgreSQL 保存了几乎全部状态：配置、密钥元数据、消息历史与摘要、可变 Skill 记录、Recally 文章及其正文、已获取模型缓存、目标、计划任务以及调度队列。必须与持久的项目 Skill 数据一起保留，二者都无法重建。

Phase 1 还在 PostgreSQL 中记录了类型化 Home 的身份和生命周期元数据：用户与群组 Principal Home、每 Principal 的 Agent Home，以及窄范围的 system 与 system-Agent Skill 根。这些稳定元数据**不能**让 Home 的文件字节恢复。必须将 PostgreSQL 与全部持久 Principal Home、Agent Home 的存储位置一起备份。

- **内嵌集群（默认）：** 数据位于 `$STELLA_HOME/postgres/`。该目录必须置于持久卷上并加以备份（先停止服务，或使用文件系统快照）。`pg-runtime/` 下下载的 runtime 只是程序代码，可重新获取。
- **外部服务器（`STELLA_DATABASE_URL`）：** 数据库完全移出 `$STELLA_HOME`。用 `pg_dump` 对你的数据库进行备份。这是 Kubernetes 的推荐方案——它把最难管理的有状态目录从 pod 上移走。

## 用户资产（持久或镜像）

用户上传的文件写入 `users/{id}/data/assets/`（群组为 `users/group-{id}/data/assets/`）。如何处理这棵目录树取决于是否配置了 S3 镜像：

- **未配置 S3**（`STELLA_BLOB_S3_*` 未设置）：本地副本是唯一副本。该目录树属于持久数据，需要持久卷；磁盘丢失即文件丢失。
- **已配置 S3**（四个 `STELLA_BLOB_S3_*` 变量全部设置）：每次写入都会镜像到存储桶；本地未命中的读取会从存储桶恢复文件；冷启动的 pod 会在会话初始化时从存储桶重新水合其资产。本地目录树因而变为可恢复的缓存，pod 可运行于临时磁盘，需备份的是存储桶。

配置镜像正是让资产服务副本能够无状态运行的关键。四个必需的 S3 变量必须一起设置——部分配置会导致启动失败。启动只将是否配置可变资产对象权威记录为迁移元数据；Phase 1 不会改变镜像/水合行为或权威。

## Principal 与 Agent Home（持久数据，单副本）

当前 local store 保留兼容路径：`users/{id}/data/` 和 `users/group-{id}/data/` 分别是用户和群组 Principal Home；`users/{principal}/agents/{id}/` 是每个 Principal 的 Agent Home。Agent Home 保存该 Principal 的可变工作树与项目文件。Principal 数据或 Agent Home 的文件字节不会镜像到 PostgreSQL，S3 也只按上文所述镜像资产。它们属于持久数据：使用持久存储并将工作负载固定到一个副本。带检查点的多副本执行属于未来工作——现在不要假定它存在。

这些路径是当前 local 的兼容坐标，不是 Home 身份。Home 具有稳定的 registry 元数据，包括 Store ID 和不透明 locator，因此未来的存储实现无需保留这些路径形状。

## 破坏性所有者删除

显式破坏性删除用户、群组或 Agent 时，其 Home 会立刻被 tombstone，并 fence 本地缓存的执行；随后共享 worker 会异步且幂等地清除物理字节。这是唯一会删除 Home 的生命周期：移除 Agent 分配、移除群成员、归档 Session 和卸载 Helm 都**不会**删除 Home。

物理清除失败时，Home 会以 `purge_failed` 状态连同审计记录保留，不会被静默丢弃；操作员必须重试。命令语法请运行 `stellad storage retry-purge --help`。

## 遗留文章镜像（迁移中）

`library/` 是 Recally 文章正文曾以磁盘文件存储时留下的遗物。正文现已存于 PostgreSQL，如今唯一仍会读取这些文件的是一个启动任务，它将仅存于文件的正文回填进数据库——文章读取只从 PostgreSQL 取数，绝不回退到磁盘。也不再有任何代码在此写入新文件。将该目录保留在卷上，直到某次回填运行记录缺失正文为零；此后这些文件即为惰性遗留数据，可安全归档或删除。

## Skill 与派生缓存

builtin Skill 是位于 `bundles/{revision}/` 的精确发行 bundle。原生 `local` 和 `none` 执行会安装该 bundle；隔离执行从 `/opt/stella/skills/builtin` 读取它。`/opt` 路径是执行坐标，不是第二个内容权威。

Project Skill 是持久 Agent/项目工作树中的普通文件。PostgreSQL 是可变 `system`、`system_agent`、`user` 和 `user_agent` 记录的权威；`.agents/db-skills/`、`agents/{agent-id}/.agents/skills/`、`users/{principal}/data/.agents/skills/` 和 `users/{principal}/agents/{agent-id}/.agents/skills/` 是加载时重新 materialize 的派生镜像。这里的 `{principal}` 是用户 ID 或 `group-{id}`。Phase 1 注册类型化 Home 身份，但不会切换可变 Skill 内容的权威。

升级前，请使用旧的可工作二进制，在 **设置 → 技能** 中将遗留顶层 `.agents/skills/` 下的每个自定义 Skill 根导入为全局（`system`）Skill。其他残留路径应先备份、验证后删除。新版本启动会列出每个阻塞路径并停止，不会修改或删除任何内容。当前发行 manifest 所拥有的路径即使内容或模式陈旧也只是惰性数据；其他每个 Skill 根或残留路径都会阻塞启动。

这些目录会自动重建，可置于临时磁盘上：

- **builtin bundle**（`bundles/{revision}/`）：从运行中二进制的不可变发行 bundle 安装。
- **PostgreSQL 派生的 Skill 镜像**（`.agents/db-skills/`、`agents/{agent-id}/.agents/skills/`、`users/{principal}/data/.agents/skills/` 和 `users/{principal}/agents/{agent-id}/.agents/skills/`）：加载时重新 materialize。
- **`bin/`**：内嵌工具与 `stella` CLI，启动时重新解压。
- **工具链**（`.mise-tools/`、每用户 `.mise-tools/`）：按需重新安装。
- **`pg-runtime/`**：下载的内嵌 PostgreSQL runtime；用 `stellad postgres download` 重新下载。每个 runtime 版本安装在各自的目录中，旧版本不会被自动清理，每个约数百 MB。执行 `stellad postgres prune` 查看哪些已不再使用，加 `--force` 才会真正删除。
- **`users/{id}/data/.cache/`**：每用户工具缓存。

## 临时数据

`dumps/` 保存进程收到调试信号时写出的诊断转储。Stella 从不回读它，可安全丢弃。

`cache/sandbox-tmp/` 为 Docker 沙箱会话提供后端目录，属于临时空间。若存在遗留的 `stella.db` 文件，它仅被一次性的 SQLite 到 PostgreSQL 迁移工具读取，运行中的服务不会触碰它。
