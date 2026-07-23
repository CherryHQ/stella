# Stella Release Full Test 实施计划

## 目标与方案引用

- 方案来源：[solution.md](./solution.md)。
- 建立一套只在发布新版本前运行的 Release Full Test；日常开发继续使用现有 PR/Main CI/CD，不运行本套件。
- 以机器可读 Capability/Scenario 清单为唯一覆盖索引，完整纳入核心能力 C01-C19 和扩展能力 X01-X19。
- 每次执行都按同一份 `release_policy` 处理全部已登记场景，不按版本大小、改动范围或时间计划筛选；Nonblocking 和 Manual 结果必须报告，不能伪装成自动通过。
- 以 `mise run release:validate` 作为发布者唯一入口，输出一个发布结论和按层级、Capability、Scenario 拆分的诊断报告。
- 本计划以方案盘点使用的 `60435d5d84a093ef5647b720ce23193464fdc943` 为结构参考；首次建立清单时必须从当时最新 `main` 重新提取能力表面，不能直接复用旧的 240 个 OpenAPI Operation、58 个 Web Route 或 473 个测试文件计数。清单建立后作为长期版本化文件增量维护，发布时只重新提取表面做一致性校验，不自动重建清单。

## 第一部分实施状态

第一部分“建立能力与现有测试基线”已快进并复核到 `upstream/main` 的 `8e454a0112d51e0cde7e81ffd3ba2df0fb69672d`，对应本计划第 1 至第 3 步：

- 新增长期维护的 `test/capabilities.yaml`，登记 C01-C19、X01-X19、78 个初始 Scenario、14 类现有测试资产和 107 条直接测试证据。
- 当前表面为 240 个 OpenAPI Operation、58 个唯一 Web URL、17 个 CLI Command、13 个内置 Plugin、10 个 System Skill；55 个 Web URL 已映射，3 个非产品布局/文档 URL 有明确豁免。
- 排除本盘点工具自身的 tagged tests 后，已有资产计数为 473 个 Go Test 文件、0 个前端测试文件、10 个 System Test 文件、35 个 `httptest` 使用文件、57 个 `dbtest` 使用文件和 22 个 `memorytest` 使用文件；项目测试没有直接采用 Testify 或 Testcontainers。
- `mise run test:capabilities` 使用 `capability` build tag 运行清单单元测试、真实 `newApp()` CLI Tree 校验和报告生成，不进入日常 `go test ./...` 或普通 CI。
- 当前 Gap Report 为 25 `covered`、18 `partial`、32 `missing`、3 `manual-only`，共 53 个待补场景。它是第二部分的工作输入，不代表当前可以发布。
- 78 个 Scenario 已全部登记发布处置：65 `blocking`、11 `nonblocking`、2 `manual`；当前分为 40 个 Blocking Gap、11 个 Nonblocking Backlog 和 2 个 Manual Requirement，五项自托管目标不再误列为 Live，Live Scenario 共 13 个。
- 报告输出到忽略的 `dist/test-results/capabilities/inventory.json` 和 `inventory.md`，不另存一份需要人工同步的覆盖表。

第一部分的策略收口已经完成：所有 Scenario 均有 `release_policy`，五个可自托管目标已改为确定性阻塞测试，六条首批 Blocking Browser、其余 Browser Backlog、X06/X13 Manual Requirement 和 X11 公共 Feed 的非阻塞处置均已登记，报告会分开显示不同处置。第一部分没有补业务测试、Browser Runner、Live Smoke Runner、真实 Target、Tag workflow 或跨 Runner 最终门禁，不能把当前缺口报告误当成完整 Release Full Test。

## 全局约束

- 不修改或替代日常 `.github/workflows/lint-and-test.yml` 的职责；本任务只建设发布前显式运行的完整验收。
- Capability 表达稳定产品语义，OpenAPI Operation、Web Route、CLI Command、Plugin 和 System Skill 只是能力入口，不能按入口重复计算能力。
- 每项行为在最低充分层级验证；只有真实进程、浏览器、异步 Worker 或外部协议边界无法由低层证明时，才增加 System、Browser、Contract 或 Live Smoke。
- 手工 Runbook、测试人员目测和一次性脚本不能伪装成自动化证据；`release_policy: manual` 的场景必须记录人工结果或显式 waiver。
- 不为测试修改生产协议、增加测试专用 HTTP 后门或让生产代码识别“测试模式”；fixture 和 fake 通过公开协议接入。
- 复用现有 `test/system` Harness、包级测试和 `mise` Task；只有出现第二个真实复用者时才抽取公共 fake 或 fixture，避免先建设空泛测试框架。
- 每次运行使用唯一 Run ID、临时 `STELLA_HOME`、隔离数据库、显式环境变量 Allowlist 和有截止时间的等待；失败中途也必须清理已获得资源。
- Live Smoke 只使用测试专用低权限账户和 Secret；凭据值不进入仓库、Capability 清单、日志、Trace 或报告。
- 能够合规自动化的内置外部集成都必须登记专用 Live Target；X06 微信和 X13 真实 IdP 等当前缺少合规稳定身份的场景登记为 Manual Requirement，不伪造 Target。
- `Product Failure` 阻止发布；`External Blocked`、`Not Run` 和 `Flaky` 不能计为 Pass，只有绑定当前 Commit 和 Scenario 的显式人工 waiver 才能继续。
- 生成的日志、Trace、JSON、JUnit、截图和覆盖报告写入忽略的 `dist/test-results/`，执行完成后不得污染 Git 工作树。

## 实施前置依赖

- 在接入真实 Live Smoke 前，准备测试专用 Provider、Channel、Email、Marketplace 和适用的外部 CLI 环境，并明确 Secret 托管人、额度和清理责任。Webhook Receiver、MCP Echo、Docker、kind、外部 PostgreSQL 和 OTLP Collector 由测试自托管，不列为第三方 Live 前置。
- 每个 Channel 必须确认一个符合平台规则、可自动控制的外部发送者和观察者。若平台不允许自动化用户账号，不能用直发 Stella Webhook 冒充真实 Live Smoke；该平台保持覆盖缺口，直到取得合规自动化路径。
- 在平台验收落地前，明确 X01、X15、X18 所需的 OS、架构、Docker 和 Kubernetes 矩阵。Skip 不能代替某个宣称支持平台的通过结果。
- 在最终接入发布流程前，选择能访问上述 Secret 和平台环境的执行位置。前面的清单、确定性测试、Browser、Contract 和 Runner 实现不依赖该选择；最终发布绑定步骤依赖它。

## 第一部分策略收口（已完成）

**预期结果**

- `test/capabilities.yaml` 中每个 Scenario 同时声明 `layer`、`status` 和 `release_policy`。
- 默认确定性行为和已覆盖行为使用 `blocking`；C02、C05、C06、C07、C17、X02 六条 Browser Journey 为首批 Blocking Browser。
- C03、C10-C14、C16、C18、X10、X11 的 Browser 候选先作为 `nonblocking` Backlog；C18 Secret UI 和 X10 动态 Plugin 配置在 Browser Runner 实现时必须再次评估。
- X07-S02、X09-S02、X15-S02、X18-S02、X19-S02 分别改为 `contract` 或 `package` 层的确定性 `blocking` 场景，当前 `status` 仍保持真实缺口。
- X06-S02、X13-S02 使用 `status: manual-only`、`release_policy: manual` 和真实 Runbook；只读公共 Feed X11-S03 作为明确的 `live + nonblocking` 场景。
- 报告分别统计 Blocking Gap、Nonblocking Backlog、Manual Requirement 和 Live Scenario，不再只给出混合的 Gap 总数。

**改动区域**

- `test/capabilities.yaml`、`test/capabilities/{manifest,validate,report}.go` 及单元测试。
- `test/manual/release-integrations.md`：X06/X13 的执行步骤、通过标准和 Release Evidence 要求。
- `docs/changes/release-test-coverage/{solution,implementation}.md`。
- 不修改 `.github/workflows/release.yml`、普通 CI、业务测试或生产代码。

**依赖**

- 依赖第一部分现有 Manifest、表面校验和证据盘点；不依赖第二部分 Runner。

**验证方法**

- 严格 YAML 解析拒绝缺失或未知 `release_policy`，并验证 Manual Requirement 有 Runbook。
- 报告中的策略总数等于 Scenario 总数，Blocking/Nonblocking/Manual Gap 分类互斥且不遗漏。
- 五项重分类不再计入 Live，所有公开表面和 Evidence 引用仍通过校验。
- `mise run test:capabilities`、仓库格式、构建和普通测试通过，生成报告不污染 Git 工作树。

## 实现步骤

### 1. 在最新 main 上首次建立并基线化长期 Capability 清单（第一部分已完成清单与表面映射）

**预期结果**

- 新增 `test/capabilities.yaml`，登记稳定的 C01-C19、X01-X19 Capability ID、公开表面、完整适用 Scenario、测试层级、当前状态、发布策略、依赖和测试引用；该文件首次建立后长期维护，不在每次发布时删除重建。Live Target Schema 与引用在第二部分建立真实 Runner 和测试账户时加入。
- 重新从实际实现提取 OpenAPI、Web Route、CLI Command、Plugin、Sandbox 和 System Skill 表面；记录相对方案基线的新增、删除和改名，不把旧计数当作验收条件。
- 每个 Scenario 表达一个可观察的 Given/When/Then 行为，并覆盖适用的成功、持久化、权限、非法输入、异步、重试、重启、幂等、脱敏和清理边界。
- 每种对外宣称支持的内置集成都有对应 Contract Scenario；能够合规自动化的真实外部集成登记 Live Scenario，当前不能自动化的 X06/X13 登记 Manual Requirement 和升级条件。
- 人类可读能力报告由该清单生成，不再维护第二份会漂移的手工能力表；发布时的表面提取结果只用于比对，不能覆盖稳定的 Capability/Scenario 归并结果。

**改动区域**

- `test/capabilities.yaml`：唯一的 Capability、Scenario、Surface、Test Ref 和 Live Target 索引。
- 只读核对 `api/spec/domain/**/paths.yaml`、`web/src/routeTree.gen.ts`、`cmd/stellad/`、`cmd/stellad/plugins_imports.go`、`pkg/plugins/`、`internal/pluginhost/`、`plugins/`、`resources/skills/system/` 和 Sandbox Registry。
- `docs/changes/release-test-coverage/solution.md`：只有能力定义确实因最新 `main` 变化时才同步修正，不为实现细节反复改写方案。

**依赖**

- 从最新 `main` 的干净实现分支开始；不依赖后续测试 Runner。

**验证方法**

- 首次基线化时人工复核所有新增、删除和改名表面，并为非产品表面记录明确豁免理由。
- 检查 Capability ID 和 Scenario ID 唯一、稳定，且没有按 Web/API/CLI 入口重复登记同一业务行为。
- 对 C01-C19、X01-X19 逐项检查适用场景，不以“至少一条 Happy Path”冒充完整能力验收。
- 模拟新增一个未登记公开表面，确认发布校验失败且 `test/capabilities.yaml` 不被自动修改。

### 2. 实现能力表面校验与覆盖报告（第一部分已完成基础校验）

**预期结果**

- 增加一个小型测试工具读取 `test/capabilities.yaml`，校验 Schema、ID、`layer/status/release_policy`、测试引用和表面映射，并在 `dist/test-results/` 生成机器可读 JSON 和人类可读 Markdown 报告。
- OpenAPI 校验直接解析 `api/spec/domain/**/paths.yaml` 中的 `operationId`。
- Web 校验读取构建生成的 `web/src/routeTree.gen.ts` 路由树结果，不根据文件名猜测最终 URL。
- CLI 校验在 `cmd/stellad` 包测试中复用 `newApp()` 遍历真实 `urfave/cli` Command Tree，不增加生产导出函数或测试后门。
- Plugin 校验通过真实 Catalog/Host 注册结果和内置 Manifest/Skill Registry 取值，不只扫描目录名或 blank import 文本。
- `mise run test:capabilities` 汇总这些检查；任何未映射表面、重复 ID、未知策略、无效引用或未说明豁免都会失败。

当前第一部分已完成 Schema、ID、Evidence Ref、OpenAPI、Web、真实 CLI Tree、真实 Plugin Catalog、System Skill、发布策略校验和分类报告。Live Target 尚未建立，不在清单中伪造目标。

**改动区域**

- `test/capabilities/`：Manifest 解析、结构校验、表面集合比对、报告生成及其单元测试。
- `cmd/stellad/*_test.go`：仅增加遍历真实 CLI Command Tree 和内置 Plugin 注册结果的表面校验。
- `web/`：增加只读导出生成路由集合的测试脚本或测试入口，不修改生成的 `routeTree.gen.ts`。
- `mise.toml`：新增 `test:capabilities`，不接入日常 CI/CD。

**依赖**

- 依赖第 1 步稳定 Manifest 结构。

**验证方法**

- 单元测试分别证明重复 ID、未知 Layer/Status/Release Policy、缺失 Test Ref、Manual Requirement 无 Runbook、未映射 Operation/Route/Command/Plugin 和无理由豁免会失败。
- 使用测试 fixture 增加一个虚构公开表面，确认校验明确报告来源和缺失映射，而不是只返回非零退出码。
- 在未改业务代码的基线运行 `mise run test:capabilities`，报告中的表面总数与重新盘点结果一致。
- 检查生成报告只写入 `dist/test-results/`，运行前后 `git status --short` 不新增生成物。

### 3. 盘点并映射现有测试证据（第一部分已完成首次基线）

**预期结果**

- 首次集中盘点并分类现有测试资产：Go `testing`/`httptest`、`internal/db/dbtest`、`internal/memory/memorytest`、包内 Fake、System Harness、Docker Contract、mise/Go Coverage/Race、GitHub Actions、构建/质量门禁以及手工 API/Browser Runbook。
- 对每项资产记录“测试框架/辅助设施/任务编排/质量门禁/探索工具”的定位、实际引用证据和能力边界，避免把已安装但未使用的依赖、构建通过或手工命令误记为自动化功能测试。
- 扫描现有 Go Test、System Journey、mise Task、手工 API/Browser Runbook、Plugin Contract 和 Release/Package Check，将能直接证明行为的测试映射到 Scenario。
- Test Ref 使用可解析的 package/file/test/subtest 或 Browser/Live Scenario 标识，使 `go test -json`、Playwright JSON 和 Live Result 能回映 Capability。
- 每个 Scenario 标记当前 `status` 和稳定 `release_policy`；fixture 顺带创建资源或执行路径碰到代码不能标为直接覆盖。
- 生成第一份 Gap Report，按 Capability、Layer 和 Release Policy 列出 Blocking Gap、Nonblocking Backlog 和 Manual Requirement，不按测试文件数量或代码行覆盖率排序。
- 将首次盘点结果固化为长期维护的 Test Ref 和状态；后续功能或测试变更增量更新，每次 Release 只校验引用、执行场景并生成本次报告，不重新从零人工盘点。
- 后续步骤只根据 Gap Report 补缺口，避免重写已经在最低充分层级获得稳定证据的测试。

**改动区域**

- `test/capabilities.yaml`：补齐现有 Test Ref 和初始覆盖状态。
- `docs/changes/release-test-coverage/solution.md`：保留已有框架、工具、基础设施及其边界的基线盘点；实现中发现事实变化时据实更新。
- 现有测试文件：只在测试身份无法稳定引用时调整测试名称或子测试名，不为映射做大规模无行为变化重命名。
- `dist/test-results/capabilities/`：生成未跟踪的 Evidence 和 Gap Report。

**依赖**

- 依赖第 2 步的 Manifest 校验和报告工具。

**验证方法**

- 对照代码引用抽查框架/工具清单，确认没有把间接依赖、未配置的前端框架、质量工具或手工 Runbook 标成已有自动化测试框架。
- 随机抽查每个 Layer 和每个能力组的 Test Ref，确认测试包含直接可观察断言。
- 删除一个 fixture 副作用对应的错误映射，确认报告仍把目标 Scenario 标记为缺口。
- 报告明确列出所有 `partial`、`missing` 和 `manual-only`，且 Release Full Test 尚未完成时不能输出整体通过。

### 4. 统一 Release Test 的隔离、结果和诊断契约

**预期结果**

- 所有高层 Runner 共享同一 Run 元数据：待发布 Commit、版本、平台、开始时间、Run ID、Capability ID、Scenario ID、Attempt 和依赖别名。
- 统一结果状态和 Artifact 目录约定；Go、System、Browser、Contract、Live 和 Package 层都能产出可聚合结果。
- 扩展现有 `test/system` Harness 的日志和 fixture 支持，但保留真实 `stellad`、真实 TCP、嵌入式 PostgreSQL、环境 Allowlist、Context Deadline 和有序 Journey 设计。
- 测试数据通过 API 或稳定 fixture 创建，按 Run ID 隔离；失败时保留 Server Log、Fake Request Log、数据库诊断和关键资源 ID。
- 结果聚合器能从测试引用把底层失败映射回 Scenario；一次重试后通过显示为 `Flaky`，不能静默变成 `Pass`。

**改动区域**

- `test/system/harness_test.go` 及相邻 Harness 文件：Run 元数据、Artifact 路径、通用 fixture 和诊断输出。
- `test/capabilities/`：结果数据结构、Test Ref 解析和报告聚合。
- `dist/test-results/release/<run-id>/`：统一的未跟踪输出布局。
- 必要的 `mise.toml` 内部 Task 参数传递；不改变普通 `mise run test` 的开发体验。

**依赖**

- 依赖第 2 步确定 Scenario 和 Test Ref 的数据契约；可与第 3 步证据盘点后半段并行。

**验证方法**

- 让一个受控 System fixture 失败，确认报告包含 Scenario、Expected/Actual、Server Log 路径和 Run ID。
- 让 Harness 在启动中途失败，确认子进程、PostgreSQL 和临时目录仍被回收。
- 连续执行两次基础 System Suite，确认数据不冲突、旧 Artifact 不被当成本次结果、工作树保持不变。
- 检查报告和日志不包含 Vault Key、Token、Cookie、模型 API Key 或完整用户内容。

### 5. 建立版本化 Browser E2E Runner

**预期结果**

- 在 `web` 中引入 Playwright Test 和 Chromium，测试规范、fixture、断言、Trace 和 Report 随仓库版本控制。
- Browser Runner 启动测试专用 `stellad`，使用临时 Home、隔离数据库、fake Provider 和唯一 Run ID，不复用开发者服务器、浏览器 Profile 或个人凭据。
- 前置数据优先通过 API/fixture 创建；浏览器只操作当前 Scenario 真正需要证明的用户步骤。
- 使用 Locator、Web-first Assertion、网络/SSE 事件和有界状态轮询，不使用固定长 `sleep`。
- 首批实现 C02 登录、C07 Chat SSE、C05 Agent、C06 Provider Secret、C17 公开 Share 和 X02 Channel 管理六条 Blocking Browser Journey。
- 其余 Browser 候选先检查低层是否已经充分证明行为；薄 CRUD 归并到最低充分层级，确有 UI 价值但暂缓的场景作为 Nonblocking Backlog。
- `mise run browser-test` 运行全部已实现且登记的 Browser Scenario，失败时保留 Trace、截图、网络摘要和 Server Log。

**改动区域**

- `web/package.json`、`web/pnpm-lock.yaml`：Playwright Test 依赖和内部脚本。
- `web/playwright.config.ts`、`web/e2e/`：配置、fixture、页面对象仅在重复确实存在时抽取、C02/C07 基准 Journey。
- `test/system` 或复用的启动辅助：仅共享进程级契约；不把 Go Harness 强行跨语言复制成第二套业务 fixture。
- `mise.toml`：新增 `browser-test`。

**依赖**

- 依赖第 2 步 Scenario/Test Ref 规则和第 4 步 Run/Artifact 契约。

**验证方法**

- 连续运行 `mise run browser-test` 两次，确认环境隔离、清理和结果稳定。
- 分别破坏一个 UI Locator、一个 API 响应和一个 fake Provider 脚本，确认报告能够区分失败层并保留 Trace。
- 检查 Browser Journey 只覆盖关键用户路径，没有把全部 OpenAPI CRUD 机械复制到浏览器层。
- 复核 C18 Secret UI 和 X10 Plugin 动态配置页是否应从 Nonblocking 升级为 Blocking，并记录依据。
- 检查 Chromium 之外的浏览器没有被误报为已覆盖；后续只有在产品明确承诺支持时才加入矩阵。

### 6. 补齐核心能力 C01-C07

**预期结果**

- C01 覆盖首次迁移、重复启动、Readiness、外部数据库错误、Graceful Drain 和进程清理。
- C02-C04 覆盖注册、登录、退出、Session 撤销、密码、用户权限、PAT、OAuth Client 和跨租户拒绝。
- C05-C06 覆盖 Agent、用户分配、Tool Override、Provider 配置、模型发现和缓存；模型请求使用严格 fake，不访问公网。
- C07 覆盖 Session/History、Chat SSE、Attach、Cancel、Provider Error、Compaction 后继续对话和浏览器流式展示。
- 每个行为放在最低充分层级，只有 Startup/Auth/SSE/跨请求和真实浏览器行为进入 System/Browser。
- Capability Report 中 C01-C07 不再存在 `partial`、`missing` 或 `manual-only`。

**改动区域**

- `internal/server/`、相关 Service/Store 包及其现有 `*_test.go`：补 Unit/Integration 缺口。
- `test/system/`：扩展 Startup、Auth、Agent/Provider 和 Chat Journey；保持 Graceful Drain 最后执行。
- `web/e2e/`：认证、Agent/Provider 配置和 Chat 关键用户旅程。
- `test/capabilities.yaml`：更新 C01-C07 Test Ref，不修改稳定 ID。

**依赖**

- 依赖第 3 步 Gap Report、第 4 步 System Harness 和第 5 步 Browser Runner。

**验证方法**

- 运行 C01-C07 相关包测试、`mise run system-test` 和 `mise run browser-test`。
- `mise run test:capabilities` 确认 C01-C07 全部适用 Scenario 有直接自动化证据。
- 注入 fake Provider 错误、断流和超时，确认 SSE 终止且测试不会无限等待。
- 复核权限和跨租户场景使用不同真实用户身份，不以直接数据库改写跳过授权边界。

### 7. 补齐核心能力 C08-C14

**预期结果**

- C08-C09 覆盖 Workspace 文件生命周期、上传、移动、删除、空间用量、Project Scope 隔离和 Project Session。
- C10 覆盖 Goal Draft/Activate、依赖、计划审批、取消、重试、Verdict、Archive、Worker 推进、重启恢复和失败清理。
- C11-C12 覆盖 Workflow 保存/实例化、Scheduler CRUD、立即运行、Run 记录和重启后的持久化调度。
- C13 覆盖 Profile/Soul/Constraint/Knowledge、Replace/Deprecate/Restore、Compaction、Reflect/Reconciliation 和用户/Agent 隔离。
- C14 覆盖多 Scope Skill CRUD、文件、ZIP 上传、可见性和 Agent Runtime 使用。
- Capability Report 中 C08-C14 不再存在 `partial`、`missing` 或 `manual-only`。

**改动区域**

- Workspace、Project、Goal、Workflow、Scheduler、Memory、Reflect、Skill 的 Service/Store 测试。
- `test/system/`：只增加异步 Worker、跨请求、重启恢复和 Agent Runtime 等低层无法证明的 Journey。
- `web/e2e/`：文件、Project、Goal、Workflow、Scheduler、Memory 和 Skill 的关键用户旅程。
- `test/capabilities.yaml`：更新 C08-C14 Test Ref。

**依赖**

- 依赖第 3 至第 5 步；与第 6、8 步可在共享 Harness 稳定后按文件边界并行。

**验证方法**

- 运行相关包测试、System 和 Browser Scenario，并对异步流程使用有截止时间的状态轮询。
- 重启真实 `stellad` 后检查 Scheduler、Goal、Workspace 和 Memory 持久化结果。
- 强制中途失败，确认 Workspace 文件、Goal Session、River Job 和临时资源没有泄漏。
- `mise run test:capabilities` 确认 C08-C14 的全部适用 Scenario 有直接证据。

### 8. 补齐核心能力 C15-C19

**预期结果**

- C15 覆盖 Group CRUD、成员、历史、Group SSE、多 Agent Routing、并发和权限隔离。
- C16 覆盖 Attention Inbox、Agent Notify Tool 和用户通知 Identity。
- C17 覆盖 Share 创建、公开访问、撤销、过期/不存在和 Agent Share Tool。
- C18 覆盖多 Scope Secret 生命周期、加密存储、授权、脱敏和 Agent Vault Tool。
- C19 覆盖 Builtin Resource/Tool 发现、装配、禁用后不可调用和 Scope 可见性。
- Capability Report 中 C15-C19 不再存在 `partial`、`missing` 或 `manual-only`。

**改动区域**

- Group、Inbox/Notify、Share、Vault、Builtin Resource/Tool 相关包及测试。
- `test/system/`：Group SSE、多 Agent 调度、通知交付和 Tool Runtime 等跨组件 Journey。
- `web/e2e/`：Group、Inbox、Share、Vault 的关键用户旅程。
- `test/capabilities.yaml`：更新 C15-C19 Test Ref。

**依赖**

- 依赖第 3 至第 5 步；与第 6、7 步可在共享 Harness 稳定后按 Capability 和文件边界并行。

**验证方法**

- 使用至少两个用户和多个 Agent 验证 Group、Share、Vault 和通知的身份边界。
- 检查 Secret、Token 和私有 Artifact 不出现在浏览器 Trace、Server Log、错误消息或 Capability Report。
- `mise run test:capabilities` 确认 C15-C19 的全部适用 Scenario 有直接证据。

### 9. 建立严格 Contract 与 Live Smoke Runner

**预期结果**

- Contract Test 的 fake 必须校验请求路径、方法、Header、鉴权、Payload、Streaming/回调格式和最终业务结果；“任意请求返回 200”不能计为 Contract。
- 协议解析、序列化、错误、超时、限流、重连和幂等优先留在各 Plugin/Service 包；只有跨真实进程链路需要时才接入 `test/system`。
- 新增 `test/live/` Runner，按 Capability 清单中的 Live Target Registry 加载适配器、执行 Preflight/Run/Cleanup，并输出统一 Scenario Result。
- Live Target Registry 只保存 Target ID、实现类型、Secret 环境变量名和非敏感配置；Secret 值由执行环境注入。
- Runner 默认执行全部登记 Target；可以有单 Target 调试入口，但 `release:validate` 不传筛选参数。
- Webhook Receiver、MCP Echo Server、Docker、kind/外部 PostgreSQL 和 OTLP Collector 作为确定性测试 fixture，不进入 Live Target Registry。
- 先用一个低额度 Provider Target 打通 Live Runner、脱敏、Deadline、清理、结果分类和 waiver 报告闭环，再扩展其他真实集成。

**改动区域**

- 各 `plugins/*`、`internal/email`、`internal/mcp`、`internal/connections`、`internal/embedding` 等现有包测试：严格 Contract fixture。
- `test/live/`：Runner、Adapter 接口、Preflight、结果输出、Secret Allowlist、清理和单元测试。
- `test/capabilities.yaml`：Live Target Registry 和 X 系列 Scenario/Test Ref。
- `mise.toml`：新增 `live-smoke`，默认运行全部登记 Target。

**依赖**

- 依赖第 2 步 Manifest 校验和第 4 步结果契约；可与核心能力补齐并行。

**验证方法**

- fake 收到错误路径、缺失 Header、错误 Payload 或意外额外请求时，Contract Test 明确失败。
- 缺失任一登记 Secret 时，Live Runner 输出 `Not Run`；第三方不可达时输出 `External Blocked`；两者不能算 Pass，但允许记录显式 waiver 后由最终聚合器继续。
- 模拟 Stella 请求或解析错误，确认 `Product Failure` 无法通过 waiver 自动放行；重试后通过必须输出 `Flaky` 和全部 Attempt。
- 使用 canary Secret 检查所有日志和报告，确认值被完全脱敏。
- 连续运行参考 Target 两次，确认外部测试资源清理、Run ID 隔离和旧结果不复用。

### 10. 补齐 Channel X02-X07

**预期结果**

- X02 公共能力覆盖 Channel CRUD、Runtime Lifecycle、Identity Routing、Inbound/Outbound、Streaming、重复消息和重试。
- Feishu、QQ、Telegram、Weixin 和 Webhook 分别具备严格的 Payload/签名/鉴权 Contract，并验证各自声明支持的 Thread、Reaction、Attachment、Media 或 Streaming 能力。
- 能够合规自动化的平台使用测试专用真实 Target 完成双向 Live Smoke：外部发送含 Run ID 的消息，Stella 正确接收和路由，Stella 回复同一 Run ID，外部观察者确认收到。
- 直发本地 callback、复用 fake Payload 或只验证“配置保存成功”不能计为该平台 Live Smoke。
- Webhook 出站交付使用本地受控 Receiver 做确定性 Contract；不再登记虚假的外部 Webhook Live Target。
- 微信当前缺少合规自动化身份，X06-S02 保持 Manual Requirement；将来具备受控身份后再升级为自动化 Live。

**改动区域**

- `plugins/channels/{feishu,qq,telegram,weixin,webhook}/` 及现有测试。
- 必要的 `test/system/` Channel Runtime Journey。
- `test/live/` 各 Channel Adapter 和观察者集成。
- `test/capabilities.yaml`：X02-X07 Contract/Live Scenario 和 Target。

**依赖**

- 依赖第 9 步 Runner；真实双向场景阻塞于各平台测试账号和合规自动化能力。

**验证方法**

- 运行各 Channel 包 Contract Test，覆盖正常、签名错误、重复、限流、超时和重试。
- 对每个已登记自动化平台运行真实双向 Live Smoke，报告包含脱敏 Message ID、Stella Event ID 和双向时间戳；Manual 平台记录人工结果或 waiver。
- 关闭一个真实 Target 或撤销权限，确认结果是 `External Blocked`，不能记为 Pass，只有显式 waiver 后才能继续。
- 清理可删除消息；无法删除的测试消息带统一前缀和保留期限。

### 11. 补齐 Email、MCP、Recally、Provider、OIDC 和 Embedding X08-X14

**预期结果**

- X08 使用 fake Mail Server 覆盖 List/Read/Mark/Send 和 Agent Tool，并用专用邮箱完成真实收件、读取、发件和收件确认。
- X09 使用测试自托管 MCP Echo Server 覆盖 Initialize、Discovery、Namespacing、Invoke、Error、Timeout、断连和重连，作为确定性阻塞 Contract。
- X10 的 Plugin 控制面 Contract 由第 12 步统一补齐；本步骤只处理依赖外部服务的 Plugin Adapter。
- X11 使用本地 Feed fixture 覆盖 Poll、Entry、Article、Digest 和 Scheduler，并对登记的稳定只读 Feed 运行真实 Smoke。
- X12 对 Anthropic、OpenAI、OpenAI Responses 分别覆盖 Model Discovery、Streaming、Tool Call、错误和 Rate Limit Contract，并为每个 Provider Plugin 登记低额度真实模型 Target。
- X13 覆盖 fake OAuth/OIDC Device、Callback、Poll、Revoke 和 Login；真实 IdP 登录当前保持 Manual Requirement，避免脆弱或违反 ToS 的自动化身份。
- X14 覆盖 fake Embedding、Backfill、Index、Semantic Query 和重启恢复，并使用专用真实 Embedding Target。

**改动区域**

- `internal/email`、`internal/mcp`、`internal/recally`、`internal/connections`、`internal/embedding` 及相关 Server/Tool 测试。
- `plugins/providers/{anthropic,openai,openai-response}/` Contract Test。
- `test/system/`：只增加需要真实进程、Worker 或 Agent Tool 装配的 Journey。
- `test/live/`：Email、Feed、Provider、Embedding 和将来具备合规目标的 Identity Adapter；MCP Echo 放在确定性 Contract fixture。
- `test/capabilities.yaml`：X08-X09、X11-X14 Scenario 和 Target。

**依赖**

- 依赖第 9 步；可与第 10、12 步按集成目录并行。

**验证方法**

- 逐个运行包级 Contract、System 和真实 Live Scenario，确认每个 Provider Plugin 和外部类型都有独立结果。
- Email 使用唯一 Message-ID/Run ID 验证双向投递并清理；MCP 校验协商协议版本和 Tool 返回值。
- OIDC Contract 撤销授权后再次访问必须失败；Embedding 重启后查询结果仍可用。
- Capability Report 分别展示 X08-X09、X11-X14 的 Blocking、Nonblocking、Manual 和 Live 状态，不用缺少真实 IdP 自动化冒充协议缺口。

### 12. 补齐 CLI、Plugin、Sandbox、Skill、发布部署和可观测性 X01、X10、X15-X19

**预期结果**

- X01 在临时 Home 中通过真实子进程覆盖 Version、Server、Upgrade Fixture、PostgreSQL Runtime、Vault Keygen、mise Reconcile 和 Service Adapter；支持平台上验证真实 Service Install/Start/Status/Stop/Uninstall。
- X10 覆盖 Plugin List/Toggle/Config/Schema/Status/Reload、Manifest Sync、Tool/Hook 装配和禁用后不可用。
- X15 覆盖 Local/Docker/None Sandbox Contract、权限、Process/Container Lifecycle、Tool Cache、Preflight 和失败清理，并在 CI Docker Daemon 运行确定性阻塞矩阵。
- X16-X17 覆盖 Marketplace Search/Install/Upgrade、Builtin Skill Discovery/Injection 和声明依赖的外部 CLI；真实 Marketplace/CLI 使用专用或只读 Target。
- X18 对每个支持的发布物验证 Archive 解压后启动、Upgrade、Service、Docker Start/Ready、Helm Render/Install 和外部 PostgreSQL，而不只检查压缩包中存在 Binary。
- X19 使用自托管 OpenTelemetry Collector 验证 Structured Log、Trace Hook、OTLP Export、失败恢复和敏感字段脱敏。
- 平台矩阵中的每个必需单元都有独立结果；无对应 Runner 的平台不能以 Skip 计为通过。

**改动区域**

- `cmd/stellad/` CLI/Service 测试、`internal/pluginhost/`、`pkg/plugins/`、`plugins/sandbox/`、`resources/skills/system/`。
- `.goreleaser.yaml`、`Dockerfile`、Helm/部署目录及其测试脚本；只在测试发现真实产品缺陷时修改生产配置。
- `test/system/`、Contract 和 Package fixture：CLI Process、Docker/kind、外部 PostgreSQL 和 OTLP；`test/live/` 只保留真正不受我们控制的 Marketplace/CLI 场景。
- `test/capabilities.yaml`：X01、X10、X15-X19 Scenario、平台矩阵和 Target。

**依赖**

- 依赖第 4、9 步；Service、Docker 和 Kubernetes 验证阻塞于平台矩阵和可自托管执行环境，不阻塞于第三方凭据。

**验证方法**

- 在每个支持平台运行真实 CLI/Service 生命周期，检查退出码、stdout/stderr、生成文件和残留进程。
- 对 Snapshot Archive 解压后执行 Binary readiness；对 Docker/Helm 部署执行启动、迁移、Ready、最小 API 请求和清理。
- Sandbox 失败场景后检查无残留 Process、Container、Network、Volume 和 Tool Cache 临时资源。
- OTLP Backend 按 Run ID 查询 Trace，确认存在预期 Span 且无 Token、Secret 或用户私有内容。
- Capability Report 确认 X01、X10、X15-X19 的平台单元全部有实际结果。

### 13. 接成单一 release:validate 与统一报告

**预期结果**

- `mise run release:validate` 按失败优先、严格串联的顺序运行 Format、Generate Check、Build、Capability Check、Go Test、System、Browser、Release Check/Snapshot 和全部 Live Smoke。
- 普通 `mise run test`、日常 PR/Main CI/CD 和开发者局部测试命令保持原职责；发布者只需要执行一个完整入口。
- 统一报告包含 Commit、版本、平台、时长、所有 Scenario 状态、Expected/Actual、Attempt、Artifact 链接和最终 `release_allowed`。
- 统一报告分开显示 Blocking Gap、Nonblocking Backlog、Manual Requirement 和 Live Result；最终结论由 `release_policy`、本次结果和显式 waiver 决定。
- 第二部分第 0 步先在 Tag workflow 增加 Validate Job，至少执行 `test:capabilities`、普通测试和 System Test；GoReleaser 与 Docker Job 使用 `needs: validate`，不修改日常 CI。
- 最终发布流程在 Artifact 对外发布前执行完整任务。Tag 门禁失败会留下未发布 Tag；若以后要求 Tag 本身也只能在验证后创建，再增加基于 Commit 的 Promotion Workflow。
- X18 必须验证即将发布的同一份候选 Artifact；不能测试 Snapshot A 后无说明地重新构建并发布 B。
- 更新 Release、System 和 Web UI 测试开发文档，说明唯一入口、环境准备、失败诊断、Secret 规则和清理方式。

**改动区域**

- `mise.toml`：内部组件 Task 和最终 `release:validate` 顺序。
- `test/capabilities/`：跨 Runner 结果聚合和最终报告。
- `.github/workflows/release.yml`：仅在最终选择 Release 专用 Runner 时增加发布前依赖；不修改 `.github/workflows/lint-and-test.yml`。
- `web/content/docs/development/rules/release.md`、`system-test.md`、`web-ui-test.md` 及适用的现有文档。

**依赖**

- 基础 Tag Validate 不阻塞于全部缺口补齐；最终 `release_allowed` 聚合门禁阻塞于所有 Blocking Scenario 可执行，并阻塞于执行位置、Secret 托管和平台环境选择。

**验证方法**

- 从干净 Checkout 对待发布 Commit 运行 `mise run release:validate`，确认所有层级均被调用且最终报告完整。
- 分别让 Capability Check、Go、System、Browser、Contract、Live 和 Package 场景失败，确认任务立即返回非零且保留已产生 Artifact。
- `Product Failure` 必须令 `release_allowed=false`；`External Blocked`、`Not Run`、`Flaky` 和 Manual Requirement 必须产生 `needs_waiver`，只有绑定当前 Commit/Scenario 的批准记录才能继续。
- 检查日常 CI Workflow 没有新增 Release Full Test、真实 Secret 或外部服务依赖。
- 对照文档逐条执行，确认发布者不需要知道内部子任务即可完成一轮测试和定位失败。

### 14. 完成两轮发布演练与验收基线

**预期结果**

- 在实际发布环境对同一候选 Commit 连续执行两轮完整 Release Full Test，两轮均覆盖全部登记 Scenario 和 Live Target。
- 第二轮不依赖第一轮遗留数据、登录态、外部消息或 Artifact，证明隔离、清理和可重复性成立。
- 对每种失败状态至少做一次受控演练，证明 `Product Failure`、`External Blocked`、`Not Run` 和 `Flaky` 能正确分类，且 waiver 只能放行规则允许的状态。
- 固化首份完整能力验收基线报告，后续 Release 与该报告按 Capability/Scenario 比较，而不是按测试数量比较。

**改动区域**

- 仅修复演练暴露出的测试、fixture、清理、报告和文档问题；不为了让测试变绿而删除 Scenario 或降低断言。
- `dist/test-results/` 或发布环境 Artifact Store：保存绑定候选 Commit 的两轮报告和诊断产物。

**依赖**

- 严格阻塞于第 13 步；需要全部测试账号、Secret、平台和发布执行环境可用。

**验证方法**

- 两轮报告的 Scenario 集合和策略完全相同；所有 Blocking Scenario 为 `Pass`，任何 Manual/External Blocked/Not Run/Flaky 都有当前 Commit 的显式结果或 waiver，且无静默重试。
- 执行前后检查数据库、进程、端口、容器、Kubernetes Namespace、消息、邮件和测试文件的清理结果。
- 检查最终 Git 工作树干净，报告与日志中没有 Secret 或个人数据。
- 使用最终报告回答“本次 Release 验证了哪些能力、每项由什么证据证明、还有没有未执行项”；任何问题无法回答都表示验收未完成。

## 并行与阻塞关系

- 第 1 步锁定 Manifest，阻塞第 2、3 步；第 2 步的 Schema/Test Ref 契约稳定后才能大规模补测试。
- 第 3 步 Gap Report 是第 6 至第 12 步的工作入口，避免各能力组重复建设已有测试。
- 第 4 步结果/隔离契约与第 5 步 Browser Runner、第 9 步外部 Runner 可以在第 2 步完成后并行；三者不得分别发明不同的 Run ID、状态或 Artifact 格式。
- 第 6、7、8 步在共享 System/Browser fixture 稳定后可以按 Capability 和测试文件边界并行；`test/capabilities.yaml`、System Harness 和 Browser 配置由单一变更串行维护。
- 第 10、11、12 步在第 9 步 Runner 合入后可以按集成目录并行；Live Target Registry、Secret 命名和最终报告聚合保持串行协调。
- 第 12 步的平台验证可与核心和外部协议补齐并行开发，但完成状态阻塞于真实 OS/Docker/Kubernetes 环境。
- 第 13 步只在所有能力组无缺口后接成最终入口；第 14 步是实施完成门，不能与未完成场景并行宣告通过。

## 整体验证闭环

1. **能力闭环**：最新 OpenAPI Operation、Web Route、CLI Command、内置 Plugin、Sandbox 和 System Skill 100% 映射或有明确非产品豁免。
2. **场景闭环**：C01-C19、X01-X19 的全部 Blocking Scenario 均有直接自动化 Test Ref；Nonblocking Backlog 和 Manual Requirement 单独报告，不伪装成 Blocking Pass。
3. **低层闭环**：`mise run test` 和严格 Contract Test 覆盖纯逻辑、数据库不变量、权限、协议及错误分支，不把这些重复堆到 Browser。
4. **进程闭环**：`mise run system-test` 使用真实 Binary、TCP、PostgreSQL、SSE 和 Worker，覆盖所有登记 Process Seam，并保留可诊断日志。
5. **浏览器闭环**：`mise run browser-test` 使用隔离 Chromium 和真实 Web/API/DB 链路覆盖全部登记关键用户旅程，失败保留 Trace。
6. **真实集成闭环**：`mise run live-smoke` 执行所有登记 Live Target；能够合规自动化的外部集成都有本次 Commit 的真实结果，X06/X13 等 Manual Requirement 有人工结果或 waiver。
7. **平台闭环**：CLI、Service、Sandbox、Archive、Docker、Helm、外部 PostgreSQL 和 OTLP 在声明支持的平台矩阵中全部有实际结果，Skip 不计为 Pass。
8. **发布闭环**：`mise run release:validate` 是唯一发布入口；Blocking Product Failure 令 `release_allowed=false`，允许豁免的状态进入 `needs_waiver`；日常 CI/CD 不运行本套件。
9. **安全闭环**：所有 Secret 来自受控环境，日志、Trace、截图和报告完成脱敏，测试账户为低权限专用账户。
10. **可重复闭环**：同一候选 Commit 连续两轮完整运行均通过，数据和外部资源无串扰、无残留，Git 工作树保持干净。
