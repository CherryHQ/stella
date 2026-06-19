---
title: Deliverable 模型（SDLC 目标架构）
description: 把 goal/plan/task 从前向流水线演进成迭代式 SDLC 的目标架构提案——一个递归的 Deliverable 抽象，配合派生式完成、分层验收契约与有界收敛循环。
---

> **状态：方向提案，尚未实现。** 本页是执行核心演进的北极星。当前行为见
> [Goal system](./goal-system) 与 [Task system](./task-system)；本页解释那个模型在真实 SDLC
> 下错在哪，以及一次内部设计与一次独立对抗式评审共同收敛出的干净目标。朝它建设，但不要假设其中
> 任何部分已经存在。

## 问题：一个假装成 SDLC 的流水线

今天一个结构化 goal 物化成 `agent_task` 行的前向依赖 DAG，其 item 可被标注为 `design` / `impl` /
`verify`——但顺序来自依赖边、不是 role 标签，校验也只要求每个 `impl` 有一个下游 `verify`（direct
goal 是单个 `direct` item）。然后把这个 DAG **跑一遍**。这是**流水线**，不是软件开发生命周期，因
为生命周期的定义性特征——_迭代到正确为止_——缺席了。有四样东西摆错了位置：

1. **`done` 是 agent 设的状态。** 在常见的 `none`/`auto` 路径上，一次 `task_control submit` 直接把
   task 推到 `done`（`internal/tasks/review.go`）；`agent` 或 `human` review policy 会经一个
   `reviewing` 步骤延后它，但那仍然是审查提交的 output，而不是从工作自身的验收契约派生完成。完成是
   worker 的断言（或 reviewer 的是/否），不是系统从工作意图派生出来的。
2. **验收标准是游离的 advisory 元数据。** `PlanItem.Criteria` 写进完成路径从不读取的行
   （`internal/tasks/plan_service.go`）。没人核对它，也没人把它注入 prompt。
3. **`verify` 是 DAG 里的兄弟节点。** verify task 可以在它"验证"的 `impl` 是坏的情况下通过——
   它俩是独立节点，只靠一条前向依赖边相连。
4. **迭代是异常路径。** 普通的自动执行从不把 verify 的 gaps 喂回 `impl`；一个必需子项失败会把 goal
   翻成 `failed`，恢复需要显式的 lifecycle 干预，例如 `ReopenTask`。返工——SDLC 的核心——被当成失败
   恢复来处理，而不是工作收敛的常态。

后果是三个平行状态机（goal `draft/planning/planned/running/…`、plan
`draft/in_review/accepted/approved`、task `draft/ready/running/…`），状态集重叠却不一致。调用者
必须同时理解三者才能推断进度。这是浅设计。

## 核心抽象：一个递归的 Deliverable

整个模型塌缩成一个**递归应用的深模块**。

> Stella 调度 **deliverable**。Agent 通过 **attempt** 产出 **evidence**。系统拿 evidence 对照
> deliverable 的 **acceptance contract** 评估，迭代直到 accepted / blocked / abandoned，并**只把
> 已验收的 output** 暴露给依赖它的 deliverable。

一个 **Deliverable** 拥有自己的意图、分解、验收、尝试与收敛。goal 是根 deliverable；它的 children
是 deliverable；再下一层也是——同一个形状一路到底。`plan` 不是 peer object，是 deliverable 的
_分解版本_。`task`/`run` 不是 peer object，是 deliverable 的 _attempt_。`review` 不是 peer
object，是 _验收证据来源_。

### 运行实体

四个运行实体加一个可选的版本表——刻意做小；超过这个就有过度建模风险。

| 实体                            | 职责                                                                                                                    |
| ------------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| `deliverable`                   | intent + `acceptance_contract` + `convergence_policy` + 可空 `parent_id` + 可空 accepted output。                       |
| `deliverable_edge`              | 兄弟 deliverable 间的 accepted-output 依赖（`hard` 阻塞；`soft` 是建议性上下文）。                                      |
| `attempt`                       | 一次执行 episode：一个持久 agent session、冻结的输入上下文、提交的 evidence（evidence 折进 attempt，不单独建表）。      |
| `acceptance_event`              | 确定性 check 结果或判断性 verdict 的 append-only 记录。Deliverable 的验收状态是这些事件之上的**缓存投影**，不是可变列。 |
| `deliverable_revision` _(可选)_ | 一个分解版本（即旧的 `plan`）。不是第一天的运行实体；仅在打开多层分解时才需要。                                         |

`acceptance_event` 做成 append-only（而非可变的评估表）对审计与投影重建更友好——见
[简单性与可扩展性](#简单性与可扩展性)。

## 完成是派生的，从不被断言

agent 永远不设 `done`。它提交 **evidence**；系统**派生**验收。

验收有两种，*派生*与*断言*之间的边界是精确的：

- **确定性验收**——命令 exit 0、产物存在、schema 校验通过、测试通过。系统跑 check 并派生结果。
  这里完成真正被*定义出局*："它真做完了吗"无法被答错，因为没满足的验收根本到不了 `accepted`。
- **判断性验收**——由 agent 或人决定。这个 verdict 是*不可约的主观断言*；假装它客观是自欺。但这个
  断言仍然是 **evidence**，不是状态变更：

  ```text
  错：  人点击 "标记完成"
  对：  人提交 verdict: approved(rationale, scope, authority, timestamp)
        系统派生 acceptance_state = accepted → lifecycle = accepted
  ```

所以边界是：**asserted** = verdict 及其 rationale、scope、authority、timestamp；**derived** =
验收状态、完成、下游就绪。代码库已有正确形状——executor 返回 `Result`、service 拥有持久转换
（`internal/tasks/executor.go`、`internal/tasks/worker.go`）。唯一的泄漏是 `Submit` 把 _submit_
当成 _done_。

### 验收契约

验收住在 deliverable 内部，与意图不可分。它是一棵组合策略树：

```json
{
  "policy": "deterministic_then_judgment",
  "items": [
    { "id": "build", "kind": "deterministic", "command": "mise run build", "expect_exit": 0 },
    { "id": "test", "kind": "deterministic", "command": "mise run test", "expect_exit": 0 },
    {
      "id": "review",
      "kind": "judgment",
      "authority": "agent",
      "rubric": "匹配 OpenAPI spec；无裸色 token；错误路径有处理"
    },
    { "id": "signoff", "kind": "judgment", "authority": "human", "prompt": "批准登录 UX 吗？" }
  ]
}
```

- `deterministic` 项由系统在 sandbox 里跑；exit code 派生通过/失败。这正是可被操作化的 advisory
  `criteria` 字符串变成**绑定的、机器核对的断言**之处。
- `judgment` 项路由给 agent reviewer 或人，由其提交 verdict。
- `policy: deterministic_then_judgment` 是成本纪律：先跑便宜的确定性闸，过了才升级到昂贵的判断。
  可与 `all` / `any` 组合。

无法确定性化的自然语言标准，作为 judgment 项的 rubric 保留。模型不假装散文可执行。

## 迭代是收敛循环，不是节点

一个叶子 deliverable 的执行*本身*就是一个有界循环：

```text
依赖满足 → lifecycle: ready → active
loop:
  Attempt[i]，输入 = intent + 上游 accepted output + Evaluation[i-1].gaps
  agent 在 attempt.session 干活 → 提交 Evidence → attempt 结束
  验收：跑确定性 checks；若通过且策略需要，请求 verdicts
    ├ 全满足         → accepted；冻结 accepted_output
    ├ 失败, i < max  → rejected(gaps) → 下一轮（gaps 成为输入）   ← 返工
    ├ 失败, i == max → blocked(budget_exhausted) → 人决定
    └ 需 verdict      → blocked(needs_verdict) → 人提交 → 继续
```

**返工不是新 task 类型。** 它是带着上一轮评估 gaps 作为输入的 `Attempt[i+1]`。每轮 attempt 的
evidence 都保留，审计链清晰：attempt 1 产出 X，验收以 gaps Y 拒绝，attempt 2 产出 Z，接受。

## 递归白送分解与终审

非叶子 deliverable 由一次 attempt 产出一个 **decomposition**（即旧的 plan），由 review policy 把关
（`none` 自动接受；`human` 等待批准——旧的 plan-review FSM，现归到 decomposition）。一旦接受，子
deliverable 及其边被物化，每个子项跑自己的收敛循环。

当所有必需子项都 accepted，父项跑**它自己的**验收评估。这一步*就是* goal 级终审 / synthesizer
——不是一个特殊、单独建的特性，而是每个叶子都有的同一个验收闸，应用在根上。synthesizer 从递归里
掉出来。

### 一个 deliverable 还是两个

递归需要一条硬边界，否则会腐化成一棵全是琐碎节点的深树。判据：

> 如果下游能**独立地消费、审查、复用或回滚**这个结果，它就是一个 Deliverable。否则它只是一个内部
> attempt 阶段。

- "写代码 → 跑测试 → 修 → 再测" → **一个** deliverable 的收敛循环。
- "产出需在实现前批准的设计文档" → **两个** deliverable。
- "迁移 DB schema" 与 "更新 UI"，若各自可独立验收、独立阻塞 → **两个** deliverable。

这正是 `verify`、`design`、`impl` 不再是节点*类型*的原因：验证是每个 deliverable 的验收评估器，而
design/impl 最多是 category 或内部阶段。

## 今天的模型有哪些对、要保留

这是一次重新生根，不是推倒。以下已经正确，其精神原样存活：

- **plan gate**——绝不跑未分解的工作。今天它散在 `ActivateGoal`、plan、子项计数三处；它变成
  deliverable lifecycle 上的一条规则（`ready → active` 要求"带契约的叶子"或"已接受、已物化 ≥1
  子项的分解"）。
- **依赖 DAG**——保留，但边的含义是 _accepted-output_ 依赖，不是过程顺序。
- **Handoff**——下游从上游 output 取上下文是对的；强化它，使**只有 accepted output** 才往下游流。
- **executor 边界**——agent 返回结果；service 拥有持久状态。
- **持久 session** 与 **pending-vs-accepted 内容隔离**——让在途编辑不进入正在运行的 prompt。

### 概念映射

| 今天                            | 目标                                       |
| ------------------------------- | ------------------------------------------ |
| `agent_goal`（根）              | `deliverable`，`parent_id = NULL`          |
| goal 子状态                     | `deliverable` lifecycle 状态（派生）       |
| `agent_goal_plan`               | `deliverable_revision`（分解版本）         |
| plan status / `review_policy`   | decomposition status / review policy       |
| plan 内容 items                 | 提案子 deliverable                         |
| `agent_task`                    | 叶子 `deliverable`                         |
| task `running`                  | `attempt` running                          |
| task `done`（断言）             | `deliverable` accepted（**派生**）         |
| task failed——瞬时               | `attempt` interrupted → 新 attempt         |
| task failed——语义               | 验收 rejected → 下一轮，或耗尽预算 blocked |
| `criteria []string`（advisory） | `acceptance_contract` 项（绑定）           |
| `verify` task（兄弟节点）       | 该 deliverable 的验收评估                  |
| `design` / `impl` role          | deliverable category / 内部阶段            |
| `handoff.summary`               | attempt evidence 摘要                      |
| review `subject=plan`           | decomposition 评审                         |
| review `subject=completion`     | 验收契约里的 judgment 项                   |
| synthesizer（stub）             | 父 deliverable 的验收评估                  |
| `ReopenTask`（手动返工）        | 收敛循环的下一轮 attempt（自动）           |

## 简单性与可扩展性

诚实评估——这个设计是深模块交易，不是免费午餐。

**简单性。** *模型*更简单：一个递归概念替掉三个不一致的状态机，`done` 被定义出局消除了一整类
"agent 是否撒谎"的推理。*机器*更丰富、不更小——派生状态必须事务内重算，验收契约是个 planner
必须写对的小 DSL。这个交易是 APOSD 形状的：付内部复杂度，买更简单的接口、消灭一类错误。诚实的告
诫：简单/direct goal **不会**变免费——它退化成"一个 attempt + 琐碎契约的叶子 deliverable"，仍然驱
动完整的核心机器。缩小的是*调用者要装的概念数*，不是实现。

**可扩展性。** 模型在概念上可扩：递归处理任意深度且不加新概念，新验收类型是加法（不动 schema），
accepted-output DAG 天然并行。真正的天花板不在模型：

1. **agent 成本与延迟是第一堵墙**，早于任何数据库极限。每次 rejected attempt 是又一整轮 agent
   episode；深度 × 多轮 × 判断评审会放大 token 与墙钟。`max_attempts` 与
   `deterministic_then_judgment` 是载重护栏，不是可选项。
2. **session 与 evidence 增长**紧随其后：每轮一个 attempt 会膨胀 session，而 evidence（stdout、
   diff、artifact、rationale）比状态行长得更快。默认 truncate、外置 artifact、按 hash 寻址。
3. **单写者数据库**是真约束，但*可以设计掉*。不要每个事件全扫树：子项验收只更新父项的增量计数器，
   父项验收是缓存投影，下游就绪由 accepted-output 事件推动。单写者禁止全量 rollup——不禁止模型。
4. **check 结果缓存**是必需的，免得一个 3 轮叶子重构三次。难点是 cache key，必须包含
   `check_id + command + sandbox image + repo-tree hash + env hash + 上游 accepted-output hash`。
   少一项缓存就是正确性炸弹。
5. **分解质量是自治的天花板。** 整个结构押在 agent 产出好的分解与契约上；烂 plan 会传染，干净架构
   救不了它。

## 朝它建设而不过度建设

目标是北极星；别一次浇完所有水泥。正确的第一刀是**leaf-first 的 deliverable 运行时**——而且必须
从第一天就叫 `Deliverable`。给 `agent_task` 加 acceptance/check/rework 字段是 throwaway，会继续把
`task` 当核心概念，用新债换旧债。

第一刀，目标的真子集：

- `deliverable`（仅叶子）、`attempt`、`acceptance_event`，以及一个 `deliverable` 投影。
- 保留 `parent_id` 可空、`deliverable_edge` 表存在，但递归先关掉。日后长成多层树只是打开能力，不
  改概念。

这一刀已经交付约 80% 的 SDLC 价值：派生完成、真正的验收闸、带可执行 check 的有界收敛循环——且不碰
递归分解。让它保持干净的那条不可妥协的规则：**acceptance、evidence 与投影是一个深模块；它之外的任
何东西都不得设 `done`。**
