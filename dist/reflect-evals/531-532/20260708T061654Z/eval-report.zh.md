# Reflect 531 Related + Reconciliation 本地评估报告

评估时间：2026-07-08
主结果目录：`dist/reflect-evals/531-532/20260708T061654Z`
数据集目录：`dist/reflect-evals/datasets/reflect-531-focused`

## 1. 目标

本轮只评估 #531 范围：输入已经通过 #532 gate 的 accepted candidates，测试 related discovery、related bundle 组装、reconciliation plan、host 执行模拟，以及结果质量。

不覆盖：

- #532 candidate generation/evaluator 的召回质量。
- 真实生产调度、真实 DB 写入、真实 SkillStore 文件系统写入。
- #535 usage/lifecycle 维护。

## 2. 方法

测试链路：

1. 读取本地 JSONL case。
2. 将 accepted fact/skill candidates 与 active reflect-owned catalog 放入 related discovery。
3. 将 related selections 组装成 fact/skill related bundle。
4. 调用 reconciliation，生成 fact write plan 或 skill write plan。
5. 通过 host validator 与 fake memory/skill store 执行。
6. 跑 deterministic checks：related include/exclude、relation kind、operation、target、content include/exclude。
7. 对 scale/stress case 额外跑 LLM judge，评分维度为 action_fit、related_usefulness、write_accuracy、content_completeness、no_unrelated_content。

Judge 阈值：每项 >= 3 且 verdict=pass。

## 3. 用例规模

最终全量 run 共 33 个 case：

| 分组 | case 数 | 覆盖点 |
|---|---:|---|
| `531_fact_focused` | 3 | knowledge replace/noop/create |
| `531_fact_operation_focused` | 7 | profile/soul singleton create/replace/noop，knowledge deprecate/replace_many |
| `531_related_discovery_focused` | 12 | fact/skill relation kind 与噪声排除 |
| `531_skill_focused` | 3 | skill patch/create/noop |
| `531_skill_relation_focused` | 1 | stale predecessor |
| `531_mixed_focused` | 1 | fact + skill 同轮 |
| `531_scale_focused` | 2 | 20 facts、15 skills 级别 catalog，多候选 |
| `531_stress_focused` | 4 | 更大 catalog、多候选、多操作、混合线 |

新增 stress case：

| case | prior facts | prior skills | candidates | 重点 |
|---|---:|---:|---:|---|
| `stress_fact_large_catalog_multi_relation` | 33 | 0 | 3 fact | 多 endpoint/header/audit 事实同时 replace_many，排除 provider/WSL/PR 噪声 |
| `stress_fact_retired_dependency_and_manual_exclusion` | 28 | 0 | 2 fact | retired fact + dependency fact，排除 manual/inactive/distractor |
| `stress_skill_large_catalog_mixed_patch_create` | 0 | 20 | 3 skill | WSL patch、provider patch/create/noop 边界、browser create |
| `stress_mixed_large_catalog_dual_line` | 14 | 7 | 1 fact + 1 skill | fact/skill 双线同轮，OpenAPI + web fixture + browser smoke |

## 4. 最终主 run 结果

命令：

```bash
env OPENAI_BASE_URL=... OPENAI_API_KEY=... \
  STELLA_REFLECT_EVAL=1 \
  STELLA_REFLECT_EVAL_PROVIDER=openai \
  STELLA_REFLECT_EVAL_MODEL=deepseek/deepseek-v4-flash \
  STELLA_REFLECT_EVAL_DATASET_DIR=dist/reflect-evals/datasets/reflect-531-focused \
  STELLA_REFLECT_EVAL_MODE=531 \
  STELLA_REFLECT_EVAL_CASE_TIMEOUT=300s \
  mise exec -- go test -count=1 -tags reflecteval ./internal/reflect -run TestReflectReconciliationEvalManual -v
```

结果：

- 33/33 case pass。
- 0 deterministic check fail。
- 0 provider/runtime error。
- 6/6 LLM judge case verdict=pass。
- 全量耗时约 538 秒。

LLM judge 分数：

| case | action_fit | related_usefulness | write_accuracy | content_completeness | no_unrelated_content |
|---|---:|---:|---:|---:|---:|
| `scale_fact_many_active_records_multi_candidate` | 4 | 4 | 4 | 4 | 4 |
| `scale_skill_many_active_records_multi_candidate` | 4 | 4 | 4 | 3 | 4 |
| `stress_fact_large_catalog_multi_relation` | 4 | 4 | 4 | 3 | 4 |
| `stress_fact_retired_dependency_and_manual_exclusion` | 4 | 4 | 4 | 4 | 4 |
| `stress_mixed_large_catalog_dual_line` | 4 | 4 | 4 | 4 | 4 |
| `stress_skill_large_catalog_mixed_patch_create` | 4 | 4 | 4 | 4 | 4 |

## 5. 质量观察

### Related discovery

结论：在当前几十条 catalog 级别下基本可靠。

表现较好的点：

- 能在 20-33 条 active reflect-owned catalog 中找到正确相关项。
- 能排除 manual、inactive、跨域相似词和同项目不同子系统噪声。
- fact 线能覆盖 conflict、supersedes、same_entity_or_slot、depends_on、possibly_affects。
- skill 线能覆盖 patchable_gap、same_workflow、broader/narrower workflow、stale predecessor、overlapping trigger。

需要注意：

- relation label 本身有语义重叠，例如 `supersedes` 与 `depends_on/possibly_affects`、`patchable_gap` 与 `overlapping_trigger`。测试不能把同一语义边界锁得过窄，否则会误判正确输出。
- Related hint 只能作为 reconciliation 输入提示，不能作为 host 写入依据。最终仍需 target/source/status/version 校验。

### Fact reconciliation

结论：当前 V1 行为可靠，尤其是 knowledge 多目标替换和 singleton 处理。

覆盖到的操作：

- profile/soul `create_singleton`、`replace_singleton`、`noop`。
- knowledge `create`、`replace_many`、`deprecate_many`。
- 同一候选更新多条旧 fact。
- retired/obsolete fact 可以走 `deprecate_many`，也可以在仍有 durable negative instruction 时走 `replace_many`。后一种不一定错误，取决于是否需要保留“不要再用”的未来可读信息。

质量风险：

- Stress run 中曾出现一次 LLM reconciliation plan 协议错误：同一个 candidate 被重复 covered，host validator 正确 fail closed；单 case 复跑通过。若后续频率升高，应考虑给 reconciliation 也加 protocol repair retry。
- `stress_fact_large_catalog_multi_relation` 的 content_completeness=3，是因为 judge 认为 fact 写入是简洁事实句，不包含 procedure/trigger/verification。对 fact 来说这不是严重问题，但 judge rubric 对 fact/skill 共用时会偏严。

### Skill reconciliation

结论：当前 V1 可用，但比 fact 更容易出现边界差异。

覆盖到的操作：

- `create_skill`、`patch_skill`、`noop`。
- reflect-owned active user_agent skill 才能 patch。
- manual skill 不会被 patch。
- 大 catalog 下能正确 patch WSL/provider/OpenAPI skill，也能创建 browser visual verification skill。

质量风险（已在后续修复中处理主要问题）：

- 同一个 candidate 横跨多个已有 skill 时，模型可能选择 noop、patch 一个主 skill、或 patch 多个 skill。只要 judge 认为目标和内容正确，这属于可接受边界，但 deterministic 测试不应硬锁唯一操作。
- 有一轮单 case judge 给 `content_completeness=3`，原因是 skill patch 把 trigger/non-trigger 或 verification 信息压缩到正文里，没有完整保留候选字段。后续已加强 skill reconciliation prompt，要求 create/patch 保留 trigger、non-trigger boundary、procedure、verification。
- Stress run 中出现过一次 provider timeout，复跑通过。这是外部调用稳定性风险，不是内容质量问题。

## 6. 调试中修正的测试预期

本轮不是简单扩 case，也修正了几个过窄断言：

- `related_skill_overlapping_trigger_proxy_download` 允许 `patchable_gap`，因为“给已有 WSL proxy skill 补 package-download 分支”更像 patchable gap。
- `stress_mixed_large_catalog_dual_line` 允许 fact related relation 为 `supersedes`，因为候选是在扩展旧 OpenAPI 事实说明。
- `stress_fact_retired_dependency_and_manual_exclusion` 不强制 retired fact 必须 `deprecate_many`。如果候选带有“以后不要使用”的 durable negative instruction，`replace_many` 也合理。
- `stress_skill_large_catalog_mixed_patch_create` 不强制跨 provider/worker lease 的 candidate 必须 patch provider-stream。该类候选有多种合理处理方式，应主要看 judge 和 host target 校验。

## 7. 结论

在本地 33 case、最高 33 facts / 20 skills catalog、6 个 LLM judge case 的评估下，related discovery 和 reconciliation 的质量可以认为“基本可靠”，可以进入更完整的 532+531 e2e 测试。

但这不是生产级稳定性的最终结论：

- 目前测试 catalog 仍是几十条级别，不代表上百/上千条。
- LLM judge 与被测模型同源，存在偏差；后续可换另一个 judge model 或人工复核关键 case。
- Reconciliation plan 偶发协议错误已补充更具体的 repair retry 指令；provider timeout 仍需要作为外部调用稳定性风险记录。
- Skill 写入完整性已通过 prompt 加强 trigger、boundary、procedure、verification 的保留要求。

建议下一步：

1. 用这套 dataset 作为 #531 局部回归集。
2. 开始跑真正的 532 generation -> 531 related/reconciliation e2e。
3. 如果后续 e2e 中 provider timeout 频繁复现，再给 provider 调用增加 retry/backoff。

## 8. 后续修复验证

本报告之后又处理了两个风险点：

1. Reconciliation repair retry：复用现有 capture retry，并为 `submit_fact_reconciliation` / `submit_skill_reconciliation` 增加专门 repair 指令，明确 “Every candidate_ref must be covered exactly once across the whole plan”，并要求重复覆盖时删除重复覆盖或合并成一个合法 write。
2. Skill 写入完整性：加强 `skillReconciliationPrompt`，要求 `create_skill` / `patch_skill` 保留候选中的 trigger_examples、non_trigger_examples、procedure.steps、procedure.verification，不把 verification 压缩成泛化的 “confirm it works”。

新增/更新的本地验证：

- `TestReconciliationRunnerRepairsDuplicateFactCoverageAcrossOperations`：先构造重复覆盖失败，再验证第二次请求带 repair prompt 并恢复成单个合法 operation。
- `TestSkillReconciliationPrompt_PreservesApplicabilityAndVerification`：锁定 skill reconciliation prompt 对 trigger、boundary、procedure、verification 的要求。
- `stress_skill_large_catalog_mixed_patch_create` 单 case LLM judge 复跑目录：`dist/reflect-evals/531-532/20260708T074408Z`，结果 `judge=pass`，五项分数全为 4，其中 `content_completeness=4`。
