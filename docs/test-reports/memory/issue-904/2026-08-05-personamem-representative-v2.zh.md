# PersonaMem Representative V2 测评报告

## 评测范围

- 被测系统：Full Stella，提交 `d070430e9b0d04a870faa92ab8cc6cfc4b9a754f`
- 数据集：[PersonaMem v1](https://github.com/bowen-upenn/PersonaMem)，使用 32k 与 128k split
- 模型：`deepseek/deepseek-v4-flash`
- 样本：6 个 context，6 个 persona，47 个有效 endpoint
- 题目：核心 148 题，扩展 80 题，共 228 题
- 记忆路径：生产 agent prompt、profile 注入、knowledge search、LCM search、Fact Reflect
- 关闭项：Skill Reflect、Curator
- QA：每个 endpoint 完成历史摄入和 Fact Reflect 后冻结记忆；每题使用独立、非持久化临时 session
- 评分：官方四选一答案解析；不使用 LLM judge

本次运行使用确定性 selector。`selector_hash` 为
`000f922f3ed2b743856b5536d52ba713eed6dce771f55cb735c6ab2726145e15`，
`selection_sha256` 为
`64a689644cbe06abc4154fab65bdf373bfd8d598767522ceedb500aff4e943f8`。

## 为什么以四项核心指标为主

本次评测的主要目标是验证 Stella 记忆系统重构，而不是单独评估回答模型的通用推理或创意能力。因此，主指标只采用 Recall、Latest、Evolution 和 Revisit：

- Recall 直接检查用户信息能否被保存、检索并用于回答。
- Latest 直接检查新信息能否修正旧信息，以及回答时能否采用最新状态。
- Evolution 检查系统能否保留并重建用户状态随时间变化的过程。
- Revisit 检查跨 session 的历史话题能否被重新找到并连续使用。

这四项并非完全不受回答模型影响，但与记忆的写入、时序维护、检索和跨会话连续性联系更直接，因此以四类 accuracy 的等权 macro average 作为核心结果。

Generalization、Preference-aligned Recommendation 和 Suggest New Ideas 仍然需要记忆，但还显著依赖回答模型本身：

- Generalization 需要把已知偏好类比到未见过的新场景。
- Recommendation 需要在多个都合理的方案中进行个性化排序，并可能依赖模型的常识和推荐倾向。
- Suggest New Ideas 还要求组合多个用户特征、判断新颖性，并与数据集对“好想法”的标注口径一致。

即使向两个模型提供完全相同的记忆状态，这三项也可能因为模型的类比能力、决策偏好、创意能力、prompt 遵循和选项判断差异而出现较大分差。因此，本报告将后三项作为 Full Stella 的端到端扩展诊断，单独披露，但不把它们纳入记忆系统的核心 Macro。它们不是与记忆无关，而是不适合被单独解释为记忆质量。

## 评测方法

本节定义本次 PersonaMem 评测的可复现协议。后续修改 Stella 记忆实现后做对比测试时，除明确列为实验变量的改动外，应保持这里的数据、样本、模型、摄入顺序、QA 和评分方法不变。

### 1. 数据与代表性样本

评测读取 PersonaMem 的 `questions_32k.csv`、`questions_128k.csv`、`shared_contexts_32k.jsonl` 和 `shared_contexts_128k.jsonl`。文件来自官方 [PersonaMem v1 数据集](https://huggingface.co/datasets/bowen-upenn/PersonaMem-v1)，通过 `PERSONAMEM_DATA_ROOT` 指向本地下载目录。数据文件的 SHA-256 固定在 `manifest.json`；文件内容变化时不得从旧 checkpoint 继续运行。由于本机没有完整的 1M context 文件，本次不包含 1M split。

| 文件                         | SHA-256                                                            |
| ---------------------------- | ------------------------------------------------------------------ |
| `questions_32k.csv`          | `cccd34cf53e0bc4d9536c04cff5ca045156d9a4e227e83327112482840bbc93c` |
| `questions_128k.csv`         | `f0e137c3167fadbffbce5be2786105283c3299972c4ac1f158939155fd1578a7` |
| `shared_contexts_32k.jsonl`  | `217247ebfec9e8442fc53570c795ab69f21aad08745f7de78d9beab51b122d4a` |
| `shared_contexts_128k.jsonl` | `733cc009e84a138b386c9e40adea741565db01074f73af4058fd039b42951726` |

抽样单位是完整 `shared_context_id`，不是随机抽散题。确定性 selector 选择 6 个 context：

- 32k 和 128k 各 3 个，分别覆盖低、中、高 effective endpoint 复杂度。
- 6 个 context 使用 6 个不同 persona。
- Recall、Latest、Evolution、Revisit 各不少于 30 题。
- 每个核心类别均覆盖 near、middle、far 三个历史距离桶。
- selector 只读取数据集 metadata，不读取 Stella 输出。
- 满足约束的 1,935 个组合使用固定 seed 和排序后 context ID 计算 SHA-256，选择哈希最小者，避免人工按已有结果挑样本。

选定 context 中的四类核心题全部进入评测，共 148 题。同一批核心 endpoint 上存在的三类扩展题也全部加入，共 80 题。扩展题不新增 context、历史摄入、Reflect 或 endpoint snapshot，因此总计为 6 个 context、47 个有效 endpoint、228 题。

### 2. Context、session 与 endpoint

PersonaMem 的每个 `shared_context_id` 是一条扁平消息数组。评测按以下规则映射到 Stella：

1. 每个 context 创建一个独立 user-agent pair，不与其他 context 共享 profile、knowledge、LCM、session 或 usage。
2. 数组中的每条 `system` 消息表示一个原始会话边界。该 system 后面的 user/assistant 消息直到下一条 system 为一个 Stella 历史 session。
3. system 消息本身不写入 session。第一份 `Current user persona:` 内容作为一次性 `source=manual`、`subject=user` profile singleton 写入；同一 context 的后续 system persona 必须与第一份完全一致，只作为边界使用。
4. 不按 token 数、固定消息数或时间间隔重新切 session。本次 6 个 context 共形成 71 个历史 session。
5. 每道题的 `end_index_in_shared_context` 按 Python exclusive slice 解释，即该题只能看到 `context[:endpoint]`。负索引先按 Python 语义换算；映射到相同位置的 endpoint 共用同一冻结状态。
6. endpoint 按升序执行。第一个 endpoint 摄入 `context[0:e1]`，下一个只追加 `context[e1:e2]`，不得重复摄入旧消息或提前摄入未来消息。若增量仍位于同一 system block，则继续写入原 session；跨过 system 边界时才进入新 session。

32k/128k 是 PersonaMem 提供的数据规模分组，不是本评测设置的 session 或 review window 大小。

### 3. 历史摄入与 Fact Reflect

每个 endpoint 的增量历史按原始 role 和 content 写入生产 memory provider，然后只运行 Fact Reflect：

- Fact candidate generation、evaluation、related discovery、reconciliation 和 fact batch write 均使用 Stella 当前实现。
- Skill Reflect 和 Curator 关闭，避免测试范围混入 skill 生成或生命周期淘汰。
- Reflect 对该 endpoint 涉及的每个历史 session 分别运行。
- 如 bounded ReviewUnit 被截断，则在同一 session 上继续运行，直到 fact watermark 到达最后完整消息；多轮 review 不会创建新 session。
- 只有明确标记为 pre-write-safe 的 provider/protocol 失败可以重试；写入阶段或写后状态不确定时 fail closed。
- 为覆盖 128k 高密度 session，本地 PersonaMem adapter 将单次 Fact Reflect reviewer timeout 设为 4 分钟，并将 pre-write-safe 最大尝试次数设为 6。该调整不修改生产默认值，也不改变候选 schema、评分门槛、候选上限或写入逻辑。

完成 Reflect 后记录 memory version、profile、active knowledge、fact watermark 和 pair digest，形成该 endpoint 的冻结状态。该 endpoint 的所有题回答完之前，不摄入下一个 endpoint。

### 4. 冻结记忆后的逐题 QA

每道题都从所属 endpoint 的同一冻结记忆状态开始，但使用独立临时 QA session：

- 使用 Full Stella 的生产 agent prompt、profile 注入、knowledge search、LCM search 和 memory tool。
- 使用官方 `user_question_or_message`、`all_options` 和答题指令，不改写问题和选项。
- 模型固定为 `deepseek/deepseek-v4-flash`，temperature 为 0，thinking 关闭。
- 每题最多执行 8 次 memory tool 后端读取；超出预算的调用由 host 拒绝，不访问后端。
- 单次 QA timeout 为 90 秒，运行错误最多尝试 3 次。错误选项不重试；只有 timeout、连接错误、空答案等在临时状态完整清理后可安全重试。
- 问题、回答、QA snapshot 以及 QA 导致的 usage 变化均在题后清理。QA session 不进入 Reflect，也不得改变冻结 pair digest。

因此，同一 endpoint 的题目相互独立，并且不会因为前一道题已经询问过某项记忆而获得额外上下文。

### 5. 答案解析与评分

所有题均为 `(a)` 至 `(d)` 四选一，不使用 LLM judge。解析逻辑镜像 PersonaMem 官方 `extract_answer`：

1. 优先读取最后一个 `<final_answer>` 后的内容。
2. 优先提取括号形式 `(a)` 至 `(d)`，否则提取独立字母。
3. 只有唯一提取选项与 gold option 相同才得 1 分。
4. 三次运行尝试均失败的题记为 incomplete，而不是错误答案；所有报告同时披露 `completed/expected`。

主报告分别计算 Recall、Latest、Evolution、Revisit accuracy，并取四项等权 macro average。扩展三项分别报告，不进入核心 Macro。228 题 overall 是固定代表性样本的诊断性 micro accuracy，不称为 PersonaMem 官方全量分数。

### 6. Checkpoint、恢复与产物

`manifest.json` 固定仓库提交、数据 SHA-256、selector、selection SHA、模型、开关和运行参数。`checkpoint.json` 记录每个 context 的 user ID、摄入位置、watermark、endpoint state、已完成题和临时恢复状态；当前适配器还将不含凭据的 provider endpoint SHA-256 与模型 metadata 纳入恢复身份，避免切换路由后继续混用旧答案。

恢复时必须校验上述身份；不一致时 fail closed。成功题逐题原子落盘，安全恢复不得重复已完成题，也不得跳过未完成题。最终产物为：

- `manifest.json`：实验身份和配置。
- `checkpoint.json`：可恢复运行状态。
- `endpoint_states.json`：47 个冻结记忆状态。
- `answers.json`：228 题的预测、gold、评分、尝试次数和 memory tool audit。
- `scores.json`：核心、扩展和综合指标。
- `representative-run.log`：Reflect、QA、恢复和数据库日志。

### 7. 后续对比测试协议

后续评估记忆系统改动时，应使用新的空 Stella home、run directory 和 checkpoint，从零重建 6 个 context，不能复用本次 `representative-v2` 的数据库、facts 或答案。对比至少固定：

- PersonaMem 数据文件及其 SHA-256。
- 6 个 context、228 个 question ID、endpoint 映射和 `selection_sha256`。
- 回答模型、temperature、thinking、QA prompt、tool budget 和评分器。
- system persona seed、session 边界、增量摄入和冻结 QA 语义。
- 除待测改动外的 Reflect 配置和运行开关。

每轮同时比较核心四项、扩展三项、completed/expected、Reflect generated/accepted/write/no-op、最终 active facts、memory tool 调用和运行错误。若修改了模型、prompt、样本、tool budget 或评测 adapter，必须视为新的实验条件单独披露，不能直接把分差归因于记忆系统。

当前可复现命令为：

```bash
# 纯结构、selector、checkpoint 和评分测试，不调用真实模型
PERSONAMEM_DATA_ROOT=/absolute/path/to/personamem-v1 \
mise exec -- go test -tags=personamemeval ./cmd/stellad \
  -run '^TestPersonaMem' -count=1

# 正式运行；相同 run identity 会从安全 checkpoint 恢复
STELLA_HOME="$(pwd)/dist/benchmarks/personamem/h/r-v2" \
PERSONAMEM_DATA_ROOT=/absolute/path/to/personamem-v1 \
PERSONAMEM_PROVIDER_API_KEY=... \
PERSONAMEM_PROVIDER_BASE_URL=... \
PERSONAMEM_MODEL_SNAPSHOT_STATUS=operator-confirmed-latest \
PERSONAMEM_MODE=representative \
mise exec -- go test -tags=personamemeval ./cmd/stellad \
  -run '^TestPersonaMemBenchmark$' -count=1 -v -timeout 0
```

未来做独立对比运行时，需要先分配新的 run revision 和空 home；不得仅删除答案而保留旧记忆状态。

下载的数据、benchmark home、checkpoint、逐题答案、endpoint snapshot 和日志均保留在被忽略的 `dist/benchmarks/personamem/` 下，不进入 Git。仓库只跟踪评测适配器、运行协议和本报告。

## 核心结果

| 类别      | 正确 | 完成 / 预期 | Accuracy |
| --------- | ---: | ----------: | -------: |
| Recall    |   25 |     30 / 30 |   83.33% |
| Latest    |   37 |     50 / 50 |   74.00% |
| Evolution |   26 |     37 / 37 |   70.27% |
| Revisit   |   22 |     31 / 31 |   70.97% |

四类核心指标的等权 macro accuracy 为 **74.64%**。

## 扩展结果

| 类别                              | 正确 | 完成 / 预期 | Accuracy |
| --------------------------------- | ---: | ----------: | -------: |
| Generalization                    |   10 |     20 / 20 |   50.00% |
| Preference-aligned Recommendation |   10 |     18 / 18 |   55.56% |
| Suggest New Ideas                 |    7 |     42 / 42 |   16.67% |

全部 228 题的诊断性 micro accuracy 为 **60.09%**（137 / 228）。该数值混合了核心和扩展题，不能替代核心 PersonaMem macro 指标。

## Context 结果

| Split | Context        | 正确 / 题数 | Accuracy |
| ----- | -------------- | ----------: | -------: |
| 32k   | `f56dc82b8027` |     10 / 17 |   58.82% |
| 32k   | `1621543a17bd` |     18 / 28 |   64.29% |
| 32k   | `7797915581c0` |     15 / 21 |   71.43% |
| 128k  | `bb7f46b01bc9` |     38 / 64 |   59.38% |
| 128k  | `5fa9c2b9e3d3` |     30 / 52 |   57.69% |
| 128k  | `8d42d864b2b0` |     26 / 46 |   56.52% |

## 执行完整性

- 228 个 answer record 对应 228 个唯一 question ID，全部 completed，无 incomplete。
- 47 个有效 endpoint 均有冻结状态。
- 运行结束时 `pending_question` 全部为空，`reset_required` 全部为 false。
- 测试结束前执行残留 QA session 检查，并正常关闭 benchmark PostgreSQL。
- 227 题一次完成；1 题在 provider deadline 后完成清理并于第 2 次尝试成功。
- 最后一次 checkpoint 续跑耗时 803.13 秒。完整评测经历过中断和续跑，因此不把各次运行的墙钟时间相加为稳定性能指标。

## Memory Tool 诊断

- 116 / 228 题至少调用过一次 memory tool。
- 共记录 254 次 memory tool 请求，其中 249 次执行，5 次在达到每题 8 次上限后由 host 拒绝。
- 已执行/请求动作：`search` 144 次、`profile_get` 54 次、`get_message` 49 次、`search_knowledge` 3 次、`profile_history` 2 次、`expand` 1 次、`constraint_list` 1 次。

达到 tool budget 后的拒绝不会访问后端，也不会自动把该题判错；模型仍可用已经获得的证据完成回答。

## Fact Reflect 诊断

按 `session + round + watermark` 去重成功的 review unit 后：

- 成功处理 112 个 review unit、2,498 条 fresh message。
- generator 产生 261 个 fact candidate。
- evaluator 接受 256 个 candidate。
- reconciliation 提交 99 次实际写入，另有 29 次语义 no-op。
- 6 个 context 的初始官方 persona 分别以一条 manual profile seed 写入；99 次 Reflect 写入加上 6 次 seed，对应最终 memory version 总和 105。
- 最终 active records 为 6 个 subject=user profile singleton 和 2 条 subject=world knowledge。
- 最终 profile 长度为 2,891 至 8,714 字符，平均 5,822.5 字符。

这里的“候选数”“写入次数”和“最终 active record 数”不是同一口径。profile 是持续聚合更新的 singleton，不能用最终 6 条 profile row 代表其中包含的语义事实数量。

## 观察与限制

1. 核心四类中 Recall 最好；Latest、Evolution、Revisit 接近但明显更低。后续分析应优先区分错误来自 profile 更新、历史检索还是最终选择。
2. Suggest New Ideas 只有 16.67%，显著低于其他类别。它更多衡量基于 persona 的开放式候选选择能力，不应与事实回忆能力混为一谈。
3. 两条 active knowledge 中，一条合同术语符合 subject=world；另一条“用户正在开发旅行预算应用”从内容看更像 subject=user profile，说明本轮出现至少一次 subject 误分类。
4. 官方 system persona 作为 manual profile seed 注入，因此本结果衡量的是“保留官方 persona 先验后，Stella 如何维护和使用后续历史”，不是从空记忆完全学习 persona。
5. 为覆盖 128k 高密度历史和 provider 抖动，本地适配器仅对 PersonaMem Fact Reflect 使用 4 分钟 reviewer timeout 和最多 6 次 pre-write-safe retry；生产默认值未修改。
6. `deepseek/deepseek-v4-flash` 是 provider alias，路由没有提供可独立验证的不可变 revision ID。因此本报告固定了执行协议和当时可见的模型 metadata，但未来同名 alias 仍可能发生模型漂移，不能把小幅分差全部归因于 Stella。

## 产物

- `manifest.json`：输入、模型、开关和样本身份
- `checkpoint.json`：可恢复执行状态
- `endpoint_states.json`：47 个冻结 endpoint 状态
- `answers.json`：228 题逐题输出、评分与 tool audit
- `scores.json`：核心、扩展和综合分数
- `representative-run.log`：运行日志
