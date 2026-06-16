---
title: 对象存储后端（设计）
---

> 这是 [#481](https://github.com/vaayne/stella/issues/481) 的设计提案（#477 生产部署的子 issue）。它解释设计**为什么**长这样，不是实现指南。目前尚无代码。

## 要避开的陷阱

Issue 要求"为持久文件加 S3 兼容存储"。照字面理解，像是把本地文件系统换成 S3——这是范畴错误。

Stella 的存储是**沙箱文件系统中心**的。沙箱把 `users/{userID}/agents/{agentID}/`、`projects/{projectID}/`、`.mise-tools/`、`.cache/` mount 成 agent 的工作目录（`/workspace`、`/user`）。agent 通过 `write`/`edit`/`read` 工具、mise 工具链、git working tree **就地读写**这些路径。这要求真正的 POSIX 语义：部分写、rename、mmap、可执行位。

S3 是对象存储：按 key 做 `PUT`/`GET`/`DELETE`，没有部分写、没有 rename、没有 POSIX。**它撑不起一个活的沙箱文件系统。** 把整个 `STELLA_HOME` 放上 S3 不可行。

所以真正的问题比 issue 标题窄得多。

## 多副本真正坏掉的是什么

生产痛点不是"沙箱要共享"。一个会话的沙箱是单次运行的，可以钉在某个节点上。痛点在**持久 blob 层**：replica A 处理的上传，replica B 之后要读，本地磁盘不共享状态。

这一层——也只有这一层——是本设计要迁到对象存储的。

| 该上对象存储（写一次读多次）         | 留在本地 FS（活的 POSIX 状态）   |
| ------------------------------------ | -------------------------------- |
| 渠道/用户上传 —— `data/assets/`      | 沙箱工作树、project working tree |
| 邮件附件 —— `internal/email/imap.go` | `.mise-tools/`、`.cache/`        |
| 知识库文章 —— `internal/recally/`    | skill 磁盘镜像（沙箱就地读取）   |
| 导出的持久 artifact                  | git 仓库                         |

让沙箱层跨副本共享（节点钉死的临时盘 + 会话启动时从 S3 rehydrate，或 RWX 卷）属于 **#477 / Kubernetes** 范畴，明确不在本设计内。

## 抽象

新建包 `internal/objstore`，定义一个后端无关的接口。两个实现：`local`（默认，文件系统）和 `s3`（用 [minio-go](https://github.com/minio/minio-go)，它对 AWS S3、MinIO、Cloudflare R2 一视同仁，依赖树也小）。

```go
type Store interface {
    Put(ctx context.Context, key string, r io.Reader, opts PutOptions) error
    Get(ctx context.Context, key string) (io.ReadCloser, error)
    Delete(ctx context.Context, key string) error
    Stat(ctx context.Context, key string) (ObjectInfo, error)
    // SignedURL 返回一个限时直连 URL。后端无法签名时（local 后端）ok=false，
    // 调用方据此回退到代理流式转发。
    SignedURL(ctx context.Context, key string, ttl time.Duration) (url string, ok bool)
}
```

接口刻意做成深模块：极小的表面（五个方法）藏住所有后端相关细节（分片上传、path-style vs virtual-host 寻址、content-type 协商）。调用方永远不按后端类型分支。

## key 不能泄露名字

对象 key 是寻址方案，不是编码身份的地方。issue 明确要求"避免泄露敏感的用户/项目名"。所以 key 是不透明的：

```
objects/{tenant}/{uuid-或-sha256}
```

人类文件名、owner（用户/agent/项目）、content-type、大小都放在 DB 元数据表 `blob_object`，绝不进 key。逻辑对象通过这张表映射到存储 key，从而把 key 方案和磁盘 FS 布局彻底解耦。内容寻址（`sha256`）可做去重；随机 UUID 更简单——这个选择留给实现阶段。

## 访问控制在 store 之上，不在其内

`objstore.Store` 不做任何鉴权——它只是个搬 blob 的笨工具。权限检查放在 HTTP 服务层：

1. 解析逻辑对象 → 查 `blob_object`，拿到 owner + 存储 key。
2. 用 Stella 现有的权限模型对调用方做鉴权。
3. 返回字节。

第 3 步按后端能力二选一：

- **能签则签，否则代理。** 调 `SignedURL`。若 `ok`，302 重定向到一个短时效的 presigned URL——客户端直接从 S3 拉，大文件省下 stellad 的带宽。若 `!ok`（local 后端），鉴权通过后由 stellad 流式转发字节。

这样不论后端如何，都只有一个统一的下载入口，且绝不暴露未签名、未鉴权的对象 URL。

## 配置

新增 `storage` 配置段，选后端并携带 S3 参数：

- `backend`：`local`（默认）| `s3`
- S3 参数：`endpoint`、`region`、`bucket`、`access_key`、`secret_key`、`path_style`（MinIO/R2 置 true）、`use_ssl`
- 每项都有对应的环境变量覆盖，与现有配置包（`internal/config`）保持一致。

`local` 仍是零配置默认值，简单的自托管部署不受影响。

## 迁移

一个 CLI 命令（`stella storage migrate`）遍历现有的本地 blob 目录（`data/assets/`、`library/`、邮件附件），逐个上传到配置的后端，写入 `blob_object` 元数据行，并在一轮校验后可选删除本地副本。幂等：重跑会跳过元数据中已存在的对象。

## 保留与删除

删除一个逻辑对象会移除其 `blob_object` 行和底层对象。是立即硬删除还是 tombstone 保留一段宽限期，属于策略决定，留给实现阶段；接口两者都支持。

## 不在范围内

- 让沙箱/工作树文件系统跨副本共享（→ #477 / K8s）。
- 把 SQLite 数据库迁到外部存储（→ #477 PostgreSQL 线）。
- skill 文件——已是 DB 为源 + 沙箱就地消费的磁盘镜像，对象存储在这里没有增益。
