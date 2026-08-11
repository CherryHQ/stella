---
title: 存储与持久化
---

Stella 写入磁盘的所有内容都位于 `$STELLA_HOME` 下（默认为 `~/.stella`，可用 `STELLA_HOME` 覆盖）。在拥有持久磁盘的单机上，你完全无需关心本页内容。但在 Kubernetes、多副本部署，或任何 pod 磁盘是临时性的环境中，你就需要了解：每个目录要么是必须保留的**持久数据**，要么是能自我重建的**派生缓存**，要么是可安全丢弃的**临时数据**。

本页对每个目录进行分类，并说明各自所需的卷与备份处理方式。下文引用的环境变量（`STELLA_DATABASE_URL`、`STELLA_BLOB_S3_*`）参见[部署页面的环境变量表](/docs/start-here/deployment#环境变量)。

## 分类速览

| `$STELLA_HOME` 下的路径                                                                         | 存放内容                                     | 分类     | Kubernetes / 临时磁盘处理方式                               |
| ----------------------------------------------------------------------------------------------- | -------------------------------------------- | -------- | ----------------------------------------------------------- |
| `postgres/`                                                                                     | 内嵌 PostgreSQL 集群——事实来源               | 持久数据 | 持久卷**并**备份。设置 `STELLA_DATABASE_URL` 时不存在。     |
| `users/{id}/data/`                                                                              | 用户 Principal Home：用户数据与上传文件      | 持久数据 | 持久 POSIX 存储；单副本使用本地 `$STELLA_HOME`。            |
| `users/group-{id}/data/`                                                                        | 群组 Principal Home：群组数据与上传文件      | 持久数据 | 同按用户 Principal 数据。                                   |
| `users/{principal}/agents/{id}/`                                                                | 每 Principal 的 Agent Home：工作区与项目文件 | 持久数据 | 持久卷**并**固定到单一副本。不在任何地方镜像。              |
| `library/`                                                                                      | 旧版文章镜像（正被迁移进 PostgreSQL）        | 遗留     | 保留在卷上，直到回填报告缺失为零，之后可归档或删除。        |
| `bundles/{revision}/`                                                                           | 与发行版完全一致的 builtin Skill bundle      | 派生缓存 | 从匹配的二进制重新安装；不要修改。                          |
| `.agents/skills/`                                                                               | 遗留 Skill 清单                              | 迁移门槛 | 自定义根必须先导入或安全删除。                              |
| `.agents/db-skills/`、`agents/{agent-id}/.agents/skills/`                                       | 窄范围的 system 与 system-Agent Skill 根     | 派生缓存 | 由 PostgreSQL 派生、加载时重新 materialize；临时磁盘即可。  |
| `users/{principal}/data/.agents/skills/`、`users/{principal}/agents/{agent-id}/.agents/skills/` | Principal 与 Agent 的可变 Skill 镜像         | 派生缓存 | 由 PostgreSQL 派生、加载时重新 materialize；临时磁盘即可。  |
| `bin/`                                                                                          | 内嵌工具与 `stella` CLI                      | 派生缓存 | 临时磁盘即可。启动时重新解压。                              |
| `.mise-tools/`、`users/{id}/.mise-tools/`                                                       | 沙箱工具链                                   | 派生缓存 | 临时磁盘即可。按需重新安装。                                |
| `pg-runtime/`                                                                                   | 下载并解压的内嵌 PostgreSQL runtime          | 派生缓存 | 临时磁盘即可。用 `stellad postgres download` 重新下载。     |
| `users/{id}/data/.cache/`                                                                       | 每用户工具缓存                               | 派生缓存 | 临时磁盘即可。                                              |
| `cache/sandbox-tmp/`                                                                            | Docker 沙箱临时目录                          | 临时数据 | 临时磁盘即可；启动时会删除遗留目录。                        |
| `runner-scratch/runner-*`                                                                       | 无用户运行使用的可丢弃工作区                 | 临时数据 | 永远不是 Home 权威；仅在 Stella 停止或已 fence 时清理遗留。 |
| `dumps/`                                                                                        | 收到信号时写出的诊断转储                     | 临时数据 | 临时磁盘即可。仅用于诊断。                                  |

## PostgreSQL 是事实来源（持久数据）

PostgreSQL 保存了几乎全部状态：配置、密钥元数据、消息历史与摘要、可变 Skill 记录、Recally 文章及其正文、已获取模型缓存、目标、计划任务以及调度队列。必须与持久的项目 Skill 数据一起保留，二者都无法重建。

Phase 1 还在 PostgreSQL 中记录了类型化 Home 的身份和生命周期元数据：用户与群组 Principal Home、每 Principal 的 Agent Home，以及窄范围的 system 与 system-Agent Skill 根。这些稳定元数据**不能**让 Home 的文件字节恢复。必须将 PostgreSQL 与全部持久 Principal Home、Agent Home 的存储位置一起备份。

- **内嵌集群（默认）：** 数据位于 `$STELLA_HOME/postgres/`。该目录必须置于持久卷上并加以备份（先停止服务，或使用文件系统快照）。`pg-runtime/` 下下载的 runtime 只是程序代码，可重新获取。
- **外部服务器（`STELLA_DATABASE_URL`）：** 数据库完全移出 `$STELLA_HOME`。用 `pg_dump` 对你的数据库进行备份。这是 Kubernetes 的推荐方案——它把最难管理的有状态目录从 pod 上移走。

## 用户资产（持久 POSIX 数据）

用户上传的文件写入 `users/{id}/data/assets/`（群组为 `users/group-{id}/data/assets/`）。这棵可变 live tree 是 Principal Home 的一部分，具有相同的持久化要求。Workspace API、渠道附件写入和 Agent mount 看到的是同一份 POSIX 字节。

`STELLA_BLOB_S3_*` 配置的是 media、artifact 和已发布 Share snapshot 等不可变 BlobStore 内容。它不会让 S3 成为 live workspace API，不会恢复缺失的可变资产，也不允许 Principal Home 使用临时磁盘。即使配置了 S3，也必须备份 POSIX 命名空间。

升级不会复制或删除遗留 asset object。受支持的单副本升级会保留原有持久 `$STELLA_HOME`，其中已经包含旧版本写入的本地文件。如果某部署因 POSIX 副本被独立删除而只剩 object 中的可变资产，请在升级前恢复并验证这些文件；Stella 不会运行通用 workspace migration，也不会在读取未命中时恢复。

## Principal 与 Agent Home（持久数据，单副本）

当前 local store 使用 `users/{id}/data/` 和 `users/group-{id}/data/` 作为用户和群组 Principal Home；`users/{principal}/agents/{id}/` 是每个 Principal 的 Agent Home。Agent Home 保存该 Principal 的可变工作树与项目文件。这些可变字节不会镜像到 PostgreSQL 或 S3。它们属于持久数据，必须使用持久 POSIX 存储；当前产品仅支持一个副本。

这些确定性路径是当前单副本 POSIX 产品的存储布局。PostgreSQL owner row 授权访问，文件系统保留字节。未来副本必须挂载同一个全局共享、强一致 POSIX 命名空间并保留相同确定性布局；S3 不能替代它。

Stella 仅支持一个副本和一个 POSIX `STELLA_HOME`。PostgreSQL 中的用户、群组、Agent 与分配记录仍是身份和授权权威；目录没有 PostgreSQL catalog。确定性根目录为 `users/<user-id>`、`users/group-<group-id>`、其中的 `agents/<agent-id>`，以及全局 `agents/<agent-id>`。文件系统是布局和数据权威。活跃所有者缺少根目录时会创建根及内部 scaffold；符号链接、非目录和不安全 ID 会被拒绝。主机属于受信任边界。

## 破坏性所有者删除

显式破坏性删除按“进程生命周期 fence → 本地所有者 gate → 现有数据库删除事务”的顺序执行。文件字节和 inode 保留；提交后，所有者存在性检查会拒绝新的 workspace view 和 admission。全局 `agents/<agent-id>` 中的任何孤儿条目（文件、目录或符号链接）都会保留该 Agent ID；受信任主机手动删除后才允许复用。未来的多副本、S3 数据权威、generation 和分布式 lease 需要重新设计。

在单个 server 进程内，一个 writer-prioritized admission barrier 防止 runner setup 与破坏性删除竞态。同步 runner 选择与 Home 解析结束后即释放 barrier，不等待 active Turn 完成。这是 single-replica 保证，不是 distributed lease。未来 multi-replica 必须在每个进程的本地 barrier 之外增加 PostgreSQL generation/lease。durable management 变更后的 best-effort runtime refresh 若失败，可能保持 stale，直到后续 reconcile。

Phase 1 不包含 Home 物理清除、重试命令或 Store 迁移/切换。在 provider/filesystem 边界能够正确 fence 物理操作之前，暂时留下孤立字节比提前删除更安全。Stella 运行时不得手动删除 Home 根目录。provider/filesystem 边界之后的专用清除变更将负责在停机或完成 fence 后清理。

## 遗留文章镜像（迁移中）

`library/` 是 Recally 文章正文曾以磁盘文件存储时留下的遗物。正文现已存于 PostgreSQL，如今唯一仍会读取这些文件的是一个启动任务，它将仅存于文件的正文回填进数据库——文章读取只从 PostgreSQL 取数，绝不回退到磁盘。也不再有任何代码在此写入新文件。将该目录保留在卷上，直到某次回填运行记录缺失正文为零；此后这些文件即为惰性遗留数据，可安全归档或删除。

## Skill 与派生缓存

builtin Skill 是位于 `bundles/{revision}/` 的精确发行 bundle。原生 `local` 和 `none` 执行会安装该 bundle；隔离执行从 `/opt/stella/skills/builtin` 读取它。`/opt` 路径是执行坐标，不是第二个内容权威。

Project Skill 是持久 Agent/项目工作树中的普通文件。PostgreSQL 是可变 `system`、`system_agent`、`user` 和 `user_agent` 记录的权威；`.agents/db-skills/`、`agents/{agent-id}/.agents/skills/`、`users/{principal}/data/.agents/skills/` 和 `users/{principal}/agents/{agent-id}/.agents/skills/` 是加载时重新 materialize 的派生镜像。这里的 `{principal}` 是用户 ID 或 `group-{id}`。Phase 1 注册类型化 Home 身份，但不会切换可变 Skill 内容的权威。

升级前，请使用旧的可工作二进制，将遗留顶层 `.agents/skills/` 下的每个自定义 Skill 根导入为全局（`system`）Skill：旧版入口为 **设置 → 技能**，新版入口为 **管理控制台 → 部署资源 → 全局技能**。其他残留路径应先备份、验证后删除。新版本启动会列出每个阻塞路径并停止，不会修改或删除任何内容。当前发行 manifest 所拥有的路径即使内容或模式陈旧也只是惰性数据；其他每个 Skill 根或残留路径都会阻塞启动。

降级前，请重新启用每个已禁用的 Skill，并清除所有悬空的禁用引用。旧版二进制可能忽略 AgentSkillPolicy v1，并在普通 Agent 编辑时覆盖它。混合版本部署中的 Skill 启用状态只是产品偏好设置，不是安全保证或文件系统访问控制。

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

`runner-scratch/` 是由受信任主机拥有的结构命名空间，其中保存无用户运行使用的可丢弃工作区。正常关闭 runner 或构造失败时会尽力清理，但进程崩溃或受信任主机篡改仍可能留下子目录。隔离型 provider 只挂载精确的 `runner-*` 子目录，从不挂载结构父目录。Scratch 不是 Principal Home 或 Agent Home，也永远不是持久权威。操作员只能在 Stella 已停止或相关消费者已 fence 时清理遗留目录。
