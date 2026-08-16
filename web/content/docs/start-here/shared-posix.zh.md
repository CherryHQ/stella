---
title: 认证共享 POSIX 存储
---

Stella 默认的 `local` 存储模式支持一个服务器使用一个持久 POSIX `STELLA_HOME`。选择 `shared-posix` 前，必须认证将要部署的**准确**后端版本、拓扑、客户端/节点版本与 mount options。`ReadWriteMany` claim、成功 mount，或本地 bind/Compose volume 都不等于认证通过。

共享模式仍然**不会**开放多个 Stella 副本。Helm chart 会继续强制 `replicaCount: 1`，直到独立的分布式 runtime lifecycle 前置条件及后续多副本 conformance 全部完成。

## 部署契约

合格部署必须向所有 `stellad` 副本和 Session compute 客户端提供一个全局、强一致 namespace，并位于相同逻辑 `STELLA_HOME` 根。所有客户端使用一致的数值 UID/GID 与权限模型。Session compute 只能在 Stella 固定 guest coordinate 看到该次授权的 Principal data、Agent workspace 与只读 Skill view——绝不能看到 namespace 顶层或其他 Principal 的 root。Pod/节点位置不能决定持久字节的位置，也不存在副本本地 fallback。

后端必须保证同目录原子 rename、相对 symlink 与 rooted containment、mode/ownership、跨客户端 advisory lock、原子 append record、并发读写可见性、close-to-open consistency，以及文件和目录 `fsync` 持久性。若写入中断时字节可能已改变，结果就是 **outcome unknown**：调用方只能检查并 reconcile 该次精确操作，不能自动 replay。

JuiceFS Community Edition 是推荐参考实现，不是应用依赖。EFS、NFS、CephFS 等后端只有在准确拓扑通过同一组 gate 后才可使用。

## 1. 运行前声明标准

把同一候选 namespace 独立 mount 到两个真实、非 symlink 客户端路径。创建如下 JSON 输入：

```json
{
  "client_a": "/mnt/client-a/stella",
  "client_b": "/mnt/client-b/stella",
  "metadata": {
    "backend": "juicefs-ce",
    "version": "1.4.1",
    "topology": "两个客户端；共享 metadata 与 object store",
    "clients": 2,
    "nodes": 2,
    "mount_options": ["default_permissions"],
    "namespace_identity": "production-home-v1",
    "identity_mechanism": "后端 volume UUID 加 Stella identity 文件",
    "reference_hardware": "记录节点、CPU、内存、网络、metadata 与 object-store 等级",
    "independent_mounts": true
  },
  "limits": {
    "metadata_p95_ms": 50,
    "small_files_p95_ms": 250,
    "concurrent_p95_ms": 250,
    "stream_mib_per_second": 25,
    "minimum_free_bytes": 10737418240
  },
  "failure_injection": {
    "injected": true,
    "disconnect_observed": true,
    "remounted": true,
    "revalidated": true,
    "error_class": "outcome_unknown",
    "outcome_unknown": true,
    "detail": "记录准确的断连、readiness、remount 与恢复过程"
  }
}
```

必须在执行前按 Stella 生产延迟/容量预算确定限制，不能看完结果再放宽。每个 latency sample 都使用不同的新 tree，而不是复用 warm path。固定 workload 包括持久化 typed-root materialization；通过同步临时文件、原子 revision rename 与父目录 sync 发布 16 文件 Project/Skill；已同步 4 MiB upload 加经过验证的 peer stream；以及八组并发持久化 API writer/sandbox reader。记录包含 p95 latency、stream throughput 与 free-capacity verdict。

failure evidence 是运维 attestation，因为断连/remount 方法取决于拓扑。必须真实执行：在写入期间中断一个客户端，确认错误分类为 outcome-unknown，确认 readiness/admission 关闭，remount 后确认完整 identity、qualification、read/write 与跨客户端 freshness validation 全部完成才恢复。伪造 attestation 会使记录无效。

## 2. 执行并审查认证

```bash
stellad storage qualify --config qualification-input.json --output qualification.json
```

先在本地 POSIX control 上运行同一 harness。control 应通过语义项，但最终必须为 `qualified_shared: false`；symlink alias、分裂 root、只读 mount 或不兼容后端都不能合格。然后在候选后端至少两个独立 mount 上运行。把输入、输出、后端/客户端版本、硬件与 failure-injection 日志一同保存。只有所有 conformance 与 benchmark 项，以及 `qualified_shared`、`overall_pass` 均为 `true` 才可批准。

只把审查通过的记录安装到共享 namespace：

```bash
stellad storage install-qualification --record qualification.json --root /mnt/client-a/stella
sha256sum /mnt/client-a/stella/.stella-shared-posix/qualification.json
```

命令写入固定 namespace identity 与准确记录。把显示的 SHA-256 用作 `STELLA_SHARED_POSIX_QUALIFICATION_SHA256`。后端版本、拓扑、mount options、identity、客户端或限制任一变化，都必须生成新记录与 digest。

安装与 runtime 使用同一套严格记录契约。即使文件 digest 与配置相符，不支持的 schema、未知字段，以及缺失、重复、失败或相互矛盾的 identity/conformance/benchmark/failure/recovery/readiness evidence 仍会被拒绝。digest 只负责 pin 已审查字节，不能替代语义验证。

## 3. 运行独立 freshness witness

在与所有 `stellad` 进程不同的客户端/节点上，通过独立 mount 运行一个受监管 witness：

```bash
stellad storage witness --root /mnt/witness/stella --client-id storage-witness-a --interval 2s
```

witness 在共享 namespace 内原子递增 sequence。把它放在 Stella 旁边或使用 Stella 的同一个 mount，不能证明跨客户端 freshness。witness 丢失按 availability failure 处理：Stella 会刻意变为 not ready，并拒绝新的持久文件系统与 Session-compute admission。

## 4. 启用共享模式

在每个 Stella server 上设置：

```text
STELLA_STORAGE_MODE=shared-posix
STELLA_SHARED_POSIX_IDENTITY=production-home-v1
STELLA_SHARED_POSIX_QUALIFICATION_SHA256=<64 位十六进制字符>
STELLA_SHARED_POSIX_WITNESS_ID=storage-witness-a
STELLA_STORAGE_CHECK_INTERVAL=2s
STELLA_STORAGE_FRESHNESS_TIMEOUT=15s
STELLA_STORAGE_STARTUP_TIMEOUT=20s
```

只有 root object、identity、准确 qualification digest、可写/fsync probe 与两次递增 witness observation 全部通过，启动才继续。之后 monitor 重复完整检查。存储缺失、被替换、断连、只读、stale 或不匹配时，`/readyz` 返回可操作且不带路径的 `503`，并关闭唯一 Home admission gate。新的 Workspace/API filesystem capability 与 Session compute setup 都会失败；Stella 绝不会创建或使用本地 fallback。liveness 仍只检查进程。

`STELLA_STORAGE_STARTUP_TIMEOUT` 是包含 mount probe 阻塞时间在内的整体启动 deadline。POSIX mount syscall 无法通用取消，因此 Stella 最多只允许一个 probe worker。若 syscall 一直卡住，启动会在 deadline 到达时失败（进程退出时释放它）；runtime 中最后一次成功检查过期后 readiness/admission 会关闭，且不会启动重叠 probe worker。迟到的返回不能自行重新开放 admission——仍必须完成完整 validation，并再观察到一次新 witness advance。

瞬时故障后只有完整 revalidation 成功且观察到新 witness advance，readiness 才恢复。mount 被替换或明确 unmount/remount 后必须重启 `stellad`，让 `WorkspaceManager` pin 新验证的 root object；旧进程会一直 fail closed。现有操作不会被静默 replay。请另行监控 free bytes 与 inode；认证中的 capacity check 是一次性 gate，不是容量监控。

Helm 使用 `persistence.sharedPOSIX.enabled=true`、`persistence.accessMode=ReadWriteMany`、三项 evidence value 与 timing value，并保持 `replicaCount=1`。chart 不负责 provision 后端或 witness，并会拒绝将共享模式配到 ephemeral/local-only persistence。
