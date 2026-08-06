# Group Memory legacy → structured 切换手册

## 适用范围

本手册只用于首次启用 #757 structured Group Memory。切换从停服时刻之后的新公开 Group Event 开始生成 Group Facts：

- 不拆分或迁移旧 `ctx_group_memory.content` Blob。
- 不回放旧 `ctx_group_message`。
- 不回填旧 `ctx_group_message.actor_display_name`。
- 不回填旧 `ctx_message.origin_group_message_id`。
- 不重置现有 `lcm:<agent_id>` cursor。

切换后 legacy 与 structured 不能同时作为权威来源。

## 切换前检查

1. 数据库迁移已经包含连续版本 `90000000000004` 到 `90000000000006` 的 Group Memory migrations。
2. 所有将要启动的实例都支持：
   - `STELLA_GROUP_MEMORY_MODE=structured`。
   - `STELLA_GROUP_REFLECT_MODEL=<provider>/<model>`。
3. Group Reflect 后台模型已启用、凭证可用且声明至少 128k context。
4. 所有可能参与群聊的 Agent 聊天模型声明至少 128k context。
5. `ctx_group_fact` 和 `ctx_group_fact_changelog` 为空。
6. 已准备数据库备份和恢复步骤。

## 受控切换

1. 停止所有 Stella 实例。
2. 停止或隔离群消息入口，确认没有进程继续写入 Group Event Log。
3. 在目标数据库执行以下事务。不要拆开执行。

```sql
-- stella-group-memory-cutover-begin
BEGIN;

LOCK TABLE
  ctx_group_state,
  ctx_group_message,
  ctx_group_memory,
  ctx_group_fact,
  ctx_group_fact_changelog,
  ctx_group_ingest_cursor
IN ACCESS EXCLUSIVE MODE;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM ctx_group_fact)
     OR EXISTS (SELECT 1 FROM ctx_group_fact_changelog) THEN
    RAISE EXCEPTION
      'structured Group Memory cutover requires empty fact and changelog tables';
  END IF;
END
$$;

INSERT INTO ctx_group_ingest_cursor (group_id, pipeline, last_seq, updated_at)
SELECT id, 'group_reflect', next_seq, now()
FROM ctx_group_state
ON CONFLICT (group_id, pipeline) DO UPDATE
SET last_seq = GREATEST(
      ctx_group_ingest_cursor.last_seq,
      EXCLUDED.last_seq
    ),
    updated_at = now();

-- Structured mode reuses this table only as a version clock. Removing every
-- legacy row makes each group start its structured version at zero.
DELETE FROM ctx_group_memory;

COMMIT;
-- stella-group-memory-cutover-end
```

4. 运行以下检查；四项结果都必须为 `0`。

```sql
SELECT count(*) FROM ctx_group_fact;
SELECT count(*) FROM ctx_group_fact_changelog;
SELECT count(*) FROM ctx_group_memory;
SELECT count(*)
FROM ctx_group_state gs
LEFT JOIN ctx_group_ingest_cursor c
  ON c.group_id = gs.id
 AND c.pipeline = 'group_reflect'
WHERE c.group_id IS NULL
   OR c.last_seq <> gs.next_seq;
```

5. 所有实例统一设置：

```text
STELLA_GROUP_MEMORY_MODE=structured
STELLA_GROUP_REFLECT_MODEL=<provider>/<model>
# 可选；未设置时使用当前评测通过的默认门控。
STELLA_GROUP_REFLECT_GATE={"weights":{"evidence_strength":0.2,"subject_fit":0.2,"durability":0.2,"future_utility":0.2,"atomicity":0.2},"core_floor":3,"threshold":0.8,"candidate_cap":5}
```

`STELLA_GROUP_REFLECT_GATE` 设置后会在启动阶段严格校验；未知字段、缺失的权重维度和越界值都会阻止 structured Group Memory 启动。

6. 启动全部 Stella 实例，确认日志出现：
   - structured Group Reflect builtin 已注册。
   - schedule 为 `0 3,9,15,21 * * *`，或明确配置的开发 interval。
7. 恢复群消息入口。

## 切换后验证

1. 切换前 Event 的 `seq` 均不大于 `group_reflect` cursor，不会被回放。
2. 新公开 Event 会进入 pending Group Reflect 范围。
3. structured 群 turn：
   - 不读取或注入 legacy Blob。
   - 不暴露 Profile、Soul、Constraint 或 1v1 Knowledge。
   - 不读取 user/user_agent Skill。
4. 至少观察一个完整 Group Reflect 周期：
   - window 没有超时或持续失败。
   - candidate、gate、related、operation 和 watermark 日志/trace 可见。
   - 多个 Agent 读取同一 group version。
5. 至少触发一次 per-Agent LCM bootstrap/增量同步和一次测试 compaction，确认 origin 幂等与 KeepTail=6。

## 中止与回滚

- SQL 事务提交前出现任何错误：事务回滚，保持停服，修复后重新执行。
- SQL 已提交但 structured 实例尚未成功启动：保持群入口关闭；优先修复 structured 启动配置。需要恢复旧 Blob 时只能使用切换前数据库备份。
- 回滚旧二进制不会恢复已删除的 legacy Blob。旧二进制可以启动，但只能得到空 Group Memory；不要把代码回滚描述为内容恢复。
- structured 已产生任何 Group Fact 后，不得重新执行首次切换 SQL。后续修复应使用独立、经过评审的 repair 方案。
