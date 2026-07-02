---
title: Goal 模型
description: 执行架构——一个递归的 Goal 抽象，配合派生式完成、分层验收契约与有界收敛循环，把工作迭代到正确，而非把前向流水线只跑一遍。
---

本页是 Stella 如何调度并收敛工作的权威说明。执行核心是一个递归的 **Goal** 抽象：根
goal 是用户的目标，子 goal 是它的子目标，同一个形状一路向下重复。完成是从每个
goal 的验收契约*派生*出来的，不由 agent 断言；工作通过有界收敛循环迭代，直到被接受。

> **状态说明。** **leaf** 运行时——单个 goal 的收敛循环(attempt、验收 fold、派生完成、
> handoff)——已落地,也是当下承载绝大部分 SDLC 价值的部分。**composite / 递归**路径(分解 →
> 物化 → 子节点执行 → 父节点 rollup)在本页有完整建模,但尚未端到端接通;补齐这一闭环是已跟踪的
> follow-up([#542](https://github.com/CherryHQ/stella/issues/542))。下文的递归相关章节请按
> *目标设计*阅读,而非已落地行为。

## 为什么是递归 Goal，而非流水线

早期的执行核心把一个前向依赖 DAG 跑一遍就算完。那是**流水线**，不是软件开发生命周期，因为生命周
期的定义性特征——_迭代到正确为止_——缺席了。Goal 模型修掉跑一遍的流水线带来的四个结构性
缺陷：

1. **`done` 不该是 agent 设的状态。** 一旦由 worker（或 reviewer 的是/否）断言完成，系统就在信任
   一个断言，而不是从工作自身的验收契约派生完成。Goal 模型从工作意图派生完成——见
   [完成是派生的，从不被断言](#完成是派生的从不被断言)。
2. **验收标准不该是游离的 advisory 元数据。** 没人核对、也没人注入 prompt 的标准只是装饰。验收契约
   让它们变成绑定——见[验收契约](#验收契约)。
3. **`verify` 不该是兄弟节点。** 当 verify 节点与它"验证"的工作只靠一条前向依赖边相连时，前者能在
   后者是坏的情况下通过。验证改为每个 goal 自己的验收评估。
4. **迭代必须是常态路径，不是异常。** 把返工当成失败恢复——一个子项失败把目标翻成 `failed`、再手动
   reopen——把 SDLC 弄反了。返工就是工作收敛的方式；它是循环里的下一轮 attempt。

回报是用一个状态机替掉三个重叠且不一致的状态机（目标、plan、task 各自带着自己的状态集）。调用者
通过一个递归概念就能推断进度。这是深设计。

## 核心抽象：一个递归的 Goal

整个模型就是一个**递归应用的深模块**。

> Stella 调度 **goal**。Agent 通过 **attempt** 产出 **evidence**。系统拿 evidence 对照
> goal 的 **acceptance contract** 评估，迭代直到 accepted / blocked / abandoned，并**只把
> 已验收的 output** 暴露给依赖它的 goal。

一个 **Goal** 拥有自己的意图、分解、验收、尝试与收敛。根 goal 是用户的目标；它的
children 是 goal；再下一层也是——同一个形状一路到底。`kind` 区分**叶子**（worker 执行）与
**复合**（分解为子项）。分解不是 peer object，是复合 goal 的 _内联 plan_（`goal.plan`）。attempt
不是 peer object，是 goal 的 _执行 episode_。review 不是 peer object，是 _验收证据来源_。

### 运行实体

四个运行实体——刻意做小；超过这个就有过度建模风险。

| 实体               | 职责                                                                                                                                                                |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `goal`             | intent + `acceptance_contract` + `convergence_policy` + 可空 `parent_id` + 可空 accepted output。复合 goal 另在 `plan`（jsonb）与 `planned_at` 栅栏内联保存其分解。 |
| `goal_edge`        | 兄弟 goal 间的 accepted-output 依赖（`hard` 阻塞；`soft` 是建议性上下文）。                                                                                         |
| `attempt`          | 一次执行 episode：一个一次性内部 agent session、冻结的输入上下文、提交的 evidence（evidence 折进 attempt，不单独建表）。                                            |
| `acceptance_event` | 确定性 check 结果或判断性 verdict 的 append-only 记录。Goal 的验收状态是这些事件之上的**缓存投影**，不是可变列。                                                    |
| `goal_event`       | 面向人的 append-only 时间线：计划、尝试、验收、生命周期与人工留言事件。这是 UI 叙事；执行 session 只是内部管道。                                                    |

`acceptance_event` 与 `goal_event` 做成 append-only（而非可变的评估表）对审计与投影重建更友好——见
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
验收状态、完成、下游就绪。executor 返回 `Result`；service 拥有持久转换。submit 记录 evidence 并触发
验收——它从不直接设 `done`。

### 验收契约

验收住在 goal 内部，与意图不可分。它是一棵组合策略树：

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

一个叶子 goal 的执行*本身*就是一个有界循环：

```text
依赖满足 → lifecycle: ready → active
loop:
  Attempt[i]，输入 = intent + 上游 accepted output + 近期时间线指引 + Evaluation[i-1].gaps
  agent 在一次性内部 session 干活 → 提交 Evidence → attempt 结束
  验收：在同一个活 sandbox 里跑确定性 checks；若通过且策略需要，请求 verdicts
    ├ 全满足         → accepted；冻结 accepted_output
    ├ 失败, i < max  → rejected(gaps) → 下一轮（gaps 成为输入）   ← 返工
    ├ 失败, i == max → blocked(budget_exhausted) → 人重试或补充时间线指引
    └ 需 verdict      → blocked(needs_verdict) → 人提交 → 继续
```

**返工不是新 task 类型。** 它是带着上一轮评估 gaps 作为输入的 `Attempt[i+1]`。每轮 attempt 的
evidence 都保留，审计链清晰：attempt 1 产出 X，验收以 gaps Y 拒绝，attempt 2 产出 Z，接受。

## 递归白送分解与终审

复合 goal 由一次 attempt 产出一个 **decomposition**，由 review policy 把关（`none` 自动接
受；`human` 等待批准——plan-review 闸，归到 decomposition）。结构性 decomposition 错误会带着
`prior_errors` 回灌到同一个 planning session 里修；耗尽 `planner_repair_max` 后，复合 goal 进入
`blocked(planning_invalid)`，不消耗语义/瞬态规划预算。一旦接受，子 goal 及其边被物化，每个子项跑自己的收敛循环。

当所有必需子项都 accepted，父项跑**它自己的**验收评估。这一步*就是*根级终审 / synthesizer
——不是一个特殊、单独的特性，而是每个叶子都有的同一个验收闸，应用在根上。synthesizer 从递归里
掉出来。

### 一个 goal 还是两个

递归需要一条硬边界，否则会腐化成一棵全是琐碎节点的深树。判据：

> 如果下游能**独立地消费、审查、复用或回滚**这个结果，它就是一个 Goal。否则它只是一个内部
> attempt 阶段。

- "写代码 → 跑测试 → 修 → 再测" → **一个** goal 的收敛循环。
- "产出需在实现前批准的设计文档" → **两个** goal。
- "迁移 DB schema" 与 "更新 UI"，若各自可独立验收、独立阻塞 → **两个** goal。

这正是 `verify`、`design`、`impl` 不再是节点*类型*的原因：验证是每个 goal 的验收评估器，而
design/impl 最多是 category 或内部阶段。

## 载重不变式

几条规则把模型钉在一起。它们刻意做小，且在递归的每一层都成立：

- **plan gate**——绝不跑未分解的工作。`ready → active` 要求"带契约的叶子"或"已接受、已物化 ≥1
  子项的分解"。它是 goal lifecycle 上的一条规则，而不是散在目标、plan、子项计数三处的三次
  检查。
- **依赖 DAG**——`goal_edge` 承载 _accepted-output_ 依赖，不是过程顺序。
- **Handoff**——**只有 accepted output** 才往下游流；在途 attempt 的 evidence 永不泄漏进依赖项的
  输入。
- **executor 边界**——agent 返回结果；service 拥有持久状态。
- **一次性 attempt session** 与 **pending-vs-accepted 内容隔离**——让在途编辑不进入正在运行的 prompt。attempt session 为审计保留，但从用户会话列表隐藏；目标时间线才是 UI 表面。

### 词汇

递归把若干曾经分立的概念折进单一的 Goal 抽象：

| 概念               | 在 Goal 模型里                             |
| ------------------ | ------------------------------------------ |
| 用户目标（根）     | `goal`，`parent_id = NULL`                 |
| 子目标 / 子任务    | 子 `goal`                                  |
| 目标 lifecycle     | `goal` lifecycle 状态（派生）              |
| plan               | `goal.plan`（内联分解）                    |
| plan items         | 提案子 goal                                |
| worker 执行的 task | 叶子 `goal`                                |
| task running       | `attempt` running                          |
| task done          | `goal` accepted（**派生**）                |
| 模型责任失败       | 验收 rejected → 下一轮，或耗尽预算 blocked |
| 环境失败           | `blocked(env_unavailable)`；报告管理员     |
| 契约失败           | `blocked(contract_conflict)`；编辑契约     |
| 基建抖动           | 不扣业务预算重试，直到 flaky 上限          |
| 验收标准           | `acceptance_contract` 项（绑定、机器核对） |
| verify 步骤        | 该 goal 的验收评估                         |
| design / impl role | goal category / 内部阶段                   |
| handoff 摘要       | attempt evidence 摘要                      |
| plan 评审          | decomposition 评审                         |
| completion 评审    | 验收契约里的 judgment 项                   |
| synthesizer        | 父 goal 的验收评估                         |
| 返工 / reopen      | 收敛循环的下一轮 attempt（自动）           |

## 简单性与可扩展性

诚实评估——这个设计是深模块交易，不是免费午餐。

**简单性。** *模型*更简单：一个递归概念替掉三个不一致的状态机，`done` 被定义出局消除了一整类
"agent 是否撒谎"的推理。*机器*更丰富、不更小——派生状态必须事务内重算，验收契约是个 planner
必须写对的小 DSL。这个交易是 APOSD 形状的：付内部复杂度，买更简单的接口、消灭一类错误。诚实的告
诫：简单/direct goal **不会**变免费——它退化成"一个 attempt + 琐碎契约的叶子 goal"，仍然驱
动完整的核心机器。缩小的是*调用者要装的概念数*，不是实现。

**可扩展性。** 模型在概念上可扩：递归处理任意深度且不加新概念，新验收类型是加法（不动 schema），
accepted-output DAG 天然并行。真正的天花板不在模型：

1. **agent 成本与延迟是第一堵墙**，早于任何数据库极限。每次 rejected attempt 是又一整轮 agent
   episode；深度 × 多轮 × 判断评审会放大 token 与墙钟。`max_attempts` 与
   `deterministic_then_judgment` 是载重护栏，不是可选项。
2. **时间线、session 与 evidence 增长**紧随其后：每轮 attempt 会追加时间线事件并创建一次性内部 session，而 evidence（stdout、diff、artifact、rationale）比状态行长得更快。session 为审计保留，但用户列表隐藏内部 task/delegate kind；默认 truncate、外置 artifact、按 hash 寻址。
3. **单写者数据库**是真约束，但*可以设计掉*。不要每个事件全扫树：子项验收只更新父项的增量计数器，
   父项验收是缓存投影，下游就绪由 accepted-output 事件推动。单写者禁止全量 rollup——不禁止模型。
4. **check 结果缓存**是必需的，免得一个 3 轮叶子重构三次。难点是 cache key，必须包含
   `check_id + command + sandbox image + repo-tree hash + env hash + 上游 accepted-output hash`。
   少一项缓存就是正确性炸弹。
5. **分解质量是自治的天花板。** 整个结构押在 agent 产出好的分解与契约上；烂 plan 会传染，干净架构
   救不了它。

## 叶子运行时交付了什么，递归又加了什么

模型落地为一个**leaf-first 的 goal 运行时**，递归叠在其上——同一个概念，只是打开更多深度。
仅叶子运行时本身就承载了大部分 SDLC 价值：

- `goal`（叶子）、`attempt`、`acceptance_event` 与一个 `goal` 投影，给出派生完成、真
  正的验收闸、带可执行 check 的有界收敛循环——不碰任何递归分解。
- `parent_id` 可空、`goal_edge` 表存在；多层分解在同一组实体之上打开一个能力——不改概念。

让运行时保持干净的那条不可妥协的规则：**acceptance、evidence 与投影是一个深模块；它之外的任何东西
都不得设 `done`。**
