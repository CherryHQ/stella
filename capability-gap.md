# Stella 能力差距分析:已支持 vs 待支持

> 与 `roadmap.md`(v3.2,数字员工雇佣制)配套。基于 2026-07 仓库全量盘点(main @ d895290c),每项附文件证据;待建清单按四幕对齐,2026-07-19 随 v3 评审与外部对抗评审(sol)更新——后者纠正的三处事实错误已吸收(见第三节)。成熟度:**产品级** / **雏形** / **缺失**。

## 一、已支持(产品级,可直接依赖)

### 执行核

| 能力                       | 现状                                                                                                                                                                  | 证据                                                                                |
| -------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| Goal 引擎                  | 验收契约(确定性检查 + human/agent 判责复合树)、attempt 状态机、失败四分类(model/environment/contract/flaky)、blocked 原因枚举、可恢复 session、append-only 事件时间线 | `internal/goal/contract.go:23`、`types.go:52,69`、`converge.go:695`、`events.go:17` |
| Workflow(冻结 Goal 树模板) | `frozen/v0` 格式、hash、实例化生成 goal 树、run 幂等键去重                                                                                                            | `internal/workflow/frozen.go:15`、`service.go:241`                                  |
| Scheduler                  | cron/every/at、overlap 双保险(River unique + DB 层)、可触发 workflow 或 message-mode job                                                                              | `internal/scheduler/job.go:32`、`river.go:195`、`access.go:137`                     |

### 记忆(分身与组织记忆的地基)

| 能力                     | 现状                                                                                                                                                                                                                                         | 证据                                                                                                       |
| ------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------- |
| per-user agent 记忆      | facts 按 `(user_id, agent_id)` 分仓;profile/soul(user/agent subject)靠部分唯一索引保证 active 单例,world facts 可多条;LCM 混合检索(embedding + hybrid + fusion)注入 context,非全量塞入。**会话与记忆按相对方分仓**是混淆代理人防线的现成资产 | `internal/db/migrations/20260625090000_add_facts_memory.sql:32`、`memory/lcm/retrieval.go`、`assembler.go` |
| Knowledge 生命周期       | 用户可管理的记忆视图:active/removed、retention 窗口、keyset 分页、Web 页已上线(#734)                                                                                                                                                         | `internal/memory/profile/knowledge.go`、`web/src/features/memories/`                                       |
| Reflect(记忆/技能自进化) | 半成品偏可用:从会话抽取 fact/skill 候选,gate → 评分 → reconciliation,自动创建 user_agent skill;有独立 eval                                                                                                                                   | `internal/reflect/`(60+ 文件)                                                                              |

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

## 二、待支持(缺失,按四幕对齐)

### 第一幕「转正」(Q1)——雇佣层全部从零建

| # | 待建能力                                                                                                         | 现状                                                                                                                                                                                                                                                                | 可借力的现有件                                                                                                                                                                                            |
| - | ---------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1 | **最小雇佣记录 + 授权清单 v0 + Action Gateway v0 + Receipt**(授权检查 + 回执记录一体;网关校验的主体先于网关存在) | 缺失。幂等各自手写:workflow run 键、goal 唯一约束、River unique、eventlog 键——四套无横切框架,也无外部写审计账本;岗位身份/凭证绑定/授权清单均无载体。**封路验收**:入网关的动作类,凭证须退出沙箱注入(现状 vault env 直接复制进 agent 环境,`agent/sandbox/env.go:172`) | 发起人身份已可信(渠道回调);authz 动词集、credential scope、Vault;eventlog 的 append-only + JSONB 模式可参考(`agent_goal_event` 列极少,回执 schema 须自建:发起人/岗位/动作/对象/结果/证据/策略版本/幂等键) |
| 2 | **计件契约与聚合**(只计结果、外部证据、outcome 唯一键、重开/撤销语义、归因拆解、契约版本冻结)                    | 缺失。只有 OTEL 技术指标,无业务计件模型                                                                                                                                                                                                                             | 从回执重放推导,依赖 #1;口径规则已在 roadmap 冻结方向                                                                                                                                                      |
| 3 | **单据时间线(读时重建 + 对账巡检 + 验收证据快照)**                                                               | 缺失。Inbox 与 goal 事件严格单用户,无跨用户读模型;人工直接操作 SoR 是正常路径,须靠定时巡检留观测回执撑住归因                                                                                                                                                        | 回执按外部对象引用查询 + SoR 实时读 + Scheduler 定时巡检;**不建过程状态机**                                                                                                                               |
| 4 | **网关双重检查 + 驳回回执闭环**(岗位授权 × 相对方资格 × 输出投影)                                                | 缺失。Inbox fail-closed 只看自己;无按发起人的资格判定;卡片"点击即授权"需一次性授权凭据(现回调把点击降级为合成文本,`plugins/channels/feishu/action.go:72`)                                                                                                           | authz Authority 可扩展;资格事实源 = 报销 skill 员工关系表(生产 Bitable 资产,不在本仓库,须在岗位说明书声明)                                                                                                |
| 5 | **追人原语**(未决审批、缺失材料两类)                                                                             | 缺失。通知有投递器,无升级规则/频率上限/结果核实                                                                                                                                                                                                                     | `internal/notify/dispatcher.go`、Scheduler、渠道卡片                                                                                                                                                      |
| 6 | **周报**(台账确定性聚合 → 冻结快照 + 主管签收)                                                                   | 缺失。digest 是 prompt 驱动的 Agent 模板(`builtin_digest.go`),只能复用其 Scheduler 调度与模板注册,报表数字须确定性生成、LLM 只写叙述                                                                                                                                | Scheduler 模板机制、渠道投递                                                                                                                                                                              |
| 7 | **GitHub 业务动作**(研发助理原型)                                                                                | 工具与 skill 缺失;**认证存量已有**:GitHub OAuth device flow(`internal/connections/oauth/types.go:13`)、token 获取(`internal/connections/service.go:379`)、Recally GitHub source。缺 PR/issue/review 动作                                                            | sandbox 跑 `gh` CLI + user skill;Vault/PAT/connections 管 token                                                                                                                                           |
| 8 | **参与度基线埋点**                                                                                               | 缺失(无"主动交互员工数"统计)                                                                                                                                                                                                                                        | sessions/eventlog 数据已在库,只差聚合口径                                                                                                                                                                 |

Q1 并行轨(SSO、K8s 冒烟)**基础已就绪**:OIDC 是现成的,SSO 打通主要是企业版侧账号映射;helm + 外部 PG 路径存在,只差冒烟验证。

### 第二幕「成队」(Q2)

| #  | 待建能力                                                                                    | 现状                                                                                                                                                               | 可借力                                                                  |
| -- | ------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------- |
| 9  | **雇佣档案 + 岗位说明书**(= Pack manifest 真身)                                             | 缺失。`manifestplugins` 是 OAuth/托管插件清单,与此无关;skill 只能单个装                                                                                            | skill 的 DB 存储先例;`acceptance_contract` 的 JSONB 先例                |
| 10 | **Human Task 两类**(补材料、审批/判断)                                                      | 缺失。人工介入只有 goal blocked 枚举 + `human_message` 事件                                                                                                        | goal 事件模型、卡片回调                                                 |
| 11 | **统一 Inbox 纳入过程待办**(完成以 SoR 回执为准)                                            | 缺失。Source 封闭枚举,仅 2 来源                                                                                                                                    | `internal/inbox/` 扩展点                                                |
| 12 | **扩权机制与授权留痕**(两物种共用;授权清单 v0 已随 #1 前移 Q1,此处是扩权流程与审计)         | 缺失。试用期零决定权、扩权留痕均无载体                                                                                                                             | 网关(#1)、authz 动作目录                                                |
| 13 | **组织记忆**                                                                                | **缺失,且地基比直觉薄**:Knowledge 是 per-user-agent facts 生命周期视图,授权严格"只读自己的";组织级需新建存储 scope + 跨 agent 授权模型,不是在现有 Knowledge 上开闸 | facts/LCM 检索栈可复用;授权边界全新设计                                 |
| 14 | **受控成长闭环**(失败/争议回执 → 候选改进 → 台账离线评估 → 主管批准 → 影子运行 → 晋级/回滚) | 缺失。Reflect 只从**会话**抽取候选(`internal/reflect/`),不消费回执;无离线评估、影子对照、版本晋级/回滚机制                                                         | Reflect 管道(gate → 评分 → reconciliation)加台账输入源;skill 版本化存储 |
| 15 | 研发助理引擎化(PR review = 带验收契约的 Goal)                                               | 依赖 #7 原型 + Goal 引擎(已产品级)                                                                                                                                 | acceptance contract 天然适配 CI/review 判定                             |

### 第三幕「外派」(Q3)

| #  | 待建能力                                                                | 现状                                                                                    | 可借力                                                                    |
| -- | ----------------------------------------------------------------------- | --------------------------------------------------------------------------------------- | ------------------------------------------------------------------------- |
| 16 | **Pack v0.1 入职工具链**(导出/导入 + install/diagnose/upgrade/rollback) | 缺失。无任何配置打包导出                                                                | #9 岗位说明书                                                             |
| 17 | **backup/restore 运维命令**                                             | 缺失。operator 命令有 upgrade/postgres/vault/service,无备份恢复                         | `stellad postgres` 子命令体系                                             |
| 18 | 跨 agent 授权读(组织记忆落地)                                           | 依赖 #13                                                                                | —                                                                         |
| 19 | 外部写审计完整率验证                                                    | 依赖 #1 回执账本;**分母须独立来源对账**(SoR 审计/变更记录),回执自证会漏掉绕过网关的动作 | Bitable 记录变更历史;台账三来源标注(gateway/legacy-direct/human-external) |

### 第四幕「站得稳」(Q4)

| #  | 待建能力                             | 现状                                         |
| -- | ------------------------------------ | -------------------------------------------- |
| 20 | 运维面板 / 业务运营面板分离          | 均缺失(#6 只有周报)                          |
| 21 | 回执完整率 SLO                       | 依赖 #1;分母同 #19,独立来源对账,不拿回执自证 |
| 22 | 容量与成本控制                       | 缺失                                         |
| 23 | 培训版本晋级/回滚的生产证据(Q4 门槛) | 依赖 #14 成长闭环                            |

## 三、曾影响 roadmap 的修正(已吸收进 v3/v3.2)

1. **组织记忆的起点比预想低。** 现 Knowledge 是 per-user-agent 记忆的管理视图,不是组织知识库;组织级要按"新建存储 scope + 授权模型,复用检索栈"估工作量。→ 已写入 roadmap 一.7 与 Q2 立项口径。
2. **GitHub 业务动作零存量,但认证存量已有**(2026-07 sol 评审纠正本文早先"完全缺失"的说法):OAuth device flow、token 获取、Recally source 都在;缺的是 PR/issue/review 动作与 skill,按"从零写 skill"排期(数天,非数小时)。→ 已写入 Q1 副线。
3. **sol 评审的另两处事实纠正**(2026-07):facts 唯一性是部分索引(仅 active 的 user/agent subject),"按 (user_id, agent_id, subject) 唯一"的旧表述不准;`builtin_digest.go` 是 prompt 驱动的 Agent 模板,周报只能复用其 Scheduler 调度,"直接复用 digest"的旧表述过度。→ 本文第一、二节已改。同轮评审的四条结构修正(成长闭环、雇佣记录前移、对账巡检、网关封路)见 roadmap v3.2 头注。

## 四、意外资产(盘点发现,roadmap 已采纳或可随时取用)

- **Reflect 自进化管道**:员工"越干越好"、分身"越用越懂你"的复盘机制已存在;雇佣循环落地时,台账成为它的新输入源。
- **OAuth AS + MCP**:企业集成的两条现成通路(第三方接入 Stella / Stella 挂外部工具);保活实验"外部劳动力留门"的接口基础。
- **架构边界测试**(`architecture_boundary_test.go` + persistence tripwire):经办人模型与计件契约的架构规则有现成强制载体。→ roadmap 已采纳(一.7、风险 1/3)。
- **嵌入式 PG + 自升级 + service 安装**:个人自托管一键部署已成立,Cherry Studio 漏斗探针(保活实验 1)零额外投入。
