# #771 Knowledge Base V1 方案

## 文档状态

本文是 Stella Knowledge Base V1 的当前方案基线，取代 issue 正文中已标记为废弃的旧方案。

方案依据 [issue #771 的 V1 scope 建议](https://github.com/CherryHQ/stella/issues/771#issuecomment-5043121252) 收缩为：**带四级 scope 的静态文件知识检索**，并纳入 [实施合同补充](https://github.com/CherryHQ/stella/issues/771#issuecomment-5045230802)。本文记录已经确认的领域、存储、解析、权限、RAG、管理 UI 和 OpenAPI 行为；具体实现任务留待后续拆解。

## 问题与目标

Stella 未来同时服务两类 Agent：

- 员工数字分身：拥有某个员工上下文、可以在其缺席时协助沟通的 Agent。
- 岗位数字员工：承担某项企业职能或工作流的 Agent，例如财务 Agent。

两类 Agent 都需要在回答企业问题时按需使用正式纳入 Stella 的文件知识。V1 的目标是用尽可能薄的模型证明以下完整闭环：

```text
授权用户在 Knowledge Base 页面上传文件
→ 后台异步解析和切块
→ 文件变为 ready
→ Agent 在当前 user / Agent 可见 scope 内执行 BM25 检索
→ 返回有限的完整 chunks 和引用
→ 模型基于证据回答
→ 文件删除后立即不可检索
```

Knowledge Base 只负责为 Agent 提供 RAG，不承载 Agent 的全部上下文、长期记忆、工具权限或企业授权框架。

## 范围与非目标

### V1 范围

- 复用 Stella 已有的四级 scope：**system**、**system_agent**、**user**、**user_agent**。
- Knowledge Base 页面中的静态文件列表、上传、删除和 **processing / ready / failed** 状态。
- 第一批格式：文本型 PDF、DOCX、Markdown 和纯文本。
- 单文件 25 MiB 硬限制。
- PostgreSQL 保存原文件和解析后的 chunks。
- shared River 异步调用受管的 **Xberg v1.0.0-rc.35** 完成抽取和切块。
- 使用现有 **pg_search BM25** 完成中文和中英文混合检索。
- 一个只读 Agent 工具：**knowledge.search(query, limit)**。
- 模型根据提示词自行决定是否检索，并在使用知识时给出文件名和页码或标题引用。

### 明确不进入 V1

- 不建立 **KnowledgeBase**、**KnowledgeSource**、**Revision**、ACL 或规范化全文实体。
- 不采用“一 Agent 一个 Knowledge Base”的资源拓扑；Knowledge Base 是产品能力和页面名称，不是数据库实体。
- 不接入飞书或其他外部来源，不做审批、候选扫描、定时同步、撤权检查或来源适配器。
- 不提供对话附件或“上传知识”入口；普通聊天文件不会自动入库。
- 不提供文件更新或替换 API，不保存替换关系、版本或回滚；内容变更时先上传新文件，待其 ready 后再删除旧文件。
- 不支持指定文档、document_id/file_ids 检索过滤、文档选择器或文档打开能力。
- 不提供独立 **load/fetch** 工具，也不向模型暴露原始文件。
- 不支持 OCR、扫描 PDF、图片理解、图表理解、复杂表格语义或压缩包。
- 不实现 embedding、hybrid search、rerank、查询数组、后端多查询扩展或自动 RAG 预检索。
- 不实现文档级、chunk 级或回答级 ACL。

## 约束与已知事实

### 四级 scope 已有统一语义

Knowledge 文件完整复用 MCP / Skills 的 scope 词汇、owner tuple 和授权语义，不建立 Knowledge 专用的角色或 ACL。运行时可见内容是四个 scope 的**叠加语料**，不采用同名资源覆盖优先级。

这里的 **user_id** 表示知识资源所属用户，不是 Agent 创建者，也不引入“Agent 管理者”作为新的 Knowledge Base 角色。

### 当前没有独立 Knowledge Base 资源

V1 直接以 **knowledge_file** 作为管理和授权对象。页面中的“Knowledge Base”表示当前操作者可以管理或当前会话可以检索的文件语料集合。

因此：

- 一个 Agent 不拥有独立 KnowledgeBase 记录。
- 同一文件只属于一个 scope owner tuple。
- 一次检索将当前用户和当前执行 Agent 可见的四层文件合并查询。

### 已有基础设施

Stella 当前已有：

- 强制依赖的 PostgreSQL **pg_search** 和 jieba BM25 索引模式；
- shared River 后台任务客户端；
- 来源已迁移到 `xberg-io/xberg`、但本地注册名和调用方仍使用旧 `kreuzberg` 名称的受管文档解析工具。

Knowledge V1 复用这些能力，但实施前必须把受管二进制统一修正为 **xberg v1.0.0-rc.35**，并为解析任务增加独立队列、并发和超时边界。现有 Agent 临时读文件的文本接口不是完整的 Knowledge 入库适配器，不能直接复用其返回值作为最终数据协议。

## 最终方案

### 1. Scope 与授权

四级 owner tuple 如下：

| scope            | user_id  | agent_id   | 上传与管理 | 运行时可见范围         |
| ---------------- | -------- | ---------- | ---------- | ---------------------- |
| **system**       | NULL     | NULL       | 管理员     | 当前系统的所有 Agent   |
| **system_agent** | NULL     | 指定 Agent | 管理员     | 指定 Agent             |
| **user**         | 当前用户 | NULL       | 当前用户   | 该用户使用的所有 Agent |
| **user_agent**   | 当前用户 | 指定 Agent | 当前用户   | 该用户使用指定 Agent   |

数据库必须用 CHECK 约束保证 scope 与 user_id / agent_id 的组合严格匹配。

HTTP 层只把可信 session 身份传给 Knowledge domain，不在 handler 中判断资源权限。Knowledge domain 按与 MCP / Skills 一致的四级 scope 语义完成管理授权：

- **system**、**system_agent** 仅管理员可管理；**user**、**user_agent** 的 user_id 固定为可信 session 的当前用户，客户端不能指定其他 owner。
- 任何带 agent_id 的 **system_agent**、**user_agent** 请求都必须委托现有 Agent domain 检查读权限，不能只验证 owner tuple。
- V1 不引入 Agent 管理者、Knowledge Base 管理者或文件 ACL，也不增加新的角色类型。
- 所有上传必须先完成 scope 授权和 Agent access 检查，再读取或解析请求体。

### 2. 运行时检索身份

一次 Agent 检索使用可信 session 中的两个当前身份：

- **user_id**：当前 session 关联的 Stella 用户。
- **agent_id**：当前 session 中执行 **knowledge.search** 的 Agent。

用户 U 与 Agent A 的可见文件集合为：

```text
system
∪ system_agent(A)
∪ user(U)
∪ user_agent(U, A)
```

安全边界：

- user_id 和 agent_id 只能来自可信 session，工具参数不能接受或覆盖它们。
- 当前说话人没有关联 Stella 用户时，不加载任何 Knowledge，包括 system 和 system_agent。
- 群聊仅能执行 Agent 不代表获得 Knowledge 读取能力；没有可信用户身份的群聊检索结果为空。
- 无真人发起者的后台任务同样不能读取 Knowledge。先前“后台任务可读企业两层”的假设已经废弃。

对数字分身的含义：

- 希望其他员工询问数字分身时也能获得的共享上下文，应放入 **system_agent**。
- 只属于某个员工本人使用的私人知识，应放入 **user** 或 **user_agent**。

### 3. 数据模型

V1 只新增两张核心表。

#### knowledge_file

至少包含：

| 字段                    | 语义                             |
| ----------------------- | -------------------------------- |
| id                      | 文件主键                         |
| scope                   | 四级 scope                       |
| user_id                 | nullable owner user              |
| agent_id                | nullable target Agent            |
| file_name               | 安全展示文件名                   |
| media_type              | 已验证的媒体类型                 |
| size_bytes              | 原始字节数                       |
| raw_content             | PostgreSQL BYTEA 原文件          |
| status                  | processing / ready / failed      |
| error                   | nullable、经过清理的简短失败信息 |
| created_at / updated_at | 时间戳                           |

不保留 **uploaded_by_user_id**。管理授权已经由 owner tuple 与当前操作者确定，V1 不额外建设上传审计模型。

文件不可变。V1 不提供更新或替换能力，也不保留版本或替换关系。需要变更内容时，先上传一个新的独立文件；旧文件在新文件 ready 前保持可检索，新文件解析失败也不影响旧文件。

#### knowledge_chunk

至少包含：

| 字段    | 语义                                |
| ------- | ----------------------------------- |
| id      | chunk 主键，同时作为 BM25 key field |
| file_id | 关联 knowledge_file，删除级联       |
| ordinal | 文件内稳定顺序                      |
| content | 完整 chunk 文本                     |
| locator | JSONB 格式定位信息                  |

约束与索引：

- UNIQUE(file_id, ordinal)。
- file_id 外键使用级联删除。
- content 建立 pg_search BM25 索引，使用 jieba tokenizer 和空白 stopwords。
- 不保存规范化全文、embedding 或独立页面/章节实体。

locator 可保存解析阶段取得的 **first_page**、**last_page**、**heading_context** 和内部 byte offsets。检索工具只向模型返回页码和标题，不暴露 byte offsets。

### 4. 上传与任务创建

上传流程的领域行为为：

```text
鉴权 scope owner tuple
→ 有界读取上传体并验证 25 MiB 限制
→ 检查对应 scope 配额
→ 在同一事务中写入 processing knowledge_file，并通过 River InsertTx 写入 job
→ 提交事务
→ 后台异步解析
```

要求：

- 未授权请求不能先读取完整文件再拒绝。
- 文件和 River job 必须通过 **InsertTx** 原子创建，避免文件永久停留在 processing 且没有任务。
- River job 参数只包含 **file_id**，不复制原文件或可信身份。
- River job 按参数去重，唯一性范围覆盖仍可能执行的 retryable 状态；重复投递同一 file_id 不得产生并行解析。
- 上传 API 本身不幂等：每次成功上传都创建新的 file_id，即使文件内容和名称完全相同。后台解析的幂等保证只针对同一个 file_id。

#### 文件内容变更

V1 将内容变更建模为两个独立文件的上传和删除，不提供 Replace API、原子替换或幂等更新接口：

1. 在与旧文件相同的 scope 上传新文件。
2. 新文件处于 processing 时，旧文件继续保持 ready 并参与检索。
3. 新文件变为 ready 后，由用户删除旧文件。
4. 新文件变为 failed 时，由用户删除失败的新文件；旧文件状态和检索不受影响。

过渡期内新旧文件可以同时被检索，可能产生重复或冲突结果。两份文件均按各自的状态和 size_bytes 正常占用配额，不为内容变更预留额度，也不提前扣除旧文件占用。Knowledge Worker 只处理其 job 参数中的 file_id，不删除、切换或修改另一个文件。

#### Scope 容量配额

V1 不设置整个 Stella 实例的独立配额。配额分别按 system 全局、system_agent 的 agent_id 和个人知识的 user_id 统计：

| 配额池           | 统计范围                                                              |            文件数 |      原文件总字节 |
| ---------------- | --------------------------------------------------------------------- | ----------------: | ----------------: |
| **system**       | 所有 system 文件                                                      |         **4,000** |        **20 GiB** |
| **system_agent** | 每个 agent_id 的 system_agent 文件                                    | **1,000 / Agent** | **5 GiB / Agent** |
| **个人知识**     | 每个 user_id 的全部 user 和 user_agent 文件；统计时不按 agent_id 分裂 |         **2,000** |        **10 GiB** |

system_agent 的配额与完整 owner tuple 一致，每个 Agent 互不占用额度。个人知识则有意合并 user 和 user_agent，避免用户使用多个 Agent 时重复获得额度。以上规则只影响配额统计；文件授权、管理和检索仍严格使用完整 owner tuple。

配额行为：

- processing、ready、failed 文件全部计入文件数和原文件总字节数，因为三种状态都持有 raw_content。
- 原文件总字节数使用 knowledge_file.size_bytes 统计；派生 chunks 继续由单文件解析产物上限约束。
- 内容变更过渡期的新旧文件同时计入配额；旧文件仍存在时，新文件上传必须在剩余额度内通过正常配额检查。
- 删除文件后立即释放对应配额池的额度。
- 配额检查必须与 knowledge_file 和 River job 的创建原子执行；并发上传不能共同越过限制。

若用户数为 U、Agent 数为 A，整个实例的理论上限为：

```text
system：4,000 个文件、20 GiB
+ A × system_agent：每 Agent 1,000 个文件、5 GiB
+ U × 个人知识：每用户 2,000 个文件、10 GiB

当 U = 100、A = 100 时：
合计最多 304,000 个文件、1,520 GiB 原文件

每增加一个 Agent，再增加 1,000 个文件、5 GiB 上限
```

管理页面如何展示这些配额，以及管理 API 如何返回配额，分别见第 14、15 节。

### 5. 支持格式

V1 支持：

- 文本层 PDF；
- DOCX；
- **.md / .markdown**；
- TXT。

明确不支持：

- 扫描 PDF 和 OCR；
- 加密或无法提取正文的 PDF；
- **.doc、.pptx、.xlsx、HTML、CSV**；
- 图片、图表和嵌入图片中的文字；
- 压缩包；
- 对复杂表格关系的可靠问答。

包含少量表格的文档可以提取可见文本，但 V1 不承诺保留复杂表结构和单元格语义。解析结果没有任何可用文字时必须失败，不能把空文档标记为 ready。

### 6. 解析与切块

Knowledge Worker 将原始 BYTEA 写入受控临时文件后，调用受管的 **xberg v1.0.0-rc.35**。实施不能沿用旧 `kreuzberg` 命令，也不能在尚未验证时把旧 CLI 参数直接改名后使用。

Linux GNU 资产由 Xberg 官方在可执行文件旁提供非系统动态库，并通过 `$ORIGIN` 解析。Stella 的 mise 安装必须保留整个官方压缩包，不得只复制 `xberg` 单文件；Stella 不再自带 `libheif`，也不注入私有 `LD_LIBRARY_PATH`。发行包只声明上游仍要求的系统 `libstdc++` 依赖。

在接入 Worker 前，必须分别使用 PDF、DOCX、Markdown 和 TXT fixtures 验证：

- 实际 `xberg` CLI 调用方式和退出语义；
- JSON 输出 schema 及异常输出；
- chunk 顺序、正文和页码或标题 locator；
- 关闭 OCR、关闭缓存等资源边界参数确实生效；
- chunk size 和 overlap 由 Stella 每次显式传入，不依赖 Xberg 默认值。

Stella 负责：

1. 对 JSON 结构和资源限制做验证。
2. 将 Xberg chunks 映射为 knowledge_chunk。
3. 规范化 locator。
4. 丢弃完整提取正文、pages 和其他不入库的中间产物。
5. 在一个短事务中写入全部 chunks、把文件更新为 ready，并完成当前 River job。

切块参数不向管理员或用户开放。初始实现使用以下可测试默认值，并在每次调用 Xberg 时显式传入：

- **chunk_size = 1000**；
- **overlap = 200**；
- 优先尊重标题、段落和句子边界。

参数的实际单位、边界行为和 locator 保留效果以 v1.0.0-rc.35 fixture 测试为准，不能仅根据旧 Kreuzberg 文档推断。

如果未来修改切块参数，必须从 raw_content 重建整份文件的 chunks，不能让同一索引长期混用不同参数。

### 7. 解析状态、重试和资源上限

状态机只有：

```text
processing → ready
processing → failed
```

规则：

- ready 和 failed 是 V1 终态。
- River 采用 **at-least-once** 执行语义；Knowledge Worker 必须对同一 file_id 业务幂等。
- 不增加 retrying 等数据库状态，尝试次数由 River 管理。
- 不提供手动重试；failed 保留原文件和精简错误，用户删除后重新上传。
- 不允许部分 chunks 可检索。
- Worker 提交解析结果前必须锁定 knowledge_file 行并再次检查状态；只有 processing 可以进入终态。
- 文件不存在或已经 ready / failed 时成功 no-op，不重新解析、不改写终态。
- 成功时“全部 chunks + processing → ready + River job completed”通过 **JobCompleteTx** 在同一事务中提交。
- 确定性失败时“无 chunks + processing → failed + River job completed”同样在一个事务中提交。
- 最终失败时文件必须没有 chunks。

自动重试：

- Xberg 进程异常、超时和暂时性数据库错误默认最多尝试 3 次；可重试错误在终态提交前直接返回给 River。
- 没有可提取文字、解析产物超限或输出 schema 明确无效时直接 failed。
- 每次尝试都从 raw_content 重新开始，不复用临时产物。

panic、SIGKILL、数据库故障或重试耗尽后，不能只依赖进程内逻辑恢复。系统必须提供持久化 reconciliation，定期检查长期处于 processing 且没有可继续执行 River job 的文件：仍可重试时重新入队，重试耗尽时收敛为 failed，避免永久 processing。

单文件 25 MiB 是 V1 领域硬限制。其余数值是可测试、可通过部署配置调整的初始实现默认值，不属于用户可见的领域合同：

| 项目                  |            初始值 | 性质                       |
| --------------------- | ----------------: | -------------------------- |
| 原始上传文件          |            25 MiB | 领域硬限制                 |
| 单次解析时间          |            5 分钟 | 实现默认值                 |
| Knowledge Worker 并发 |          每节点 2 | 实现默认值；不是集群总并发 |
| Xberg stdout JSON     |           128 MiB | 实现默认值                 |
| 提取后正文            | 32 MiB UTF-8 字节 | 实现默认值                 |
| 全部 chunk 正文合计   | 48 MiB UTF-8 字节 | 实现默认值                 |
| 单文档 chunk 数量     |            50,000 | 实现默认值                 |
| 持久化错误信息        |             1 KiB | 实现默认值                 |

任一解析产物上限被超过时直接失败，不静默截断。stdout 必须有界读取，不能用无上限缓冲；stderr 仅保留短诊断信息。错误不得包含正文、完整 stderr、服务器临时路径或堆栈。

### 8. 删除与并发

文件删除后必须立即不可检索：

- 删除 knowledge_file，同时通过外键级联删除 chunks 和数据库内原文件。
- 正在排队或执行的 River job 再次读取文件时，如果记录不存在则成功 no-op。
- Worker 最终提交事务必须锁定文件行，并确认文件仍存在且状态为 processing。
- 删除与最终提交竞争时，不能重新创建文件或留下孤立 chunks。

### 9. Agent 检索工具

V1 只暴露：

```text
knowledge.search(query, limit?)
```

参数：

- **query**：一条字符串。
- **limit**：最终返回的 chunk 数量，可选，默认 5，合法范围 1～10。
- 不接受 scope、user_id、agent_id、document_id、file_ids 或 query 数组。

每次查询必须在同一条 SQL 中完成：

1. pg_search BM25 content 匹配；
2. knowledge_file.status = ready；
3. 当前可信 user / Agent 对应的四级 scope 过滤；
4. BM25 score 降序和稳定 tie-break；
5. 最终 LIMIT。

禁止先搜索全库再在 Go 中过滤权限。

四个 scope 只是授权过滤，不分别执行四次查询，也不增加 scope 权重。所有可见 chunks 位于同一 BM25 索引，因此不使用 RRF。

V1 直接返回全局 BM25 Top K：

- 不超额召回；
- 不限制单篇文档结果数量；
- 不扩展相邻 chunks；
- 不做 rerank；
- 不设置固定或相对 BM25 分数阈值。

### 10. Query 生成

模型可以根据当前对话和最新用户问题生成一条独立检索 query，而不是机械传入用户原话。

提示词要求：

- 补全“它、这个流程、刚才那个制度”等上下文指代；
- 保留公司术语、产品名、岗位名、编号、日期、金额和原始关键词；
- 删除寒暄、回答指令和无关对话；
- 可以补充少量同义关键词，但不能把原始关键词全部替换；
- 不加入 scope、身份或文档过滤；
- 用户提到的文件名可以保留为普通 query 文本，但不能转化为指定文档条件；V1 只索引 chunk content，不保证仅凭文件名命中；
- 生成检索词，不提前生成答案。

一次工具调用只接收一条 query。第一次结果不足时，模型可以换一条 query 再次调用；V1 不接收 query 数组，也不在后端进行多查询融合。

服务端对 query 做空白和标点规范化。规范化后没有有效文字或数字时返回无结果，不执行全库扫描。query 最大长度尚未确定。

### 11. 触发方式

**knowledge.search** 作为只读工具始终提供给 Agent，由系统提示词指导模型自行判断是否调用。

提示词要求模型在以下情况检索：

- 用户明确要求查询 Knowledge Base 或根据企业资料回答；
- 问题依赖公司制度、内部流程、产品规范、内部项目或岗位知识；
- 问题包含模型无法凭通用知识可靠解释的内部名称、缩写或事实；
- 数字分身或数字员工需要其正式文件知识才能回答。

普通常识、纯写作、翻译、计算或已有上下文足够时默认不检索。用户明确要求不要检索时不调用。

V1 不增加：

- 检索开关或 API 强制字段；
- 关键词分类器；
- 独立分类模型；
- 每轮自动预检索。

这是提示词级约束，不是运行时绝对保证。模型漏检风险通过工具调用日志和真实问答评测发现，再调整提示词。

Knowledge chunks 是不可信数据，只能作为事实证据，不能作为系统或工具指令执行。提示词必须要求模型忽略文档中的提示注入。

### 12. 搜索结果和无结果语义

工具向模型返回：

```json
{
  "results": [
    {
      "content": "完整命中 chunk",
      "file_name": "差旅管理制度.pdf",
      "locator": {
        "first_page": 3,
        "last_page": 4,
        "heading_context": "第二章 > 住宿标准"
      }
    }
  ]
}
```

规则：

- content 是完整命中 chunk，不是 BM25 高亮摘要。
- “有限片段”限制结果数量和总输出，不把单个 chunk 再裁成短摘要。
- 不向模型返回 raw_content、存储路径、下载地址、scope、身份、BM25 score、file_id 或 chunk_id。
- 内部日志可以记录 file_id、chunk_id、排名和耗时，但普通日志不记录完整 chunk 正文。

三种结果必须区分：

| 情况              | 工具行为           | 模型行为                                        |
| ----------------- | ------------------ | ----------------------------------------------- |
| 没有词面命中      | 成功返回空 results | 可改写 query 再查；仍无结果则说明未找到相关知识 |
| 返回弱相关 chunks | 正常返回 Top K     | 模型核对内容；证据不足不能据此回答              |
| 数据库或检索异常  | 返回明确工具错误   | 说明检索暂时不可用，不能声称知识库没有内容      |

BM25 原始分数只用于内部评测，不提供给模型。固定阈值、匹配词覆盖规则或语义检索只有在标注数据证明必要后才增加。

### 13. 引用

模型使用 Knowledge 内容回答时必须在受支持的结论附近引用实际返回的来源。

格式定位：

| 文件格式 | 用户可见 locator                   |
| -------- | ---------------------------------- |
| PDF      | 1-based 页码范围，可附标题路径     |
| DOCX     | 标题路径；不使用不稳定的 Word 页码 |
| Markdown | 标题路径                           |
| TXT      | 仅文件名                           |

示例：

```text
【差旅管理制度.pdf，第 3–4 页，第二章 > 住宿标准】
【员工手册.docx，休假制度 > 年假】
【部署指南.md，生产环境 > 数据库迁移】
【客服术语表.txt】
```

locator 缺失时退化为仅引用文件名，不能编造页码或标题。V1 不展示 byte offset、chunk ordinal、数据库 ID 或分数，也不生成可点击的原文件链接。

### 14. Knowledge Base 管理 UI

#### 页面入口与 scope 映射

V1 使用两个入口管理四级 scope，不提供可以任意切换 owner 的通用管理页：

| 入口                   | 路由                                          | 可管理 scope                                                     | 行为                                                                |
| ---------------------- | --------------------------------------------- | ---------------------------------------------------------------- | ------------------------------------------------------------------- |
| Agent 顶部“知识库”页签 | `/agents/$agentId/knowledge?q=`               | 当前用户在当前 Agent 下的 **user_agent**                         | 不显示 scope 或 Agent 选择器；管理员进入时也只管理自己的 user_agent |
| 设置侧栏“知识库”       | `/settings/knowledge?scope=user&agent_id=&q=` | 普通用户仅 **user**；管理员可选 **user / system / system_agent** | system_agent 必须再选择目标 Agent                                   |

Agent 顶部的“知识库”只出现在 Agent 级页面，不增加到项目或群聊页面。设置侧栏入口对所有用户可见，权限只决定页面中可以选择和管理哪些 scope。

设置页规则：

- 所有人默认进入 **user**；普通用户即使修改 URL，也必须被规范化回 user。
- 普通用户只看到自己的 user 文件，不显示只有一个选项的 scope 切换器。
- 管理员显示三个互斥视图：**我的 · 全部智能体**、**系统 · 全部智能体**、**系统 · 仅此智能体**；它们分别对应 user、system、system_agent。
- 选择 system_agent 后必须显式选择 Agent，不自动选中第一个 Agent。未选择时显示选择 Agent 的空状态并禁用上传。
- 切换 scope 或 Agent 时清空 q 和已经加载的分页结果。
- scope、agent_id 和 q 保存在 URL；“加载更多”的 page_token 只保存在页面查询缓存中，不写入 URL。

技能的四级 scope 存在覆盖优先级，Knowledge 的四级 scope 则是叠加检索语料，因此 Knowledge 页面不能展示“优先级”阶梯，也不能用排列顺序暗示 scope 权重。

#### 文件列表

页面使用紧凑表格，每行表示一个不可变 knowledge_file：

| 列       | 展示内容                                                            |
| -------- | ------------------------------------------------------------------- |
| 文件     | 文件类型图标和 file_name；failed 时在文件名下显示经过清理的简短错误 |
| 状态     | processing / ready / failed                                         |
| 大小     | size_bytes 的易读格式                                               |
| 创建时间 | created_at 转为当前用户本地时间                                     |
| 操作     | 删除                                                                |

V1 不显示 scope、Agent、上传者、updated_at、chunk 数量或 BM25 信息；当前页面本身已经确定 scope 和 Agent。文件名不可点击，不提供预览、下载或原文件内容接口，也不提供批量操作。

所有状态都可以删除，包括 processing；每次删除都要求确认。删除成功后，页面立即移除该行并在本地扣减对应的文件数和字节占用。

列表行为：

- 服务端使用游标分页，固定按 **created_at DESC、id DESC** 排序；页面每次请求 50 条并使用“加载更多”，不显示页码。
- q 只对 file_name 做忽略大小写的包含搜索；前后空白由服务端移除，最大 200 个字符。
- 不提供状态筛选、排序选项或总存储量筛选。
- 列表包含 processing、ready、failed 三种状态。
- 浏览器进入页面或整页刷新时获取最新状态；V1 不轮询、不在窗口聚焦时刷新、不提供手动刷新，也不接入 SSE 或 WebSocket。
- 上传完成后立即重新获取第一页，新文件显示为 processing。存在 processing 文件时提示：**“解析在后台进行，请刷新页面查看最新状态。”**

#### 上传

上传弹窗不提供 scope 或 Agent 选择器，目标 owner tuple 完全由当前页面上下文确定。界面支持拖拽和多选，但管理 API 一次只接收一个文件：

- 前端以受控并发逐个调用创建接口，初始并发数为 3。
- 每个文件独立成功或失败，不建立批量事务；部分成功时保留成功结果并逐项展示失败原因。
- 前端先检查扩展名、媒体类型和 25 MiB 限制，以便尽早提示；服务端验证仍是最终依据。
- 同名或同内容文件允许重复上传，不弹出覆盖或去重提示。

#### 配额展示

页面头部只显示“已用文件数 / 上限”和“已用原文件字节 / 上限”两项文字，不增加进度条或配额详情页：

| 当前页面     | 展示的配额池                                                        |
| ------------ | ------------------------------------------------------------------- |
| user         | 当前用户全部 user + user_agent 的合并个人配额：2,000 个文件、10 GiB |
| user_agent   | 与 user 相同的当前用户合并个人配额，并明确提示该额度由个人知识共享  |
| system       | system 全局配额：4,000 个文件、20 GiB                               |
| system_agent | 当前所选 Agent 的独立配额：1,000 个文件、5 GiB                      |

配额统计不受 q 或分页影响，并包含 processing、ready、failed 文件。

### 15. Knowledge File 管理 API

V1 直接暴露统一的顶层 **knowledge-files** 资源，不增加 KnowledgeBase 父资源，也不为两个 UI 入口建立专用接口：

| 方法   | 路径                        | operationId           | 用途                              |
| ------ | --------------------------- | --------------------- | --------------------------------- |
| GET    | `/api/knowledge-files`      | `listKnowledgeFiles`  | 按一个 owner tuple 列出文件和配额 |
| POST   | `/api/knowledge-files`      | `createKnowledgeFile` | 上传一个不可变文件                |
| GET    | `/api/knowledge-files/{id}` | `getKnowledgeFile`    | 获取单个文件元数据                |
| DELETE | `/api/knowledge-files/{id}` | `deleteKnowledgeFile` | 删除文件、原始内容和 chunks       |

V1 不提供 PATCH、Replace、Retry、Download 或原文件内容接口。

#### Scope 参数与授权

List 和 Create 的查询参数遵循同一规则：

- **scope** 必填且没有默认值，只接受 system、system_agent、user、user_agent。
- system_agent、user_agent 必须提供 **agent_id**；system、user 禁止提供 agent_id。组合错误返回 400。
- API 永不接受 user_id；user 和 user_agent 的 owner 始终来自可信 session。
- system、system_agent 复用现有系统资源管理授权；user、user_agent 只能管理当前用户自己的文件。
- 任一 Agent scope 都必须通过现有 Agent access gate。
- 管理员也不能通过该 API 管理其他用户的 user 或 user_agent 文件。

Get 和 Delete 只接收路径中的 id，不再接收 scope、user_id 或 agent_id。服务端从 knowledge_file 记录恢复 owner tuple 后执行同样的授权；记录不存在或当前用户无权管理时都返回 404，避免泄露文件存在性。List 或 Create 针对一个明确但无权访问的 scope 时返回 403。

#### List

请求示例：

```http
GET /api/knowledge-files?scope=system_agent&agent_id=finance&q=%E5%B7%AE%E6%97%85&page_size=50&page_token=opaque
```

参数：

- q 可选，移除首尾空白后最多 200 个字符，只对 file_name 做忽略大小写的包含搜索。
- page_size 可选，默认 20，合法范围 1～500。
- page_token 可选且不透明，绑定 scope、agent_id 和规范化后的 q；携带 token 后改变这些参数返回 400。
- 排序固定为 created_at DESC、id DESC。

响应示例：

```json
{
  "knowledge_files": [
    {
      "id": "019...",
      "scope": "system_agent",
      "agent_id": "finance",
      "file_name": "差旅管理制度.pdf",
      "media_type": "application/pdf",
      "size_bytes": 1827364,
      "status": "processing",
      "error_message": null,
      "created_at": "2026-07-23T04:30:00Z",
      "updated_at": "2026-07-23T04:30:00Z"
    }
  ],
  "next_page_token": null,
  "quota": {
    "used_files": 326,
    "max_files": 1000,
    "used_bytes": 1932735283,
    "max_bytes": 5368709120
  }
}
```

knowledge_files 包含三种状态。quota 始终针对当前请求对应的完整配额池统计，不受 q 和分页影响。

#### Create

请求：

```http
POST /api/knowledge-files?scope=user_agent&agent_id=finance
Content-Type: multipart/form-data

file=<单个文件>
```

multipart body 只包含 **file**；scope 和 agent_id 必须放在查询参数中，使服务端可以在读取文件体之前完成以下步骤：

```text
校验 session
→ 校验 scope / agent_id 组合
→ 校验 scope 权限与 Agent access
→ 有界解析 multipart 并验证文件
→ 在事务中检查配额、创建 processing 文件并 InsertTx 创建 River job
→ 返回 201
```

Create 成功时直接返回完整 KnowledgeFile，不使用 `data` 包装，也不同时返回 quota。多文件上传完成后，前端重新请求列表第一页，以统一刷新文件列表和配额。

#### KnowledgeFile

管理 API 中的公开文件结构为：

```json
{
  "id": "019...",
  "scope": "user_agent",
  "agent_id": "finance",
  "file_name": "差旅管理制度.pdf",
  "media_type": "application/pdf",
  "size_bytes": 1827364,
  "status": "processing",
  "error_message": null,
  "created_at": "2026-07-23T04:30:00Z",
  "updated_at": "2026-07-23T04:30:00Z"
}
```

- id 使用 UUID；agent_id 在 system 和 user 中为 null。
- status 只允许 processing、ready、failed。
- error_message 只在 failed 时返回经过清理的简短信息，否则为 null。
- created_at、updated_at 使用 UTC RFC 3339。
- 不返回 user_id、raw_content、chunks、locator、River 状态、BM25 信息或内部错误。

Get 返回上述元数据，不返回文件内容。Delete 可以删除任一状态，成功返回 204；再次删除同一 id 返回 404。删除物理级联清除 raw_content 和 chunks 并立即释放配额，不主动取消已经运行的 River job；旧 job 后续按第 8 节规则成功 no-op。

#### 错误

所有失败使用 Stella 统一的结构化错误响应：

| 状态码                 | 场景                                                                         |
| ---------------------- | ---------------------------------------------------------------------------- |
| 400 INVALID_ARGUMENT   | scope / agent_id 组合错误、缺少文件、单文件超过 25 MiB、格式不支持或文件无效 |
| 401 UNAUTHENTICATED    | session 无效                                                                 |
| 403 PERMISSION_DENIED  | 无权管理明确请求的 scope 或无 Agent access                                   |
| 404 NOT_FOUND          | 按 id 查询或删除的文件不存在，或当前用户无权管理                             |
| 429 RESOURCE_EXHAUSTED | 对应配额池已满                                                               |
| 500 INTERNAL           | 文件和 River job 原子创建失败；事务回滚后两者都不存在                        |

V1 不单独使用 413 或 415。400 的结构化 details.reason 至少区分 `file_too_large`、`unsupported_file_type`；429 使用 `quota_exceeded` 并在 details 中返回当前 quota。重复名称或内容不是冲突，不返回 409。

## 关键行为与接口

### 管理能力边界

Knowledge 的管理能力只存在于第 14 节定义的 Agent 顶部页签和设置页，并通过第 15 节统一的 knowledge-files 资源完成。对话输入框、普通聊天附件、项目和群聊都没有 Knowledge 管理入口。

### Agent 工具契约

工具是只读的。模型不能通过工具：

- 改变 scope；
- 指定其他 user 或 Agent；
- 上传、删除或修改文件；
- 获取原文件；
- 指定某篇文件；
- 载入整篇文档。

### 可观测性

调试和评测至少需要观测：

- 本轮是否调用 knowledge.search；
- 模型生成的 query 和 limit；
- 命中的内部 file/chunk ID、BM25 排名和耗时；
- 返回结果数量；
- 工具错误类型。

日志不得默认复制 chunk 正文。

## 验收与测试策略

### 管理 UI 与 API

- Agent 顶部“知识库”固定管理当前用户与路由 Agent 的 user_agent，管理员不能从该入口切到 system_agent。
- 普通用户的设置页只能管理自己的 user，管理员的设置页可以在 user、system、system_agent 三个独立视图间切换。
- 普通用户伪造 settings scope 参数会被规范化回 user；system_agent 未选 Agent 时不能加载列表或上传。
- 页面切换 scope 或 Agent 时清空 q 和已加载游标；刷新浏览器后能看到最新解析状态，页面不会自动轮询。
- 文件表格、失败错误、删除确认、processing 提示和合并个人配额展示符合第 14 节。
- 多文件上传只在前端并发调用单文件 Create；单个失败不回滚其他成功文件。
- List/Create 必须显式传 scope，且 agent_id 的必填和禁止组合被服务端严格验证。
- Create 在读取 multipart 文件体前完成 session、scope 权限和 Agent access 校验；未授权大文件不会先被完整读取。
- List 使用稳定游标分页和文件名搜索；page_token 不能跨 scope、Agent 或 q 复用。
- List 的 quota 不受 q 和分页影响；user 与 user_agent 返回同一个当前用户合并个人配额池。
- Get/Delete 只根据 id 定位文件；不存在和不可管理都返回 404，不能用于探测其他 owner 的文件。
- Create 返回 201 和完整 KnowledgeFile；Delete 返回 204 并立即释放配额，再次删除返回 404。
- 文件超限、格式不支持和配额已满返回第 15 节约定的结构化错误 reason。

### Scope 与授权

- 四个 scope 的 owner tuple 均受数据库 CHECK 约束。
- Knowledge domain 的四级 scope 写权限与 MCP / Skills 语义一致，HTTP handler 不再分散判断角色。
- user / user_agent 的 owner 只能来自当前可信 session；客户端不能写入其他用户名下。
- system_agent / user_agent 的管理请求必须通过现有 Agent access gate，不能操作当前用户无权访问的 Agent。
- 未授权上传在读取请求体之前被拒绝。
- user U 使用 Agent A 时只检索四层并集，不能读取其他用户或其他 Agent 的限定文件。
- 没有关联 Stella 用户的会话、群聊和后台任务均不能获得任何 Knowledge 结果。
- 工具参数不能伪造 scope、user_id 或 agent_id。

### 上传、解析和删除

- 支持的四类文件在 25 MiB 内可以进入 processing。
- 超限和不支持格式被确定性拒绝。
- system 按整个 scope 共享 4,000 个文件、20 GiB 原文件额度。
- 每个 Agent 的 system_agent 独立拥有 1,000 个文件、5 GiB 原文件额度。
- 每个用户的 user 和全部 user_agent 共享 2,000 个文件、10 GiB 原文件额度。
- system_agent 按 agent_id 统计；user_agent 的个人配额统计不按 agent_id 分裂。两者授权和检索都严格校验 agent_id。
- processing、ready、failed 都占用文件数和原文件字节额度；删除后立即释放。
- 配额检查与文件、River job 创建原子执行，并发上传不能共同越过上限。
- 文件与 River job 通过 InsertTx 同事务创建，事务回滚时二者都不存在。
- 重复上传相同内容产生不同 file_id；重复投递同一 file_id 不产生并行解析或重复 chunks。
- 内容变更时，新文件 processing 或 failed 不影响旧文件继续保持 ready；只有用户主动删除旧文件后，旧内容才停止检索。
- 新文件 ready、旧文件尚未删除时，两份文件都可以被检索并同时计入配额；删除旧文件后立即释放其额度。
- 新文件的 Worker 只处理自身 file_id，不删除或修改旧文件，也不执行隐式替换。
- xberg v1.0.0-rc.35 的 PDF、DOCX、Markdown、TXT fixtures 能产出有序 chunks 和可用 locator，且切块参数被显式传递。
- 扫描或无文字 PDF 进入 failed，不会错误变为 ready。
- chunks、processing → ready/failed 和 River job completed 通过 JobCompleteTx 原子提交；处理中和 failed 文件不可检索。
- Worker 重复执行、文件已经终态或文件已删除时成功 no-op。
- panic、SIGKILL、数据库故障和重试耗尽后，durable reconciliation 不会让文件永久停在 processing。
- 重试、超时、stdout、正文和 chunk 上限均被执行。
- 默认 Worker 并发 2 的含义是每个节点，而不是集群全局并发 2。
- 超限失败不产生截断后的可检索内容。
- 删除文件立即移除 BM25 结果；运行中的旧 job 不会恢复已删除数据。

### 检索与引用

- SQL 同时执行 ready 与四级 scope 过滤，而不是应用层后过滤。
- 中文、英文和混合查询使用 pg_search BM25 返回稳定 Top K。
- limit 默认 5，且不能超过 10。
- 单 query、无文档过滤、无 scope boost、无每文档配额和无分数阈值的行为符合契约。
- 返回完整 chunk、文件名和受支持 locator，不返回原文件和内部授权字段。
- 空命中与检索故障具有不同工具结果。
- 模型使用 Knowledge 事实时给出真实引用，不编造 locator。
- 含提示注入文本的文件不会改变系统或工具行为。

### 评测门槛

在考虑 embedding、hybrid search、rerank、query 数组、邻块扩展或 load 工具前，先建立真实文件问答集，并区分：

- 正确 chunk 没有进入 Top K：召回失败；
- 正确 chunk 已进入 Top K 但模型没有使用：生成或提示词失败；
- chunk 缺少必要上下文：切块或上下文策略失败；
- 模型没有触发工具：触发失败。

只针对被数据证明的失败环节增加复杂度。

## 风险

### BM25 语义召回有限

用户和文档使用完全不同的表达时可能零命中。模型可以保留原词并尝试一次替代表述，但 V1 不通过向量检索掩盖缺少评测的问题。

### 提示词触发不是硬保证

模型可能漏掉应该检索的问题，也可能对通用问题多余检索。必须通过调用日志和真实对话评测迭代提示词。

### PostgreSQL BYTEA 的规模边界

25 MiB 单文件不代表数据库可以无限保存文件。V1 通过 system、每 Agent 的 system_agent 和每用户个人知识配额限制文件数量和原始文件总字节数，不再增加实例级配额。整体理论上限随用户数和 Agent 数线性增长，当前按约 100 个用户和 100 个 Agent 举例评估；若任一规模显著变化，必须重新核算 PostgreSQL、WAL 和备份容量，并据此判断是否迁移到 Asset Store。

### system_agent 管理可能集中

Knowledge 复用的现有 system_agent 资源入口由企业管理员管理。大量员工数字分身可能让该既有入口成为维护瓶颈；只有真实运营需求出现后，才在统一资源授权模型中讨论演进，而不是为 Knowledge 单独增加角色。

### 无用户会话无法使用系统知识

mentor 的安全边界要求可信 Stella 用户存在后才加载任何 scope。某些群聊或后台 Agent 即使可执行，也会得到空 Knowledge 结果；未来若要支持，必须先设计可信用户映射或独立服务主体，不能绕过现有规则。

### 文件内容可能包含提示注入

检索结果会完整返回 chunk。系统提示必须把文档标记为不可信证据，禁止执行其中的指令；仍需通过攻击样本验证模型行为。

### 解析与引用质量

复杂 PDF 阅读顺序、DOCX 标题识别和表格文本可能不稳定。V1 需要使用真实样本验收；TXT 只能提供文件名引用。

### 重复文件与内容变更过渡期

V1 不做内容去重，也不支持指定文档。多个同名文件，或内容变更期间同时存在的新旧文件，可能产生重复甚至冲突的检索结果和难以区分的文本引用。这是保持文件模型不可变且不引入替换关系的明确代价；只有真实使用证明过渡期重复命中不可接受后，才设计原子替换能力。

## 未决问题

以下问题不影响当前领域方案成立，但仍需在进入对应实现前明确：

### 运行与限制

- knowledge.search 的 query 最大字符数。
- River 专用队列名称、退避间隔和运维监控。
- 面向用户的完整错误文案和日志保留周期。
- 是否及何时增加恶意文件扫描。

### 评测与后续演进

- 真实 PDF、DOCX、Markdown、TXT 样本集及标准问答集。
- BM25 召回、提示词触发、引用正确性和延迟的验收指标。
- 哪类失败足以推动 embedding、hybrid、rerank、邻块扩展或其他复杂度。
- 外部来源、飞书同步和更复杂知识治理何时作为独立版本重新设计。
