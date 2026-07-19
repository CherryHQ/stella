# Stella 能力差距分析:已支持 vs 待支持

> 与 `roadmap.md`(v3.1,数字员工雇佣制)配套。基于 2026-07 仓库全量盘点(main @ d895290c),每项附文件证据;待建清单按三幕对齐,2026-07-19 随 v3 评审更新。成熟度:**产品级** / **雏形** / **缺失**。

## 一、已支持(产品级,可直接依赖)

### 执行核

| 能力                       | 现状                                                                                                                                                                  | 证据                                                                                |
| -------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| Goal 引擎                  | 验收契约(确定性检查 + human/agent 判责复合树)、attempt 状态机、失败四分类(model/environment/contract/flaky)、blocked 原因枚举、可恢复 session、append-only 事件时间线 | `internal/goal/contract.go:23`、`types.go:52,69`、`converge.go:695`、`events.go:17` |
| Workflow(冻结 Goal 树模板) | `frozen/v0` 格式、hash、实例化生成 goal 树、run 幂等键去重                                                                                                            | `internal/workflow/frozen.go:15`、`service.go:241`                                  |
| Scheduler                  | cron/every/at、overlap 双保险(River unique + DB 层)、可触发 workflow 或 message-mode job                                                                              | `internal/scheduler/job.go:32`、`river.go:195`、`access.go:137`                     |

### 记忆(分身与组织记忆的地基)

| 能力                     | 现状                                                                                                                                                                | 证据                                                                  |
| ------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------- |
| per-user agent 记忆      | facts 按 `(user_id, agent_id, subject)` 唯一;LCM 混合检索(embedding + hybrid + fusion)注入 context,非全量塞入。**会话与记忆按相对方分仓**是混淆代理人防线的现成资产 | `internal/memory/facts.go`、`memory/lcm/retrieval.go`、`assembler.go` |
| Knowledge 生命周期       | 用户可管理的记忆视图:active/removed、retention 窗口、keyset 分页、Web 页已上线(#734)                                                                                | `internal/memory/profile/knowledge.go`、`web/src/features/memories/`  |
| Reflect(记忆/技能自进化) | 半成品偏可用:从会话抽取 fact/skill 候选,gate → 评分 → reconciliation,自动创建 user_agent skill;有独立 eval                                                          | `internal/reflect/`(60+ 文件)                                         |

### 渠道与交互

| 能力                 | 现状                                                                                                                                                                   | 证据                                                                |
| -------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------- |
| 飞书                 | 最完整:卡片 JSON 2.0 + Patch 原地更新、按钮回调→合成消息、群成员自动管理、线程路由、流式。**回调携带平台签发的 open_id** = 发起人身份不可伪造,网关双重检查的前提已成立 | `plugins/channels/feishu/action.go:21`、`render.go:63`              |
| Telegram / QQ / 微信 | 中等:handler/publisher/render 齐,微信有二维码注册                                                                                                                      | `plugins/channels/{telegram,qq,weixin}`                             |
| 群聊语义路由         | durable 派发 + 语义仲裁 + 意图分类,多 agent 群竞争发言权;只路由到 Agent                                                                                                | `internal/channel/group_dispatcher.go`、`semantic_group_arbiter.go` |
| 统一 Inbox           | 只覆盖 goal(blocked/review)+ scheduler run(failed)两个来源;严格限当前用户,fail-closed                                                                                  | `internal/inbox/service.go:50,90`                                   |

### 平台底座

| 能力          | 现状                                                                                                                                   | 证据                                                                           |
| ------------- | -------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------ |
| auth/authz    | 本地密码 + **OIDC/SSO 已存在**(PKCE)+ linkcode;authz default-deny 闭合动词集;角色仅 admin/user                                         | `internal/auth/oidc/`、`internal/authz/catalog.go:17`                          |
| 凭证与 secret | Vault(age 加密,user/system 双 scope)、PAT(constant-time、永不带 admin)、OAuth 刷新、sandbox 环境变量注入                               | `internal/vault/`、`internal/credential/pat.go:56`、`agent/sandbox/env.go:195` |
| Skill 系统    | ClawHub 安装、scope 五级(system→user_agent)、upload/upgrade API                                                                        | `internal/skills/clawhub.go`、`store.go:14`                                    |
| MCP           | 外部 MCP server 作工具源                                                                                                               | `internal/mcp/`                                                                |
| OAuth AS      | Stella 可自己当 IdP 签发 OAuth                                                                                                         | `internal/server/oauth_as.go`                                                  |
| 部署          | Helm chart(含 values.schema、外部 PG via Secret)、Dockerfile(容器强制外部 PG)、嵌入式 PG、`stellad upgrade`/`service`(systemd/launchd) | `deploy/helm/stella/`、`cmd/stellad/commands.go:70`                            |
| Web UI        | 聊天/agents/inbox/scheduler/goals/groups/recally + settings 全套(含 Knowledge/Skill 管理页)                                            | `web/src/routes/_app/`                                                         |
| API           | OpenAPI 覆盖 auth/agents/goals/workflows/inbox/scheduler/sessions/channels/providers 等全部核心资源                                    | `api/spec/domain/`                                                             |

## 二、待支持(缺失,按三幕对齐)

### 第一幕「转正」(Q1)——雇佣层全部从零建

| # | 待建能力                                                        | 现状                                                                                                            | 可借力的现有件                                                                                                                  |
| - | --------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------- |
| 1 | **Action Gateway v0 + Action Receipt**(授权检查 + 回执记录一体) | 缺失。幂等各自手写:workflow run 键、goal 唯一约束、River unique、eventlog 键——四套无横切框架,也无外部写审计账本 | 发起人身份已可信(渠道回调);authz 动词集、credential scope、Vault;eventlog 的 append-only + 幂等键模式;`agent_goal_event` 表形状 |
| 2 | **计件契约与聚合**(只计结果、外部证据、归因拆解)                | 缺失。只有 OTEL 技术指标,无业务计件模型                                                                         | 从回执重放推导,依赖 #1;口径规则已在 roadmap 冻结方向                                                                            |
| 3 | **单据时间线(读时重建)**                                        | 缺失。Inbox 与 goal 事件严格单用户,无跨用户读模型                                                               | 回执按外部对象引用查询 + SoR 实时读;**不建过程状态机**                                                                          |
| 4 | **网关双重检查 + 驳回回执闭环**(岗位授权 × 相对方资格)          | 缺失。Inbox fail-closed 只看自己;无按发起人的资格判定                                                           | authz Authority 可扩展;报销 skill 员工关系表 = 资格事实源                                                                       |
| 5 | **追人原语**(未决审批、缺失材料两类)                            | 缺失。通知有投递器,无升级规则/频率上限/结果核实                                                                 | `internal/notify/dispatcher.go`、Scheduler、渠道卡片                                                                            |
| 6 | **周报**(台账 → digest 模板 + 主管签收)                         | 雏形。digest 机制存在但只有 recally 阅读一个场景                                                                | `internal/scheduler/builtin_digest.go` 直接复用                                                                                 |
| 7 | **GitHub 集成**(研发助理原型)                                   | **完全缺失**——无工具、无连接器、无 skill,全仓 `github` 只是 Go import                                           | sandbox 跑 `gh` CLI + user skill;Vault/PAT 管 token                                                                             |
| 8 | **参与度基线埋点**                                              | 缺失(无"主动交互员工数"统计)                                                                                    | sessions/eventlog 数据已在库,只差聚合口径                                                                                       |

Q1 并行轨(SSO、K8s 冒烟)**基础已就绪**:OIDC 是现成的,SSO 打通主要是企业版侧账号映射;helm + 外部 PG 路径存在,只差冒烟验证。

### 第二幕「成队」(Q2)

| #  | 待建能力                                         | 现状                                                                                                                                                               | 可借力                                                   |
| -- | ------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ | -------------------------------------------------------- |
| 9  | **雇佣档案 + 岗位说明书**(= Pack manifest 真身)  | 缺失。`manifestplugins` 是 OAuth/托管插件清单,与此无关;skill 只能单个装                                                                                            | skill 的 DB 存储先例;`acceptance_contract` 的 JSONB 先例 |
| 10 | **Human Task 两类**(补材料、审批/判断)           | 缺失。人工介入只有 goal blocked 枚举 + `human_message` 事件                                                                                                        | goal 事件模型、卡片回调                                  |
| 11 | **统一 Inbox 纳入过程待办**(完成以 SoR 回执为准) | 缺失。Source 封闭枚举,仅 2 来源                                                                                                                                    | `internal/inbox/` 扩展点                                 |
| 12 | **授权清单与扩权机制**(两物种共用)               | 缺失。试用期零决定权、扩权留痕均无载体                                                                                                                             | 网关(#1)、authz 动作目录                                 |
| 13 | **组织记忆**                                     | **缺失,且地基比直觉薄**:Knowledge 是 per-user-agent facts 生命周期视图,授权严格"只读自己的";组织级需新建存储 scope + 跨 agent 授权模型,不是在现有 Knowledge 上开闸 | facts/LCM 检索栈可复用;授权边界全新设计                  |
| 14 | 研发助理引擎化(PR review = 带验收契约的 Goal)    | 依赖 #7 原型 + Goal 引擎(已产品级)                                                                                                                                 | acceptance contract 天然适配 CI/review 判定              |

### 第三幕「外派」(Q3)

| #  | 待建能力                                                                | 现状                                                            | 可借力                        |
| -- | ----------------------------------------------------------------------- | --------------------------------------------------------------- | ----------------------------- |
| 15 | **Pack v0.1 入职工具链**(导出/导入 + install/diagnose/upgrade/rollback) | 缺失。无任何配置打包导出                                        | #9 岗位说明书                 |
| 16 | **backup/restore 运维命令**                                             | 缺失。operator 命令有 upgrade/postgres/vault/service,无备份恢复 | `stellad postgres` 子命令体系 |
| 17 | 跨 agent 授权读(组织记忆落地)                                           | 依赖 #13                                                        | —                             |
| 18 | 外部写审计完整率验证                                                    | 依赖 #1 回执账本                                                | 第一方数据,天然可验           |

### 第四幕「站得稳」(Q4)

| #  | 待建能力                    | 现状                                         |
| -- | --------------------------- | -------------------------------------------- |
| 19 | 运维面板 / 业务运营面板分离 | 均缺失(#6 只有周报)                          |
| 20 | 回执完整率 SLO              | 依赖 #1(第一方数据,不存在第三方事件丢失问题) |
| 21 | 容量与成本控制              | 缺失                                         |

## 三、曾影响 roadmap 的两处修正(已吸收进 v3)

1. **组织记忆的起点比预想低。** 现 Knowledge 是 per-user-agent 记忆的管理视图,不是组织知识库;组织级要按"新建存储 scope + 授权模型,复用检索栈"估工作量。→ 已写入 roadmap 一.7 与 Q2 立项口径。
2. **GitHub 原型零存量。** "GitHub 工具"不存在,最低成本路径是 sandbox 内 `gh` CLI + user skill + Vault 管 token,按"从零写 skill"排期(数天,非数小时)。→ 已写入 Q1 副线。

## 四、意外资产(盘点发现,roadmap 已采纳或可随时取用)

- **Reflect 自进化管道**:员工"越干越好"、分身"越用越懂你"的复盘机制已存在;雇佣循环落地时,台账成为它的新输入源。
- **OAuth AS + MCP**:企业集成的两条现成通路(第三方接入 Stella / Stella 挂外部工具);保活实验"外部劳动力留门"的接口基础。
- **架构边界测试**(`architecture_boundary_test.go` + persistence tripwire):经办人模型与计件契约的架构规则有现成强制载体。→ roadmap 已采纳(一.7、风险 1/3)。
- **嵌入式 PG + 自升级 + service 安装**:个人自托管一键部署已成立,Cherry Studio 漏斗探针(保活实验 1)零额外投入。
