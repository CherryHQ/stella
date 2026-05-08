---
title: 调度器系统
---

## 状态

已实现——`internal/scheduler/` 包,包含 gocron/v2 调度器、SQLite 持久化、管理面板 CRUD 和通过 `stella scheduler` CLI 命令支持的内置技能。

## 概述

Stella 支持定时任务执行,让代理可以设置提醒、运行周期性任务和自动化重复性工作。调度器系统将所有调度委托给 [gocron/v2](https://github.com/go-co-op/gocron),并在其之上添加了持久化、多代理路由和面向代理的工具。

## 架构

```
Agent (via skill + stella scheduler CLI)
    |
    |  add / list / remove
    v
stella scheduler CLI  ---HTTP---> Admin API (/api/scheduler/jobs)
                                    |
                                    v
                              Scheduler Service
                                    |
                        +-----------+-----------+
                        |                       |
               gocron/v2 Scheduler     sched_jobs (SQLite)
                        |
                        v
                  OnJobFunc callback
                        |
                        v
          PoolManager.Chat(ctx, agentID, userID, sessionID, message)
```

### 包: `internal/scheduler/`

顶层包(在 `internal/` 下)。五个文件:

| 文件                                                  | 用途                                     |
| ----------------------------------------------------- | ---------------------------------------- |
| `internal/scheduler/job.go`                           | `Job` 和 `Schedule` 类型                 |
| `internal/scheduler/service.go`                       | `Service`——gocron 包装器,调度,任务 CRUD  |
| `internal/scheduler/heartbeat.go`                     | 心跳轮询——通过 LLM 决策/执行/通知        |
| `internal/scheduler/persistence.go`                   | 数据库持久化(加载/保存/迁移任务)         |
| `internal/resources/skills/system/scheduler/SKILL.md` | 内置技能——记录 `stella scheduler` CLI 命令 |

### 关键类型

**Schedule** 定义任务何时运行。必须恰好设置一个字段:

- `cron`——cron 表达式(例如 `"0 9 * * 1-5"` 表示工作日上午 9 点)
- `every`——Go duration(例如 `"30m"`, `"2h"`, `"24h"`)
- `at`——一次性任务的 RFC3339 时间戳(例如 `"2024-01-15T14:30:00+08:00"`)

**Job** 是持久化的定义:

```go
type Job struct {
    ID          string    // 短 UUID
    Name        string    // 人类可读的名称
    Schedule    Schedule  // cron、间隔或一次性
    Message     string    // 发送给代理的提示
    AgentID     string    // 池中的目标代理
    UserID      string    // 所属用户
    SessionMode string    // "reuse"(默认)或 "new"
    Enabled     bool
    CreatedAt   time.Time
}
```

任务携带 `agent_id` 和 `user_id` 字段,因此调度器可以通过 PoolManager 将每个任务路由到正确的代理池。

### Service 生命周期

1. `scheduler.New(db)` 或 `scheduler.NewFromPath(dbPath)`——创建由 SQLite 支持的调度器
2. `service.SetOnJob(fn)`——设置回调(延迟连接以解决循环依赖)
3. `service.Start(ctx)`——从数据库加载任务,向 gocron 注册所有任务,启动调度器
4. `service.Stop()`——关闭调度器(如果通过 `NewFromPath` 打开,则关闭数据库)

### 持久化

任务存储在共享 SQLite 数据库(`~/.stella/stella.db`)的 `sched_jobs` 表中。每次变更(添加/删除)都是单独的 INSERT/DELETE——没有完整文件重写。

首次启动时,如果存在旧版 `jobs.json` 文件(来自数据库之前的版本),任务会自动迁移到数据库,文件会被删除。

### 一次性任务

使用 `at` 调度的任务在指定时间恰好运行一次,执行后会自动从调度器和数据库中删除。这样可以保持任务列表清洁,不会有过期条目。

行为细节:

- `at` 字段必须是带时区偏移的有效 RFC3339 时间戳
- 创建时会拒绝过去的时间戳
- 如果 Stella 重启且一次性任务的时间戳已过,任务会被静默跳过(不调度),但会保留在数据库中直到手动删除
- 成功执行时,清理操作异步运行以避免阻塞调度器

### 会话模型

每个定时任务的会话行为由其 `session_mode` 控制:

- **`reuse`** (默认)——任务获得稳定的会话 ID `{agentID}:scheduler:{job.ID}`(设置时会添加代理 ID 前缀)。代理在同一任务的多次调度运行之间保留对话记忆。
- **`new`**——每次执行获得唯一的会话 ID `scheduler:{job.ID}:{timestamp}`。代理每次都从无先前上下文的全新状态开始。

## 配置

调度器配置通过管理面板管理。设置存储在数据库的 `settings` 表中。从管理面板 UI 启用或禁用调度器并配置其行为。

调度器仅在以下情况下激活:

- 管理面板设置中启用了调度器
- `runner.type` 是 `go`(Pi runner 不支持自定义工具)

### 管理面板 API

管理面板公开了完整的调度器任务 CRUD API:

| 方法     | 端点                       | 描述             |
| -------- | -------------------------- | ---------------- |
| `GET`    | `/api/scheduler/jobs`      | 列出所有定时任务 |
| `POST`   | `/api/scheduler/jobs`      | 创建新任务       |
| `PUT`    | `/api/scheduler/jobs/{id}` | 更新现有任务     |
| `DELETE` | `/api/scheduler/jobs/{id}` | 删除任务         |

## 代理技能

`scheduler` 内置技能自动加载。它记录代理通过 Bash 调用的 `stella scheduler` CLI 命令。

### `stella scheduler add`——创建任务

标志:

- `--name` (必需)——人类可读的名称
- `--message` (必需)——每次运行时执行的指令
- `--cron`——cron 表达式(使用 `--cron`、`--every` 或 `--at` 之一)
- `--every`——Go duration(使用 `--cron`、`--every` 或 `--at` 之一)
- `--at`——一次性任务的 RFC3339 时间戳(使用 `--cron`、`--every` 或 `--at` 之一)
- `--session-mode`——`reuse`(默认)保留对话历史;`new` 每次执行都重新开始
- `--agent-id`——可选,默认使用默认代理

示例(重复):

```bash
stella scheduler add --name "email-check" --message "Check my email and summarize new messages" --every 30m
```

示例(一次性):

```bash
stella scheduler add --name "weather-reminder" --message "Check Beijing weather and send a summary" --at "2024-01-15T14:40:00+08:00"
```

### `stella scheduler list`——列出所有任务

```bash
stella scheduler list --json   # JSON 输出用于脚本
stella scheduler list          # 人类可读表格
```

### `stella scheduler remove`——删除任务

```bash
stella scheduler remove <job-id>
```

## 心跳

心跳是由调度器服务管理的内置周期性任务。它轮询 `HEARTBEAT.md` 文件并使用 LLM 决定是否需要采取行动,执行指令并通过通知分发器发送结果。

### 工作原理

1. `SetHeartbeat(cfg, chatFn, notifier)` 在调度器服务上配置心跳
2. `StartHeartbeat(ctx, every)` 通过 `ScheduleEvery` 调度轮询循环
3. 每次心跳:
   - 读取心跳文件(如果缺失或为空则跳过)
   - 将内容发送到快速模型进行 `skip`/`run` 决策(不允许工具)
   - 在 `run` 时,将内容发送到主会话执行
   - 通过通知分发器传递结果

### 配置

心跳设置通过管理面板配置。可用参数如下:

- **enabled**——心跳轮询是否激活(默认: false)
- **every**——轮询间隔,作为 Go duration(例如 `10m`)
- **file**——心跳文件路径,相对于工作空间,除非是绝对路径(例如 `HEARTBEAT.md`)

心跳仅在 `stella` 守护进程模式下运行。快速模型用于门控决策以最小化成本。

## 连接

`gateway.go` 和 `commands.go` 中的连接:

1. 创建不带回调的 `scheduler.Service`
2. 通过 `adminSrv.SetSchedulerService(svc)` 将服务注入管理服务器,使 HTTP create/delete 通过活跃调度器
3. 使用 runner 工厂创建 PoolManager
4. 使用通过 PoolManager 路由的回调调用 `service.SetOnJob(...)`,使用任务的 `agent_id` 和 `user_id`
5. 如果启用了心跳,使用聊天函数和通知器调用 `service.SetHeartbeat(...)`
6. 在网关中调用 `service.Start(ctx)`(或用于仅心跳模式的 `StartEphemeral`)
7. 在通道连接后调用 `service.StartHeartbeat(ctx, every)`

`wireSchedulerNotifier` 函数通过 PoolManager 而不是单个池路由任务执行,使用每个任务的 `agent_id` 和 `user_id` 来到达正确的代理。

## 测试

测试位于 `internal/scheduler/scheduler_test.go` 和 `internal/scheduler/heartbeat_test.go`,涵盖:

- 添加、列出、删除生命周期
- 输入验证(空名称、缺少调度、无效 duration、冲突的调度字段、无效/过去的时间戳)
- 删除不存在的任务
- 服务重启后的持久化
- 按调度触发回调
- 一次性任务创建和验证
- 一次性任务恰好触发一次并自动删除
- 重启时跳过带有过去时间戳的一次性任务
- `AddJobWithOwner` 测试(所有权、enabled 标志)
- 会话模式默认、reuse、new 和无效验证
- 心跳: 文件缺失时跳过
- 心跳: 快速模型用于决策
- 心跳: 运行决策执行并通知
- 心跳: 决策使用工具时出错
- 心跳: 通知器错误传播

运行:

```bash
go test -race ./internal/scheduler/
```
