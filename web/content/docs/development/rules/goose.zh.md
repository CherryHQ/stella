---
title: goose 迁移
description: Stella 手写 Goose PostgreSQL 迁移规则。
---

Stella 使用 [goose](https://github.com/pressly/goose) 管理 PostgreSQL schema。
迁移文件为**手写**，并且是 schema 的**唯一事实来源**；不存在声明式 schema
副本。Atlas 已移除，因为它不能管理搜索栈需要的 pgvector 和 pg_search 对象。

## 核心概念

1. 使用 `-- +goose Up` / `-- +goose Down` 段编写迁移。
2. `stellad` 启动时会自动应用待执行迁移
   （`internal/db/database.go` → `runMigrations`，通过 `goose.Provider`）。
3. sqlc 读取同一组迁移，并应用其中的 `Up` 段构建 catalog。

schema 只存在于 `internal/db/migrations/`；不要维护声明式 schema 镜像。

## 版本过渡与并发

历史迁移截至 `20260804120000` 使用不可变的时间戳版本。
`90000000000000_sequential_versioning.sql` 是一个有文档说明的 no-op 锚点：
它刻意保持 14 位，且在字典序上位于历史 `2` 前缀文件之后，因此 Goose 与
sqlc 使用相同顺序。

此后的每个迁移必须使用下一个连续整数：

```text
20260804120000  # 最后一个时间戳迁移
90000000000000  # no-op 顺序锚点
90000000000001  # 第一个后续迁移
90000000000002  # 下一个迁移
```

只能通过以下命令创建：

```sh
mise run db:migrate:new -- add_shop_order
```

该任务传入 Goose 的 `-s` 标志，从锚点继续编号。**不要**运行 `goose fix`：
它会重命名不可变的历史文件。

并发分支可能会选择同一个下一个版本号。仓库测试会在每个 checkout（包括
merge queue checkout）中拒绝重复或跳号的顺序版本。请 rebase 或从 `main`
更新，然后只把你尚未合并的迁移重命名为测试报告的下一个版本号。绝不能
重命名、编辑或删除已合并到 `main` 的迁移。

## 迁移文件格式

```sql
-- +goose Up
CREATE TABLE shop_order (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    user_id UUID NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE shop_order;
```

- Goose 以 `;` 分割语句。对内部包含分号的语句（如 `CREATE FUNCTION` 或
  `DO $$ ... $$`），使用 `-- +goose StatementBegin` /
  `-- +goose StatementEnd` 包裹。
- 每个迁移在自己的事务中运行，因此 PostgreSQL 会干净地回滚失败的迁移。
- 始终编写 `Down` 段。若迁移确实不可逆，请使用显式 `SELECT 1;` no-op，
  并用注释解释原因。

## 运行时管理的对象

不要将以下对象加入应用迁移：

- **扩展**（`pg_trgm`、`vector`、`pg_search`）由 `ensureExtensions` 在迁移前
  安装，因为它们需要已安装的二进制文件，且 pg_search 还需要
  `shared_preload_libraries`。
- **`river_*` 表**由 River 通过 `rivermigrate` 自己拥有和版本化。

## 工作流

1. 使用 `mise run db:migrate:new -- <name>` 创建下一个版本。
2. 编写 `Up` 和 `Down` SQL。
3. 使用 `mise run db:validate` 验证解析。
4. 更新相关 sqlc 查询并运行 `mise run generate`。
5. 运行 `mise run build && mise run test`；启动流程会把迁移应用到测试数据库。

| 命令                                | 作用                                           |
| ----------------------------------- | ---------------------------------------------- |
| `mise run db:migrate:new -- <name>` | 生成下一个顺序 SQL 迁移                        |
| `mise run db:validate`              | 验证所有迁移文件可解析且格式正确               |
| `mise run db:migrate:up`            | 对 `STELLA_DATABASE_URL` 应用待执行迁移        |
| `mise run db:migrate:status`        | 显示 `STELLA_DATABASE_URL` 的已应用/待应用状态 |

`stellad` 会在启动时应用迁移，因此 `db:migrate:up` 仅用于手动操作外部
PostgreSQL 实例。

## 规则与修复

1. 已合并到 `main` 的迁移不可变：绝不能编辑、重命名或删除；应另写一个迁移。
2. 每个迁移只包含一个逻辑变更。
3. 变更在理念上应是前向的。`Down` 用于本地迭代，不是生产回滚方案。
4. 迁移中绝不能使用 `CREATE EXTENSION`。
5. 启动时不要启用 Goose 的 `AllowOutOfOrder`。`goose -allow-missing` 仅是为
   已发生分歧的开发或运维数据库准备的、先备份再执行的修复方式；绝不是常规
   启动设置。

## 重置开发数据库

baseline 没有真正的 `Down`。如需重置，请删除并重建数据库（或删除嵌入式
PostgreSQL 的 `~/.stella/postgres`），再让 `stellad` 从头应用迁移。

## 与 sqlc 集成

`sqlc.yaml` 将 `schema` 指向 `internal/db/migrations`。sqlc 应用 `Up` 段，
并在 `pkg/db/sqlc/` 生成 Go 类型。迁移及依赖它的查询应一起交付；修改任一
方后运行 `mise run generate`。
