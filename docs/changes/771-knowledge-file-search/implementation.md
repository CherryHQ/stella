# #771 Knowledge Base V1 实施计划

## 目标与方案基线

本计划用于落实同目录下的 [solution.md](./solution.md)。目标是在不引入独立 Knowledge Base 资源的前提下，交付一条完整、可验证的链路：

```text
管理员或用户按四级 scope 上传静态文件
→ PostgreSQL 保存原文件
→ River 异步调用 Xberg 解析并切块
→ pg_search 建立 BM25 可检索语料
→ Agent 根据提示词自行调用 knowledge.search
→ 模型使用返回片段回答并引用来源
```

实施完成后应同时具备：

- 四级 scope 一致的管理权限与运行时检索范围；
- PDF、DOCX、Markdown、TXT 的上传、解析、状态管理和删除；
- user、user_agent、system、system_agent 三类配额池的并发安全控制；
- `knowledge.search(query, limit?)` 单工具检索；
- Agent 页与设置页两个 Knowledge Base 管理入口；
- API、数据库、Worker、Agent 工具和 Web UI 的自动化验收证据。

## 全局约束

1. V1 只新增 `knowledge_file`、`knowledge_chunk` 两张业务表，不增加 Knowledge Base、目录、来源、版本、ACL、审批或同步实体。
2. 四级 scope 使用现有资源授权语义，不新增 Knowledge 专属角色：
   - `system`：`user_id IS NULL AND agent_id IS NULL`；
   - `system_agent`：`user_id IS NULL AND agent_id IS NOT NULL`；
   - `user`：`user_id IS NOT NULL AND agent_id IS NULL`；
   - `user_agent`：`user_id IS NOT NULL AND agent_id IS NOT NULL`。
3. 管理端和检索端都只信任 session 中的身份。API 不接受 owner `user_id`，Agent 工具不接受 scope、`user_id`、`agent_id` 或文档过滤参数。
4. 原文件存储在 PostgreSQL `BYTEA`，派生 chunk 使用 pg_search BM25；V1 不增加对象存储、embedding、hybrid search、rerank 或 load 工具。
5. 文件不可变；内容变化通过“上传新文件、确认 ready、删除旧文件”完成。
6. 上传、配额检查、`knowledge_file` 创建和 River `InsertTx` 必须位于同一个数据库事务。
7. OpenAPI、sqlc 和前端 API client 均遵循生成流程，不手改生成文件。
8. Web 端不轮询、不监听窗口聚焦、不提供手动刷新；解析状态只在进入页面、浏览器整页刷新或上传后的明确重新请求中更新。
9. 每一步都保持仓库可构建、可测试；不顺带实现飞书同步、外部来源、预览下载、替换、手动重试或批量 API。

## 对应阶段的开工门禁

这些问题不妨碍制定计划，但实施者不得自行猜测。未确认的项目只阻塞对应步骤，不阻塞前置工作。

| 门禁                                                   | 最晚确认时间               | 处理方式                                                            |
| ------------------------------------------------------ | -------------------------- | ------------------------------------------------------------------- |
| `knowledge.search` 的 query 最大字符数                 | 开始步骤 4 前              | 产品确认后同时固化到工具 schema、服务端校验和测试                   |
| River 专用队列名、退避参数和基础监控项                 | 开始步骤 2 的 River 接线前 | 按现有运维命名规范确认，不额外建设监控系统                          |
| 逻辑名 `knowledge.search` 到模型提供商合法函数名的映射 | 步骤 1 结束前              | 用当前 Agent 工具注册链路做最小验证，外部契约仍保持一个 search 工具 |
| Xberg v1.0.4 的真实二进制名、参数和 JSON schema        | 步骤 1 结束前              | 必须运行四类 fixture 实测，不能从旧 Kreuzberg CLI 猜测              |
| 面向用户的完整错误文案和日志保留周期                   | 步骤 5、6 前               | API reason 先按 solution 固定；展示文案和保留周期单独确认           |
| 真实文件问答集及验收指标                               | 步骤 6 前                  | 没有基准数据时不能以主观体验决定增加向量检索等复杂度                |

“是否增加恶意文件扫描”仍是独立产品决定。没有明确纳入 V1 前，本计划只落实文件类型、大小、解析资源和提示注入边界，不隐式扩展为安全扫描项目。

## 实施步骤

### 步骤 1：锁定 Xberg 和 Agent 工具的外部技术契约

**预期结果**

在业务表和 API 接入前，先消除两个高风险假设：

- Stella 能稳定安装并调用固定版本 Xberg v1.0.4；
- mise 保留官方 Linux GNU 压缩包中的可执行文件、相邻动态库和许可证文件，不做单文件复制；
- PDF、DOCX、Markdown、TXT fixture 能得到有序 chunk 和可规范化 locator；
- `chunk_size = 1000`、`overlap = 200`、关闭 OCR、关闭缓存等参数确实由 Stella 显式控制；
- 逻辑工具 `knowledge.search` 能映射为当前模型提供商接受的单个只读工具，不引入 action 数组或第二个工具。

**修改范围**

- 在 `resources/tools.yaml` 中固定 Xberg 版本，并根据实测结果修正资产匹配和安装后的二进制名。
- 删除 Stella 私有 `libheif` 兼容资产和 `LD_LIBRARY_PATH` 注入，只为 Deb/RPM 声明官方 GNU 资产仍依赖的系统 C++ runtime。
- 新建 `internal/knowledge` 包中的 Xberg adapter，只负责：
  - 创建和清理受控临时文件；
  - 以明确参数启动子进程；
  - 有界读取 stdout、stderr；
  - 校验 JSON schema；
  - 输出稳定的内部 chunk DTO。
- 在 `internal/knowledge/testdata` 放置最小 PDF、DOCX、Markdown、TXT fixtures，以及必要的无文字或损坏样本。
- 如果 Xberg 的真实二进制名与现有 `internal/agent/sandbox/read.go`、系统 skill 中的旧 Kreuzberg 调用冲突，只在实测后统一兼容路径；不能把旧参数机械改名。
- 用当前 `agent.BuiltinTool` 注册机制写一个最小 schema 测试，确认最终 wire name；该测试不接数据库，也不提前实现搜索逻辑。

**实现边界**

- adapter 不返回完整提取文档，只返回入库所需的 chunk、顺序和 locator。
- 不复用对话附件读取工具的文本返回协议。
- locator 只规范化 `first_page`、`last_page`、`heading_context` 和内部 byte offsets；工具输出阶段再移除内部 offsets。
- 所有进程输出必须有上限，错误不得包含正文、完整 stderr、临时路径或堆栈。

**依赖**

- 无，可与步骤 2 的数据库迁移设计并行推进。

**验证**

- 四类支持格式 fixture 均验证 chunk 顺序、正文、页码或标题。
- 扫描/无文字 PDF、损坏 DOCX、非法 JSON 和超限输出具有确定性失败结果。
- 测试确认每次调用都显式携带切块和资源边界参数。
- 通过受管工具安装流程验证版本、资产和实际命令，而不依赖开发机全局安装。
- 工具 schema 能被当前所有启用的模型提供商接受，且模型侧只看到 query 和 limit。

### 步骤 2：完成存储、配额与异步解析的内部闭环

**预期结果**

不依赖 HTTP 和 Web UI，内部服务测试可以完成：

```text
创建 processing 文件并原子入队
→ Worker 从 BYTEA 解析
→ 全量写入 chunks
→ 文件原子进入 ready 或 failed
→ 删除后原文件和 chunks 均消失
```

同时确保并发上传不能突破配额，Worker 重复执行或晚到时不会恢复已删除数据。

**修改范围**

1. 数据库迁移

   - 在 `internal/db/migrations` 新增手写迁移，只创建 `knowledge_file` 和 `knowledge_chunk`。
   - `knowledge_file` 使用 UUID 主键，保存 owner tuple、文件元数据、`raw_content BYTEA`、状态、精简错误和时间戳。
   - 用数据库 CHECK 同时约束四级 owner tuple、三种状态，以及 failed/error 的合法组合。
   - `knowledge_chunk` 保存 UUID 主键、`file_id`、`ordinal`、`content` 和 `locator JSONB`。
   - 增加 `UNIQUE(file_id, ordinal)`，文件外键级联删除。
   - 为 owner tuple、固定的 `created_at DESC, id DESC` 列表顺序及外键增加必要 B-tree 索引。
   - 为 chunk content 创建以 chunk id 为 key field、jieba tokenizer、空白 stopwords 的 pg_search BM25 索引。

2. sqlc 查询与内部服务

   - 在 `internal/db/queries` 增加文件创建、锁定、状态转换、批量 chunk 写入、删除、配额统计和 reconciliation 所需查询。
   - 由 sqlc 生成类型安全代码，不手改 `internal/db/sqlcgen` 产物。
   - 在 `internal/knowledge` 中实现文件服务、存储层、配额规则和 River job 参数。
   - 文件名只保存安全 basename；媒体类型由服务端结合扩展名和文件内容确定，不能信任 multipart 声明值。
   - PDF、DOCX、Markdown、TXT 使用同一个集中校验入口；DOCX 要验证 ZIP 容器结构，文本格式要验证 UTF-8。

3. 并发安全配额

   - 不新增配额表。
   - 在创建事务内，以 quota pool 生成稳定、带命名空间的 PostgreSQL transaction advisory lock：
     - system 使用单一全局 key；
     - system_agent 按 `agent_id`；
     - 个人知识按 `user_id`，合计 user 与全部 user_agent。
   - 获得锁后，在同一事务中统计 processing、ready、failed 的文件数和 `size_bytes`，再执行配额判断、文件插入和 River `InsertTx`。
   - 删除仍按 owner tuple 授权，但提交删除后立即自然释放额度。

4. River Worker 与 reconciliation

   - 在现有共享 River client 中注册 Knowledge 专用队列，初始每节点并发 2。
   - job 参数仅包含 `file_id`，最大尝试次数 3，并对仍可能执行的状态启用同一 `file_id` 唯一性。
   - 每次尝试从 `raw_content` 重新创建临时文件；单次 context 超时初始为 5 分钟。
   - Worker 提交前锁定文件行并再次确认 `status = processing`。
   - 成功时在一个短事务内完成“清除旧派生物、批量写入全部 chunks、状态改为 ready、`JobCompleteTx`”。
   - 确定性失败时在一个事务内保证无 chunks、状态改为 failed、写入不超过 1 KiB 的清理错误并 `JobCompleteTx`。
   - 可重试错误直接返回 River；文件不存在或已终态时成功 no-op。
   - 使用 River 公共 job 查询能力实现周期性 reconciliation，不直接依赖 `river_job` 私有表结构：
     - processing 且仍有可继续执行 job：不处理；
     - processing 且没有有效 job、仍可重试：重新入队；
     - job 已耗尽或不可恢复：收敛为 failed。

5. 解析资源边界

   - 原文件领域上限固定为 25 MiB。
   - 初始实现限制：stdout JSON 128 MiB、正文 32 MiB、chunk 正文合计 48 MiB、单文档 50,000 chunks。
   - 任一上限超过时整份文件 failed，不截断后继续入库。

**依赖**

- 数据库部分可与步骤 1 并行。
- Worker 完整接线依赖步骤 1 的 Xberg adapter 和 River 门禁确认。

**验证**

- `mise run db:validate` 验证迁移可正向、反向执行且 schema 符合约束。
- 数据库集成测试覆盖四种合法 tuple 和所有非法 tuple。
- 并发创建测试证明同一 quota pool 不会共同越限；不同 Agent 的 system_agent 池互不占用，user 与 user_agent 共用个人池。
- 事务故障注入证明文件插入或 River 插入任一失败时两者都回滚。
- 四类 fixture 完成 `processing → ready`，空文档、损坏文件和解析产物超限完成 `processing → failed`。
- 重复 job、删除与 Worker 提交竞争、已终态 job、数据库暂时故障和重试耗尽均满足幂等规则。
- reconciliation 测试证明异常退出后不存在永久 processing。
- 删除测试证明 raw content、chunks 和 BM25 命中立即消失。

### 步骤 3：交付 Knowledge File 管理 API

**预期结果**

四个 scope 都可通过统一顶层 `/api/knowledge-files` 资源完成上传、列表、元数据查询和删除；API 权限、分页、配额和错误语义与 solution 一致。

**修改范围**

1. OpenAPI-first 合同

   - 在 `api/spec/domain/knowledge-files` 增加 schema 和 path 定义，并在 `api/spec/openapi.yaml` 引用。
   - 定义四个 operation：
     - `GET /api/knowledge-files`；
     - `POST /api/knowledge-files`；
     - `GET /api/knowledge-files/{id}`；
     - `DELETE /api/knowledge-files/{id}`。
   - 固化 `KnowledgeFile`、list response、quota 和结构化错误 details。
   - 运行生成流程更新 Go server 接口和 Web client。

2. 统一 scope 授权

   - 将 `internal/server/skills_manage.go` 中已有的 scope/owner/Agent access 判断提取成资源中立的内部 helper。
   - Skills 和 Knowledge 同时使用该 helper，并用现有 Skills 测试保证重构前后授权语义不变。
   - `user_id` 始终来自 session；管理员也不能管理其他用户的 user 或 user_agent。
   - List/Create 对明确但无权的 scope 返回 403；Get/Delete 对不存在或不可管理的 id 都返回 404。

3. Create

   - 顺序必须是 session → scope/agent 参数 → scope 权限 → Agent access → multipart。
   - 鉴权通过后再用有界流读取单个 `file` part，不先调用会读取完整请求体的通用 multipart 解析。
   - multipart 不接受第二个业务字段；scope 和 agent_id 只从 query 获取。
   - 调用步骤 2 的内部事务服务，成功返回 201 和完整 `KnowledgeFile`。
   - 400 reason 至少区分 `file_too_large`、`unsupported_file_type`；配额满返回 429 `quota_exceeded` 和当前 quota。

4. List/Get/Delete

   - List 固定按 `created_at DESC, id DESC` 使用 keyset cursor。
   - 使用 Knowledge 专用不透明 token，编码游标位置及 scope、agent_id、规范化 q 的绑定信息；跨条件复用返回 400，不能退化为不绑定过滤条件的通用 offset token。
   - q 去首尾空白、最多 200 字符，对 `file_name` 做忽略大小写的字面包含匹配；必须转义 SQL wildcard。
   - quota 独立于 q 和分页统计。
   - Get 不返回 `raw_content`、chunk 或内部身份字段。
   - Delete 删除任意状态并返回 204；重复删除返回 404。

5. 服务接线

   - Server 只把可信 Authority 和参数交给步骤 2 的 Knowledge service，不在 handler 重写 scope、Agent access、存储、配额或 River 规则。
   - `cmd/stellad` 在共享数据库和 River composition root 中创建一次服务，并同时提供给 HTTP 与 Agent 工具。

**依赖**

- 依赖步骤 2 的内部服务和 River job。
- OpenAPI schema 定稿后，步骤 5 的 Web 查询层可与步骤 4 并行开发。

**验证**

- OpenAPI lint/generate/check 均通过，生成文件没有手工改动。
- 使用真实 PostgreSQL 的 server 集成测试覆盖普通用户、管理员、四级 scope 和 Agent access 矩阵。
- 用一个故意超大的未授权 multipart 请求验证：服务端在读取文件体前返回权限错误。
- 参数组合测试覆盖缺失 scope、非法 scope、应有/不应有 agent_id、q/page_size 边界。
- token 测试覆盖稳定翻页、同时间戳 tie-break、篡改 token、跨 scope/Agent/q 复用。
- Create、Get、Delete 及 400/401/403/404/429/500 的状态码和结构化 reason 与合同一致。
- API 级端到端测试验证上传后为 processing，Worker 完成后刷新得到 ready/failed，删除后配额立即下降。

### 步骤 4：交付 Agent BM25 检索、触发提示与引用

**预期结果**

带可信 `user_id`、当前 `agent_id` 且属于私有 `main/chat` session 的 Agent 可以自行调用单个只读工具，从四级并集内获得全局 BM25 Top K；该合同不按 Web、PAT/OAuth、聊天适配器或 Webhook channel 区分。群聊及 `task`、`scheduler`、`delegate` 后台 session 得不到 Knowledge。

**修改范围**

1. 单 SQL 检索

   - 在 sqlc 查询中同时完成：
     - pg_search BM25 content 匹配；
     - `knowledge_file.status = ready`；
     - `system ∪ system_agent(A) ∪ user(U) ∪ user_agent(U,A)`；
     - score 降序和稳定 tie-break；
     - 最终 limit。
   - 禁止先查全库再在 Go 中做权限过滤，也不按四个 scope 分四次查询。
   - query 先做空白和标点规范化；无有效文字或数字时直接返回空结果。
   - 按已确认门禁增加最大字符校验；limit 默认 5、范围 1～10。

2. Agent 工具

   - 将 Knowledge search 注册到现有 built-in tool 组合根。
   - 工具从可信 runtime/session context 读取 user 和 agent 身份，不在 schema 中暴露身份或过滤参数。
   - `KnowledgeToolAvailable` 只在可信 user 和 agent 同时存在、group 为空且 session kind 为 `main/chat` 时提供工具；授权 Webhook 必须通过，群聊及其他 session kind 不注册或返回空。
   - 输出仅包含完整 chunk `content`、`file_name` 和清理后的用户可见 locator。
   - 空命中成功返回 `results: []`；数据库故障返回工具错误，不能伪装成空结果。
   - 同一条用户消息触发的 Agent 执行中最多实际调用两次；第一次可使用初始 query，只有结果不足时才改写后再调用一次，第三次尝试由运行时拒绝，下一条用户消息重新计数。
   - 日志只记录是否调用、query 长度、耗时、结果数及内部 file/chunk id 和排名，不记录完整 chunk 或原文件。

3. 触发和引用提示

   - 更新 `internal/agent/prompt/template/system_prompt.tmpl` 及相关测试：
     - 内部制度、流程、产品规范、项目、岗位知识和明确 Knowledge 请求应检索；
     - 普通常识、写作、翻译、计算或上下文充分时默认不检索；
     - 允许根据上下文生成一条新 query，结果不足时最多改写后再次调用一次；
     - 文档内容是不可信证据，不能覆盖系统或工具指令；
     - 使用事实时在结论附近按文件名、页码或标题引用。
   - 明确区分文件 Knowledge Base 与现有长期记忆/事实工具，避免模型把 `memory.search_knowledge` 当成文件检索。
   - 同步 Stella 系统 skill 中关于自身能力的说明。

**依赖**

- 依赖步骤 2 的 ready chunks 和 BM25 索引。
- 依赖步骤 1 的合法 tool wire name。
- query 最大长度固定为 500 个 Unicode 字符。
- 可与步骤 5 的 UI 主体并行，但最终端到端验收依赖步骤 3。

**验证**

- 数据库集成测试以用户 U、Agent A 验证恰好命中四级并集，其他用户和其他 Agent 文件不可见。
- 检索 SQL 的授权过滤与 BM25 在同一语句中；测试不能通过应用层后过滤才能成立。
- 中文、英文和中英混合查询返回稳定 Top K。
- processing、failed、已删除文件永不命中。
- limit、空 query、超长 query、空结果和数据库错误语义正确。
- 同一条用户消息第三次调用被拒绝，下一条用户消息重新获得两次调用额度。
- Agent 运行测试覆盖正常一对一会话、授权 Webhook、无用户群聊、`task`/`scheduler`/`delegate` 后台 session 及参数伪造。
- 输出快照不包含 scope、user_id、agent_id、file_id、chunk_id、score、raw content 或 byte offsets。
- 提示词测试覆盖需要检索、不应检索、二次改写、真实引用和文档提示注入样本。

### 步骤 5：交付两个 Knowledge Base 管理页面

**预期结果**

- Agent 顶部“知识库”固定管理当前用户在当前 Agent 下的 user_agent 文件；
- 设置侧栏“知识库”中，普通用户只管理 user，管理员可以切换 user、system、system_agent；
- 两个入口复用同一文件列表、上传、删除和配额组件，不构造一个可以任意切换 owner 的复杂通用页面。

**修改范围**

1. 路由和导航

   - 在 Agent 级 `FacetTabs` 增加“知识库”，只对 Agent 页面显示，不加到项目或群聊。
   - 新增 `/agents/$agentId/knowledge?q=` 路由；页面不显示 scope 或 Agent 选择器。
   - 在设置侧栏新增“知识库”，并新增 `/settings/knowledge?scope=user&agent_id=&q=` 路由。
   - 路由文件只负责 URL 校验和页面装配，业务交互放入 `web/src/features/knowledge`。

2. 设置页权限状态

   - 普通用户始终规范化为 `scope=user`，不渲染单项 scope 选择器。
   - 管理员显示 user、system、system_agent 三个互斥视图。
   - system_agent 必须显式选择 Agent；未选择时显示空状态并禁用上传。
   - Agent 下拉使用管理员可见的完整 Agent 查询，查询 key 与普通可访问 Agent 列表分开。
   - 切换 scope 或 Agent 时清空 q 和已加载页；scope、agent_id、q 保留在 URL，page token 不进入 URL。

3. 查询与表格

   - 基于生成 client 建立 TanStack infinite query；query key 包含 scope、agent_id、q。
   - 页面请求每次 50 条，使用“加载更多”。
   - 明确设置不轮询且 `refetchOnWindowFocus: false`。
   - 头部只显示文件数和原文件字节配额文字，不加进度条或详情页。
   - 表格只显示文件、状态、大小、创建时间和删除操作。
   - failed 行显示清理错误；存在 processing 时提示浏览器刷新查看状态。

4. 上传和删除

   - 上传弹窗支持拖拽和多选，但逐文件调用单文件 API，受控并发为 3。
   - 前端预检扩展名、媒体类型和 25 MiB；服务端错误仍为最终依据。
   - 每个文件独立展示成功或失败，部分失败不回滚成功项。
   - 上传批次结束后重新获取第一页和 quota。
   - 删除任意状态前使用 AlertDialog 确认；成功后立即从本地缓存移除行并扣减文件数和字节。

5. 设计与文案

   - 使用 CossUI 现有 Table、Dialog、AlertDialog、Button、Input、Select/Combobox、Toast 和 Empty State。
   - Knowledge 四级 scope 是叠加语料，不展示技能页面的“优先级”说明或任何权重暗示。
   - 所有新增文案同步中英文 i18n。
   - 文件名不可点击，不增加预览、下载、批量操作、状态筛选或排序控件。

**依赖**

- 依赖步骤 3 的 OpenAPI 合同和生成 client。
- 页面主体可与步骤 4 并行；processing → ready 的浏览器验收依赖步骤 2。

**验证**

- 单元/组件测试覆盖普通用户与管理员的 scope 视图、URL 规范化和 system_agent 未选 Agent。
- 查询测试覆盖 query key 隔离、切换清页、加载更多、无焦点刷新和无轮询。
- 上传测试覆盖并发上限 3、部分失败、超限预检和结束后重取第一页。
- 删除测试覆盖确认、本地立即移除、配额扣减和失败回滚提示。
- 浏览器 E2E 覆盖：
  - 普通用户 Agent 页 user_agent；
  - 普通用户设置页 user；
  - 管理员设置页 system 和指定 system_agent；
  - URL 伪造、无权限 Agent、空状态、processing、ready、failed；
  - 桌面与窄屏、浅色与深色主题。

### 步骤 6：补齐文档、真实评测和发布验收

**预期结果**

交付物不仅“能运行”，还具有可复现的召回、触发、引用、安全和资源边界证据；后续是否增加 embedding 等复杂度有数据依据。

**修改范围**

- 增加中英文用户文档，说明：
  - 两个页面入口与四级 scope；
  - 支持格式、25 MiB 限制和配额；
  - processing/ready/failed、浏览器刷新和删除重传；
  - V1 无预览、下载、替换、手动重试、飞书同步。
- 更新系统能力文档，避免把文件 Knowledge Base 与记忆、会话附件或旧 Kreuzberg 能力混称。
- 建立不进入生产数据的真实样本评测集，至少覆盖 PDF、DOCX、Markdown、TXT、中文、英文、混合查询、重复/冲突文件和提示注入。
- 对每个问答样本分别记录：
  - 是否触发工具；
  - 正确 chunk 是否进入 Top K；
  - 模型是否使用正确证据；
  - 引用是否真实；
  - 解析和检索延迟。
- 根据已确认指标判断 V1 是否达到发布门槛；只记录失败类型，不在本步骤顺带增加 embedding、rerank、邻块扩展或 query 数组。

**依赖**

- 依赖步骤 1～5。
- 真实问答集和指标必须由产品/mentor 在本步骤前确认。

**验证**

- 文档中英文页面均可构建，链接和导航有效。
- 真实样本结果能够区分触发失败、召回失败、切块失败和生成失败。
- 提示注入样本不能改变系统或工具行为。
- 25 MiB、配额上限、解析超时和产物上限具备自动化测试或受控压测证据。
- 审阅普通日志，确认没有原文件、完整 chunk、临时路径或未清理 stderr。

## 依赖与并行关系

```text
步骤 1：Xberg / tool contract ───────┐
                                     ├─→ 步骤 2：存储与异步解析 ─→ 步骤 3：管理 API ─┐
步骤 2 的数据库迁移设计可并行开始 ────┘                                      │
                                                                              ├─→ 步骤 6
步骤 2 ─────────────────────────────────────→ 步骤 4：Agent 检索 ──────────────┤
步骤 3 ─────────────────────────────────────→ 步骤 5：管理 UI ────────────────┘
```

- 步骤 1 和步骤 2 的数据库部分可以并行，但 Worker 接线必须等待 Xberg 实测。
- 步骤 4 与步骤 5 可以并行：前者主要修改 Go、SQL 和 prompt，后者主要修改 Web；两者共享的稳定边界是步骤 3 生成后的 API 合同。
- 步骤 6 是发布门禁，不应与尚未稳定的合同并行完成。

## 整体验收

### 功能链路

1. 普通用户在 Agent 页上传 user_agent 文件，刷新后看到 ready，并能在该 Agent 对话中检索引用。
2. 同一用户换到其他 Agent 时不能读取上述 user_agent 文件。
3. 普通用户在设置页上传 user 文件，能够在其有可信身份的所有 Agent 中检索。
4. 管理员上传 system 文件后，所有有可信用户会话的 Agent 可检索；上传 system_agent 后仅目标 Agent 可检索。
5. 使用 `agent:write` PAT 调用 Webhook 时，Agent 能按调用者的可信 user_id 和当前 agent_id 检索四级并集。
6. 没有可信 Stella 用户的会话、群聊和 `task`、`scheduler`、`delegate` 后台 session 无法获得四级 scope 中的任何内容。
7. 删除文件后，管理列表、配额和检索结果立即一致地移除。

### 安全与一致性

- 数据库 CHECK、API 授权和检索 SQL 三层使用同一四级 scope 语义。
- 未授权上传在读取请求体前被拒绝。
- API 和工具均不能通过客户端参数伪造 owner。
- Get/Delete 的 404 不泄露其他 owner 文件是否存在。
- 配额并发测试、Worker 幂等测试和删除竞争测试通过。
- 工具输出及普通日志不泄露原文件、内部身份、数据库 ID 和分数。

### 仓库级命令

按仓库任务定义执行等价的完整验证，至少包括：

```bash
mise run db:validate
mise run generate
mise run generate:check
mise run format
mise run build
mise run test
```

Web 侧额外执行项目现有的 lint/typecheck、组件测试和浏览器 E2E；API 集成测试必须使用真实 PostgreSQL，不能用 mock 替代 scope、BM25、事务或 River 行为。

### 最终 diff 审查

- 只有两张新增业务表，没有隐藏加入 Knowledge Base、来源、版本、ACL 或同步实体。
- 没有手改生成代码。
- 没有把 Skills 的 scope 优先级带入 Knowledge 检索或 UI。
- 没有加入轮询、下载、预览、Replace、Retry、飞书同步、embedding 或 load 工具。
- 中英文文档、prompt、系统 skill、OpenAPI 和实际行为保持一致。
