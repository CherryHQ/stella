# Stella × Harbor 评测适配:候选运行时留在宿主,工具执行经 per-trial bridge 落进 task 容器

- **日期**: 2026-08-18(v2,吸收对抗 review 后重写)
- **状态**: Draft,待评审
- **范围**: Terminal-Bench 2 / WorkBuddy Bench 的执行与计分契约、`stella-eval-agent` 生命周期、新增 `bridge` sandbox 后端、eval 能力档、结果与溯源 schema、fail-closed 判定、失败分类、资源与安全边界。不含 SWE-bench 接入、WorkBuddy 全量跑分、Terminal-Bench 基线成绩、任何新公共 API。
- **关联**: [#1054](https://github.com/CherryHQ/stella/issues/1054),后续 [#1055](https://github.com/CherryHQ/stella/issues/1055)
- **修订记录**: v1 采用"经宿主 docker socket 附着到容器"。review 指出 Harbor 的 `environment_id` 是环境定义哈希而非运行中容器 ID、docker 组等价宿主 root、且缺少超时 stop 与 fail-closed 判定。v2 换为 bridge 方案并补齐这些。

## 1. 问题

我们要拿 Stella 的**候选运行时本身**去跑 agentic benchmark,而不是拿"一个用同款模型的裸 CLI"去跑。这两者的差别就是 Stella 的全部产品价值:技能体系、记忆、工具编排、沙箱策略。

难点在于 benchmark 的评分对象是 **task 容器内的文件系统状态**:测试脚本在容器里跑,读的是容器里的仓库。而 Stella 是一个带数据库的常驻服务,它的工具默认在自己所在的机器上执行命令。这两件事必须被强行对齐,否则得到的分数要么恒为 0,要么更糟——看起来跑通了,实际评的是另一个环境。

本文定下对齐方式,并给出一个可执行 spike 的边界。**本设计的第一优先级不是跑出分数,而是保证任何一个 pass 都不是假的。**

## 2. 两个 benchmark 的关系(事实与假设分开写)

**事实**:WorkBuddy Bench 的 agent wrapper 建在 Harbor 之上。`src/workbuddy_bench/agents/cc_agent.py` 顶部即 `from harbor.agents.installed.base import BaseInstalledAgent`;`configs/harnesses/_template.harness.yaml` 的 `env` 字段注释为 "Forwarded to harbor's `agent.env`";harness 用 `import_path: workbuddy_bench.agents.<module>:<Class>` 注册,与 Harbor 的 `harbor run -a path.to.module:Class` 同构。

**假设(待 spike 验证)**:一个 Harbor agent 类能同时满足两个 benchmark。共同基类只证明它们是同一框架,不证明 WorkBuddy 的 harness v3 四层配置(family/version、preset、split-mount、`local_proxy`)契约已被满足;`configs/harnesses/HARNESS_AUTHORING.md` 明确还有额外约定。第 16 节的验证计划里有对应条目。

WorkBuddy 侧两个可借用的既有约定:`mount:` 声明式 split-mount(harness CLI 从预构建镜像挂进容器);`local_proxy` 模式下容器内 agent 访问宿主上的代理服务。容器与宿主之间有服务通信,是这套 bench 的既有做法。

## 3. 环境约束(实测数据)

拉取 Terminal-Bench 2 公开仓库全部 89 个 `task.toml` 统计:

| 字段                         | 分布                                      |
| ---------------------------- | ----------------------------------------- |
| `environment.cpus`           | 1 核:84;2 核:3;4 核:2                     |
| `environment.memory_mb`      | 2048:69;4096:17;8192:3                    |
| `environment.allow_internet` | `true`:89;`false`:0                       |
| `environment.gpus`           | 0:89                                      |
| `agent.timeout_sec`          | 900:48;1800:17;3600:12;其余尾部 600–12000 |
| `agent.user`                 | 全部未设置,走镜像默认用户                 |

由此得到的约束,按可靠度分级:

- **硬约束**:84/89 的 task 只有 1 核、69/89 只有 2GB。把 stellad + PostgreSQL 塞进 task 容器,候选运行时会和被测任务抢同一个核,污染程度随 task 而变、不可校正。这一条独立地否决"全部进容器"。
- **次要理由**:`agent.timeout_sec` 中位数 900 秒,容器内冷启动 stellad(迁移 + 资产同步)会侵蚀预算。系统测试的 `readyTimeout=120s`(`test/system/harness_test.go:36`)是宽松上限,不是实测冷启动;实际侵蚀比例待测,但方向确定。
- **次要理由**:`agent.user` 未设置意味着走镜像默认用户,常见为 root;内嵌 PG 的 `initdb` 拒绝 root。这是安装约束(可在 setup 中建非 root 用户绕过),不是不可解条件。

对 dataset 的限定:上述 89 个来自 GitHub 公开仓库,HuggingFace 上的 dataset 有 94 个顶层目录。正式跑分前以 `harbor datasets` 实际拉到的版本重新统计。

## 4. 决策

### D1 — Stella 常驻宿主,不进 task 容器

一个长驻 stellad 服务所有 trial。冷启动从"每 task 一次"降为"整个 job 一次"。容器内 CPU/内存全部留给被测任务。

### D2 — 新增 `bridge` sandbox 后端,经 per-trial bridge 落进容器;bridge 由 Harbor adapter 用 `BaseEnvironment` 实现

Harbor adapter 的 `run()` 持有当前 trial 的 `BaseEnvironment` 对象,该接口原生提供带 `cwd / env / timeout_sec / user` 的 `exec`,以及 `upload_file / upload_dir / download_file / download_dir`(Harbor `src/harbor/environments/base.py:917-947, 1128`)。

adapter 在 `run()` 内起一个 per-trial 本机 RPC 服务(Unix socket,每 trial 一个,带随机 nonce),把这些能力暴露出来。Stella 的 `bridge` 后端连到这个 socket,`Exec` 转发到 `environment.exec`,`FileAccess` 用 upload/download + 容器内 exec 组合实现。

相比 v1 的 docker socket 方案,这一步同时消掉四个问题:

1. 不需要"拿容器 ID"。Harbor 的 `environment_id` 是环境定义内容哈希,不是运行中容器 ID(`base.py:223`);Docker provider 通过私有 Compose 命令操作 main service。容器 ID 从来不是公共契约。
2. 不需要把 Stella 服务用户加进 `docker` 组,v1 第 13 节"等价宿主 root"的安全边界消失。
3. Harbor 的 task workdir、`agent.user`、main service 语义由 `BaseEnvironment` 原生保留,不需要我们重新表达。
4. 将来上云 provider(Modal / Daytona),`BaseEnvironment` 的实现换掉,bridge 与 Stella 后端一行不动。

代价:多一跳本机 RPC;Stella 的工具执行依赖 adapter 进程存活。后者是我们要的性质,不是缺陷——adapter 死了,工具执行必须跟着死,不能有任何回退(第 14 节)。

### D3 — 用内嵌 PostgreSQL

stellad 跑在宿主 Debian 13 trixie 上,在内嵌 PG 运行时支持列表内(`internal/pgruntime/runtime.go:184`,支持 `bookworm` / `noble` / `trixie`),以非 root 用户运行。零配置可用,少一个组件。

### D4 — 容器内装"Stella 助手工具包",不装 stellad;明确接受并记录一个 eval 能力档

v1 声称"容器内零安装"。这是错的:Stella 的系统技能明确承诺 `fd`、`rg`、`mise`、`tap` 可用(`resources/skills/system/stella/SKILL.md:168`),sandbox policy 还声明了 `/user`、`/opt/stella/*`、builtin skills 与 per-user mise tree(`internal/agent/sandbox/env.go:19-53`),现有 docker 后端靠预制镜像和创建前挂载 tool-cache 才提供这些(`plugins/sandbox/docker/session.go:353-366`)。对一个裸 task 容器什么都不装,评到的是降级的 Stella,不是候选运行时。

因此 `install()` 不为空:上传一个静态编译的助手工具包(`fd` / `rg` / `mise` / `tap` 及 builtin skill bundle)到 `/installed-agent/stella/`,PATH 前置。这个包由 harness 侧预构建,`CGO_ENABLED=0`,不假设镜像 OS。

**诚实边界**:依赖 `uv`、`xberg` 等重运行时的技能仍然不可用(`internal/skills/tool.go:382-401` 只投影 skill 文件,不带其依赖)。本设计**不试图**在 task 容器里复现完整 mise 工具树。取舍是:定义一个 **eval 能力档**(哪些工具、哪些技能启用),把它的内容哈希写进结果的 `capability_profile_digest`,让每个分数都能回答"评的是哪个 Stella"。能力档从窄开始,随 spike 扩大;扩大是 #1055 的事。

### D5 — 继承 `BaseInstalledAgent`,理由重述

v1 给的三条理由(错误分类、日志目录、生命周期钩子)大部分不成立:日志目录、setup/run 生命周期、`populate_context_post_run` 属于 `BaseAgent`;`ERROR_PATTERNS` 只在 `BaseInstalledAgent._exec()` 收到非零 `environment.exec` 时触发(`src/harbor/agents/installed/base.py:837-853`),Stella 走 HTTP 完全不经过它。

保留 `BaseInstalledAgent` 的**真实**理由只有两条:D4 决定了 `install()` 有实质工作,而父类 `setup()` 创建的 `/installed-agent`(`base.py:931`)正好是工具包的落点;以及 WorkBuddy 现有 wrapper 全部继承它,先对齐再说,若 spike 证明 WorkBuddy 也接受 `BaseAgent`,再评估是否切换。**失败分类不依赖 Harbor 的正则,由我们自建(第 10 节)。**

## 5. 被否决的方案

### A. 全部塞进 task 容器

stellad + 内嵌 PG + 技能包 + mise 工具树全部装进容器内运行。否决理由见第 3 节:资源竞争是硬否决;冷启动与 root/initdb 是次要理由。此外安装体积几百 MB,`install()` 要面对 89 种异构镜像。

### B. 容器内 shim + 反向连接到 Stella

为"跨云 provider 容器不可达"设计。D2 的 bridge 已经通过 `BaseEnvironment` 天然获得云 provider 兼容性,不再需要它。

### C. Stella 经宿主 docker socket 直接附着容器(v1 方案)

否决理由见 D2 的四条。核心是"容器 ID"不是 Harbor 公共契约,以及 docker 组等价宿主 root。

## 6. 执行架构

两条方向相反的链路,必须同时存在。只做第一条是本设计里唯一的致命失败模式。

**链路一(adapter → Stella,控制面):** HTTP 调 Stella REST API:provision 用户 → 建 provider/agent/session → 提交指令 → SSE 等待 → **超时或取消时显式 stop 并确认终态** → 导出证据 → 清理。

**链路二(Stella → 容器,数据面):** Stella 的每一次 bash/read/write/edit 经 `bridge` 后端 → adapter 的 per-trial socket → `BaseEnvironment` → 容器。这条决定分数是否有效。

一个 trial 的时序:

```
Harbor 起容器 (task 自带 docker_image)
  └─ setup(): mkdir /installed-agent;install() 上传助手工具包
  └─ run(instruction, environment, context):
       0. 起 per-trial bridge socket,生成 nonce;deadline = agent.timeout_sec - 安全余量
       1. HTTP → Stella: provision 用户(expires_at = now + timeout + 余量)
          + agent(套 eval 能力档)+ session(绑定 bridge socket 路径 + nonce)
       2. 单向同步 task workdir 的 AGENTS.md / .agents/skills 到 Stella project(见 §7.3)
       3. HTTP → Stella: 提交 instruction,SSE 等待
          └─ 期间 Stella 工具经 bridge 落进容器;bridge 记录每次调用
       4. 三种退出:
          a. SSE 报告 turn 终态          → 进入 5
          b. 到 deadline                  → POST /stop,轮询 session 直到终态或硬上限
          c. adapter 自身异常             → 同 b,并把 trial 标记 adapter 失败
          任一路径必须拿到"turn 已终止"的确认;拿不到 → trial 标记 invalid
       5. HTTP → Stella: 导出 messages / 工具调用 / workspace 差异 / usage
       6. finally: deactivate 用户;关 bridge socket;写 /logs/agent/stella/
  └─ populate_context_post_run(): 转成 Harbor Trajectory,附 bridge 调用账本
Harbor 跑 verifier → reward
post-trial 合并步骤: 读 Harbor result.json,把 verifier 输出与 exception_info 并入我们的结果 schema
```

**关于步骤 4 的必要性**:Stella API 明确规定"断开 message 或 events stream 不会停止 turn,必须显式调用 stop"(`api/spec/domain/sessions/paths.yaml:238`);而 Harbor 在 agent 超时后**仍然运行 verifier**(`src/harbor/trial/single_step.py:75-109`)。没有步骤 4,超时路径下 Stella 会一边改文件、verifier 一边读,judge 可能读到一个瞬时正确的中间态。

## 7. 工具与上下文如何落进容器

### 7.1 四个核心工具

核心工具只有四个(`internal/agent/sandbox/tools.go:11`),它们全部在沙箱抽象层之后:

| 工具  | 调用路径                                    | 证据               |
| ----- | ------------------------------------------- | ------------------ |
| bash  | `host.Exec(ctx, command, opts)`             | `bash.go:54`       |
| read  | `SelectFileView` → `Files.ReadFile`         | `read.go:58,67`    |
| write | `SelectFileView` → `Files.WriteFile`        | `write.go:43,52`   |
| edit  | `SelectFileView` → `ReadFile` + `WriteFile` | `edit.go:49,58,72` |

实现 `Session` 接口,这四个工具的执行落进容器,工具层代码不改。路径翻译已就位:`resolveToolExpression`(`tools.go:38`)产出进程可见路径,物理映射由后端完成。

**v1 曾引用 `pkg/sandbox/bypass_guard_test.go` 论证"抽象边界被强制维护",这是过度解读**:该测试只扫描六个指定文件,不含这四个核心工具,更不含插件、MCP、builtin。它不构成边界保证。真正的保证要靠 7.2 的能力档和 §9 的账本核对。

### 7.2 非核心工具:必须收窄,不然数据面漏出容器

以下路径**不经过** `Session`,会直接从宿主执行:

- `webfetch` 的 `Build` 丢弃 `ToolContext`,用自己的 HTTP client 从宿主发请求(`plugins/tools/webfetch/webfetch.go:40, 151`);
- MCP 由宿主进程连接远端(`internal/mcp/tool.go`, `internal/mcp/client.go`);
- scheduler、goal、delegate、`session.create` 会创建越过当前 turn 的持久工作或新的 Runner,而 `RunnerParams` 与 sandbox `Config` 都没有 trial 亲和字段(`internal/agent/runtime/runner.go`, `internal/agent/sandbox/config.go:24-49`),子 session 会拿到一个不绑 bridge 的 Runner。

**决策**:eval 能力档通过现有的 per-agent 工具可见性覆盖(`PATCH /api/agents/{id}/tools/{toolName}`,`internal/agent/tool_override.go`)**禁用**上述全部工具。第一版评的是"单 agent、单 session、四核心工具 + 启用的技能"的 Stella。多 agent 能力的评测是 #1055 的范围,前提是先解决 trial 亲和的传播。

`bridge` 后端本身对未绑定 bridge 的 session 拒绝创建(不是回退到别的后端)。

### 7.3 上下文 split-brain

runner 通过**宿主** Home snapshot 读取 project 的 `AGENTS.md` 与 project skills(`internal/agent/runner_builder.go:187-318`, `internal/agent/project.go:37-67`),而 `bridge` Session 要到 `runner_impl.go:107-118` 才创建;system prompt 已非空,Session 文件系统 fallback 不会执行(`runner_impl.go:120-122`)。结果是模型看到宿主上的项目上下文,却在容器里执行工具。

**第一版处理**:adapter 在提交指令前,用 `download_file` 把 task workdir 的 `AGENTS.md` / `.agents/skills` 拉出来,经 workspace upload API 写进 Stella project 的对应位置,单向、一次。这是权宜:它复制的是 trial 开始时刻的快照。是否需要更深的对齐(让 project context 直接从 Session 读),取决于 spike 中有多少 task 自带这类文件。

### 7.4 `FileAccess` 的语义,不能靠"用 archive API 实现"一句带过

`FileAccess`(`pkg/sandbox/session.go:134`)六个方法:`ReadFile` / `ReadDir` / `Stat` / `WriteFile` / `ProjectFiles` / `ProjectTempFiles`。接口要求跨 mount symlink fail-closed、exact/no-replace 投影。bridge 后端的实现约束:

- 授权根是**容器的 `/`**。容器本身就是隔离边界,bash 工具本来就能写容器内任何路径,再对 read/write 做 `realpath` 根检查没有安全意义,只会让 read/write 与 bash 的可见范围不一致。宿主侧文件系统对 bridge 完全不可达(它只持有 socket),"写出评分对象所在容器"这条路径不存在。
- `WriteFile` = `upload_file` 到同目录临时名 + 容器内 `mv -f`,保证单文件原子。
- `ProjectFiles` / `ProjectTempFiles` = `upload_dir` 到新建目录 + 容器内 rename;不覆盖已存在目录。
- `ProjectTempFiles` 不可省略:技能系统靠它把宿主上的 skill 文件投影进容器再由 bash 执行(`internal/skills/tool.go:391`)。
- 单 trial 单写者,TOCTOU 在本设计中接受;并发 trial 之间靠 per-trial socket + nonce 隔离。
- 大文件/大目录设传输上限,超限返回错误而不是截断。

第一版 `StartProcess` 返回不支持;四核心工具都不用它。

## 8. API 缺口(内部、eval-only)

第一版需要的内部参数,不进 `api/spec`:

1. session 创建时携带 **bridge 绑定**:`{socket_path, nonce}`。缺一不可,`bridge` 后端对没有绑定的 session 拒绝创建。
2. **usage 导出**。公开 session API 只有消息级 `token_count`(`api/spec/domain/sessions/schemas.yaml:298`),没有 provider 的 input/output/cache 明细与成本。完整 usage 目前只在运行时可得。第一版用内部导出通道;若 spike 证明必须公开,作为"已证明的 API 缺口"记录进 #1055 并附证据。

不做任何投机性的公共 API 扩张。

## 9. 结果与溯源 schema,以及 fail-closed 的 pass 谓词

每条结果必须记录:

| 字段                                               | 来源                                                     |
| -------------------------------------------------- | -------------------------------------------------------- |
| `candidate_commit`                                 | 被测 stellad 的 git commit                               |
| `benchmark` / `dataset_version`                    | Harbor dataset slug + 版本                               |
| `task_id` / `run_id` / `trial_id`                  | Harbor trial 标识                                        |
| `model_config_digest`                              | provider + model + 采样参数的稳定哈希                    |
| `capability_profile_digest`                        | eval 能力档(启用工具、技能、助手包版本)的哈希            |
| `bridge_nonce`                                     | 本 trial 的 bridge 标识                                  |
| `bridge_ledger`                                    | bridge 侧记录的调用序列(类型、路径/命令摘要、时间戳)     |
| `stella_tool_calls`                                | Stella 消息流中的工具调用序列                            |
| `turn_terminal_state`                              | Stella session 的确认终态(completed / stopped / errored) |
| `tokens` / `cost_usd`                              | 内部 usage 导出;成本由价目表计算                         |
| `elapsed_sec`                                      | run() 墙钟耗时                                           |
| `workspace_diff`                                   | 容器内 workdir 的 `git diff` 或产物清单(经 bridge 导出)  |
| `harbor_verifier_result` / `harbor_exception_info` | post-trial 从 Harbor result.json 合并;两者独立字段       |
| `judge_diagnostics`                                | benchmark 原生判分输出,原样保留                          |
| `failure_class`                                    | 见 §10                                                   |
| `valid`                                            | 见下                                                     |

**pass 谓词(fail-closed)**:只有当下列全部为真,`reward = 1` 才可显示为 pass;否则记 `valid = false`,`failure_class = adapter`,reward 原样保留但不计入通过率:

1. `bridge_nonce` 与 session 绑定一致;
2. `stella_tool_calls` 中每一次 bash/read/write/edit 都能在 `bridge_ledger` 中找到对应条目(数量与顺序核对);
3. `turn_terminal_state` 已确认,且是在 verifier 开始之前;
4. `harbor_exception_info` 为空;
5. 能力档中禁用的工具在 `stella_tool_calls` 中出现次数为 0。

原始产物落 `dist/evals/`(已 gitignore)。

## 10. 失败分类:按顺序判定的决策树,不是五个并列标签

review 指出 v1 的五类既不互斥也不可判定(非零 bash 是模型可恢复的普通反馈,不该算产品失败;未实现 TTY 同时命中两类)。v2 改为**固定顺序、首个命中即停**的决策树,每一步只看确定性信号:

```
0. task 预筛(跑之前)          需要 TTY/交互进程、或依赖能力档禁用的工具
                               → task_mismatch。不进入分母,单独列出。
1. adapter / benchmark          bridge 起不来、绑定失败、pass 谓词任一不满足、
                               证据导出失败、Harbor exception_info 非空
                               → adapter。valid=false。
2. 环境 / 外部依赖              Stella 返回 provider 错误(429/5xx/断连/上下文超限)、
                               数据库不可达、bridge socket 中途消失(adapter 存活但容器死)
                               → environment。
3. Stella 产品失败              turn 以 errored 终态结束,且错误来自 Stella 运行时
                               (工具注册、技能加载、沙箱策略、内部 panic),
                               而非模型/provider。工具执行的非零退出、edit 不匹配
                               等由 agent loop 消化的反馈不算。
                               → product。
4. 模型能力                     turn 正常 completed,verifier 未通过
                               → model。
5. 通过                         turn 正常 completed,verifier 通过,pass 谓词满足
                               → pass。
```

**关键约束**:第 0 步是**跑之前**由 task 元数据与能力档判定的,不是事后挑失败样本;第 3 类与第 4 类都不得记为 pass;第 1 类的 `valid=false` 不进入通过率的分子也不进入分母,单独报告数量。

## 11. 凭据与清理

- 每 trial 一个 provisioned 用户与一个 bearer token(`/api/provisioned-users`)。**必须显式传 `expires_at = now + agent.timeout_sec + 余量`**:省略时默认 90 天(`api/spec/domain/provisioning/schemas.yaml:101`)。
- 模型 API key 由 Harbor `model_connection` 注入,不落镜像、不落仓库。
- 导出的日志与轨迹写盘前经现有 secret redaction 路径脱敏。
- **清理分两层**:
  - trial 内 `finally`:`deactivate` 用户、删除该 trial 的 agent(`DELETE /api/agents/{id}`)、关 bridge socket。
  - job 级 janitor:按 `external_id` 前缀扫描残留 provisioned 用户与 agent,job 开始与结束各跑一次,覆盖 adapter 被 kill 的路径。
- **诚实边界**:`deactivate` 只停用账户、删登录态、撤销 PAT(`internal/provisioning/service.go`),不删对话、消息、workspace。第一版接受这些数据残留在评测专用 Stella 实例里,由 job 结束后整机重置(重建 STELLA_HOME 与内嵌 PG 数据目录)兜底。**"不残留"是对凭据与运行中资源的承诺,不是对数据行的承诺。**

## 12. 资源预算与 VM 规格

单台专用 Debian 13 (trixie) VM,装 Docker,uv 装 Harbor。

- **并发按最坏 task 算,不按多数 task 算**。尾部 task 有 4 核 / 8GB(各 2 个),两个并发就是 8 核 16GB。stellad + 内嵌 PG + Harbor + Docker daemon 再算 2 核 4GB。
  - **8 核 16GB:保证 2 并发**,靠 Harbor 的 `-n` 限制。
  - **16 核 32GB:保证 4 并发**。推荐。
  - 若要按 task 资源加权准入,是 #1055 的事,第一版用固定并发。
- **磁盘:先拉全镜像再定,起步 500GB**。89 个 task 各带独立 `docker_image`,总量未测;`storage_mb` 最高 10GB/task 的可写层乘并发数;加内嵌 PG/WAL、日志、失败残留。要设 Docker GC 水位和日志上限,否则失败 trial 的残留会填盘。
- stellad 以非 root 专用用户运行,**不**加入 `docker` 组(D2 已不需要)。

## 13. 安全边界

- Stella 服务用户不再需要 docker 组。它能触达容器的唯一路径是 per-trial bridge socket,socket 由 adapter 创建、权限 0600、带 nonce,trial 结束即关闭。
- 这台 VM 仍是单一用途评测机:Stella 的 agent 可以在容器内执行任意命令,而 task 镜像来自第三方。不放生产凭据、不接生产数据库、不与其他服务共用。
- 本设计不试图在这台机器内部再建立可信边界。

## 14. 硬保险:禁止任何形式的回退

`bridge` 后端一旦启用:

- 没有 bridge 绑定的 session **拒绝创建**,不回退到 local / none / docker。
- `ResilientSession` 会在后端死亡时自动重建 session(`internal/agent/sandbox/session.go:189-195`)。重建必须重连**同一个 socket + 同一个 nonce**;连不上就硬失败,turn 以 errored 终态结束。
- adapter 进程死亡 = bridge 死亡 = 工具执行死亡。这是设计意图。
- `resolveToolExpression` 的 env 与消费结果的 `FileAccess` 必须来自同一个 `FileView`(`tools.go:36` 的注释已明确)。

**这些只保证"不换目标"。"首次绑定正确"由 §9 的 pass 谓词(nonce 一致 + 账本核对)保证。两者缺一不可。**

## 15. 范围外与停止条件

**范围外**:WorkBuddy 全量跑分、Terminal-Bench 基线成绩、SWE-bench 接入、任何新公共评测 API、多 agent / delegate / goal 的评测、按 task 资源加权准入。

**停止条件**:若现有接口无法提供稳定的 provision、执行、证据导出三件事,立即停止,只记录最小的、有证据支撑的 API 缺口,不扩张公共 API 面。

## 16. 验证计划

1. `bridge` 后端契约测试:对一个手工起的容器(经一个最小 `BaseEnvironment` 实现)验证 `Exec` 与六个 `FileAccess` 方法,含中文路径、大文件、二进制、**外跳 symlink 必须被拒绝**。
2. 单个 Terminal-Bench task 端到端跑通:install / run / trace 导出 / 清理闭环。**这是适配器验证,不是成绩。**
3. **超时路径**:人为把 deadline 设短,验证 stop 被调用、终态被确认、verifier 开始时容器内无 Stella 写入。
4. **fail-closed**:人为让 nonce 不一致 / 让一次工具调用绕过 bridge,验证结果记为 `valid=false` 而不是 pass。
5. WorkBuddy Code 域三个分层 smoke:简单、中等、失败边界。官方 judge 必须能消费 Stella 的 harness run 并产出原生诊断。**同时验证 §2 的假设**:照 `HARNESS_AUTHORING.md` 实写一份 harness 配置。
6. 故意制造一例产品失败(例如让一个启用的技能加载失败)与一例环境失败(例如 provider 返回 429),验证两者按 §10 落入不同类、且都不记为 pass。

## 17. 未决问题

1. WorkBuddy 的 harness v3 契约(preset、split-mount、`local_proxy` 与"模型直连"的关系)未实测。
2. 完整 dataset 与公开子集的差异需用 `harbor datasets` 复核。
3. `StartProcess` 的实际需求量未知;需在跑通若干 task 后统计有多少 task 因缺它落入 `task_mismatch`。
4. §7.3 的上下文单向同步是否足够,取决于自带 `AGENTS.md` / `.agents/skills` 的 task 比例。
5. `BaseAgent` vs `BaseInstalledAgent`:若 WorkBuddy 接受前者,D5 可简化。

## Deviations

`Binding` 的发布责任由 adapter 改为 `stella-eval-agent`。原因是 binding
文件名必须是 provisioned user 的 UUID,而该 UUID 只能在 driver 调用
`/api/provisioned-users` 后获得。adapter 仍创建 socket 和随机 nonce,并把
`{socket, nonce, workdir, home, temp_dir, path}` 模板传给 driver;driver 在
provision 成功、创建 agent/session 前以临时文件加 rename 原子发布
`<binding-dir>/<user-id>.json`,并在 finally 清理。wire protocol、HTTP API 和
nonce 的 fail-closed 交叉核对均未改变。
