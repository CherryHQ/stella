# Sandbox Secrets 注入重设计:绑定收紧 + 声明式 exec 注入 + egress 白名单

- **日期**: 2026-07-04
- **状态**: Draft,待评审
- **范围**: sandbox env 组装(`internal/agent/sandbox/env.go`)、vault 注入语义、Bash/exec 工具链、`NetworkPolicy`。不含 Phase 3 egress proxy 的实现(仅立项展望)。

## 1. 问题

Sandbox 内的工具依赖 secrets 才能工作,当前做法是把凭证以环境变量注入 sandbox 进程。agent 完全控制 sandbox 内的命令执行,`env`、`cat /proc/self/environ`、子进程继承都能读到全部值。叠加 prompt injection,泄露链路是:

```
恶意内容进入上下文 → agent 读 env → curl 把值发到攻击者端点
```

当前实现放大了这个风险面,三个具体问题(按严重度排序):

1. **Vault 全量注入**。`buildSandboxEnv` 把用户 vault 的所有 entry `maps.Copy` 进每个 sandbox(`internal/agent/sandbox/env.go:173`),与本次 session 用不用无关。跑纯写作任务的 agent 也持有用户全部 API key。
2. **Legacy `STELLA_TOKEN` 绕过 API-scope 检查**。vault 注入的 90 天自动 token(`internal/auth/token_service.go:88`)被 `Enforce` 豁免 scope 校验(`internal/credential/enforce.go`,`KindLegacyStellaToken`;handler 层 ownership/admin 检查仍在)。泄露即近乎全量 API 面、90 天有效。
3. **Egress 只有两档**。`NetworkMode` 仅 `disabled` / `allow_all`(`pkg/sandbox/policy.go:12-17`)。泄露的最后一跳没有任何拦截。

## 2. 威胁模型与诚实边界

**先说清楚天花板**:只要工具在 sandbox 内执行,"agent 看不见 secret"就不可能——agent 是执行命令的主体,任何进入 sandbox 的字节它都能读出来。以下手段全部是安慰剂,本设计不采用:

- 从模型 prompt 里隐藏 env(agent 跑一条 `env` 就绕过);
- 用文件挂载替代环境变量(同样可读);
- transcript redaction 作为主防线(现有 `tracehook.redactSecrets` 是 best-effort 正则,自己也标注了不是保证)。

因此目标不是"不可见",而是四件可达成的事:

| 目标         | 手段                                    |
| ------------ | --------------------------------------- |
| 降低暴露面   | 默认不注入,显式绑定才注入               |
| 降低泄露价值 | 短时效、窄 scope 的派生凭证             |
| 可审计可拦截 | 每次使用留审计点,高价值 secret 可挂批准 |
| 掐断出口     | egress 域名白名单                       |

真正的"不可见"只有 credential-injecting egress proxy 能提供(Phase 3),且**只对已知 provider 有效**——代理无法知道用户自定义脚本把 key 塞在哪个 header 里,所以用户 vault secrets 的天花板就是上表四项。

## 3. 现状(注入路径速查)

`buildSandboxEnv`(`internal/agent/sandbox/env.go:132`)单点组装,优先级从低到高:

1. vault secrets 全量作 base(`env.go:173`;group session 例外,不加载人类 vault,`env.go:137`);
2. OAuth 派生 token 经 `injectSessionEnv`(`env.go:237`,plugin `SessionEnvSpec` 声明,raw OAuth bundle 在 `env.go:193` 被显式剥除——这是本设计要推广的好先例);
3. scoped 短时效 `STELLA_TOKEN`(`CreateScopedToken`,session 绑定 JWT,`RefreshEnv` 轮转);
4. runner `ProcessEnv` 与 mise 路径。

组装结果进 `pkgsandbox.Policy.Env`,三个 backend(docker/local/none)在**每次 exec** 时把它合入子进程 `cmd.Env`(`plugins/sandbox/*/session.go` 的 `buildEnv`/`mergeEnv`)。工具统一走 sandbox session 的 exec 路径(如 `internal/tools/bash.go:57` 的 `ExecOptions{Env}`),不直接 `os.Getenv`。

**关键抓手**:注入发生在 host 侧、逐次 exec——所以注入时机可以从"session 启动时"下移到"单次 exec 时",由 host 按声明决定每条命令带什么。这是 Phase 1b 的架构基础,不需要新机制。

## 4. 设计

### Phase 1a — 绑定收紧:默认不注入

vault entry 增加注入语义(复用现有 per-user / per-agent scope 骨架,`internal/vault/service.go:283` `LoadEnvForAgent`):

- 新 entry 默认 **不注入**(只存储,可被 Phase 1b 声明式使用);
- 用户可显式绑定到 agent / project,或标记 `inject: always`;
- `buildSandboxEnv` 的 vault base 从"全量 copy"改为"仅绑定交集"。

系统推断不出用户随手存的 `FOO_API_KEY` 给哪个脚本用——**声明责任属于唯一知道用途的人**,即用户,声明时机在存入时。

**迁移**:存量 entry 与新建 entry 同等对待,默认 **不注入**(无回填豁免)。理由:尚无外部用户,存量豁免只会让真正存了敏感 key 的库无限期保留"`env` 一条命令全量外带"的暴露面;需要旧行为的条目由用户在 Credentials 页显式开启 `inject: always` 或绑定。

### Phase 1b — 声明式 exec 注入(长尾主路径)

未绑定的 secret 不进 session env,通过"运行前声明"按次注入。明确否决朴素 JIT(跑一次→失败→取 key→重跑)——那是把摩擦转嫁给每一次执行。流程:

1. **清单可见,值不可见**:session 上下文里给 agent 可用 secret 的名字+描述(`FOO_API_KEY — foo CLI 部署用`),来源即 vault entry 元数据。等价于 system prompt 里的工具清单。
2. **exec 时声明**:Bash 工具增加 `secrets` 参数(串行地,`stella run --secrets FOO_API_KEY -- <cmd>` 提供同能力)。host 侧解密后仅注入**该次 exec 的子进程** env,不落 session env。
3. **审计与拦截**:每次声明使用记审计(谁、哪个 session、哪个 secret、什么命令);entry 可配策略——高价值 secret 首次使用弹用户批准(复用 permission prompt 交互)。
4. **失败兜底**:agent 忘记声明→命令失败一次,报错信息附"可用 secrets 及声明方式",一轮自纠。与写错任何工具参数同级,不是新的错误类别。
5. `stella secret get NAME` 保留为逃生舱(走 scoped `STELLA_TOKEN` 回调链路,同样记审计),不是主路径。

诚实声明:agent 依然可以 `--secrets FOO -- sh -c 'echo $FOO'` 取到值。这一层买的是**默认干净的 env**(被动泄露面归零:`env` dump、transcript、无关子进程继承全部干净)+ **显式声明带来的审计点和拦截点**,不是不可见。

### Phase 1c — 凭证降值

- **砍掉 legacy `STELLA_TOKEN`**:停止注入 90 天 auto token,scoped session token 成为唯一形态;移除 `KindLegacyStellaToken` 的 scope 豁免。机制(签发、轮转)已齐备,主要是清理与兼容排查。
- **OAuth 派生凭证短时效化**:照抄 raw-bundle-剥除的先例再进一步,provider 支持的场景换成短时效窄 scope 形式(如 GitHub App installation token:1 小时过期、可限 repo)。泄露从"灾难"降为"一小时窗口内的有限 scope"。

### Phase 2 — Egress 白名单(承重墙)

`NetworkPolicy` 增加 `allowlist` 模式:按 session 绑定/声明的工具与 secrets 推导可达域名集合,叠加用户/管理员追加项。没有这一层,Phase 1 是半成品——泄露最终需要一个出口,掐出口比藏 secret 有效。

实现按 backend 分述(docker 网络层可控性最好;local/none 的可行性与降级语义需要在实现前单独评估,见开放问题)。

### Phase 3 — Credential-injecting egress proxy(立项,不排期)

sandbox 只持有无价值的 session placeholder;出站流量经 stellad 侧代理,代理对白名单域名把 placeholder 替换为真实凭证(AWS metadata service / CyberArk Secretless 同款思路)。这是唯一让 secret 物理上不进 sandbox 的方案,代价:

- HTTPS 需要代理终结 TLS(sandbox 信任内部 CA),或工具改走 `http://proxy/github/...` 路由——对 `gh`/`git` 等现成 CLI 侵入性大;
- 只覆盖已知 provider,救不了用户自定义 secrets。

建议 Phase 1+2 落地、且确有高价值已知-provider 凭证场景后再评估。另一条旁路:stella 自有的高价值操作不下发第三方凭证,工具经 `STELLA_TOKEN` 回调让 stellad 代为调用(MCP client host 侧已是此模式),可覆盖部分场景但救不了 sandbox 内通用 CLI。

## 5. 否决的替代方案

| 方案                        | 否决理由                                                    |
| --------------------------- | ----------------------------------------------------------- |
| 朴素 JIT(失败后取 key 重跑) | 每次执行都付一轮失败;声明式 exec 注入在同等安全性下一次跑通 |
| 从 prompt 隐藏 env          | `env` 一条命令绕过,纯安慰剂                                 |
| 文件挂载替代 env            | 可读性相同,徒增复杂度                                       |
| Redaction 作主防线          | 正则 best-effort,现有实现自己标注了不保证;保留为纵深,不承重 |
| 系统自动推断 secret 依赖    | 用户自定义 secrets 的用途系统不可知;推断必然漏,漏就回到全量 |

## 6. 分期与验收

| 阶段 | 内容                           | 验收                                                                 |
| ---- | ------------------------------ | -------------------------------------------------------------------- |
| 1a   | 绑定收紧,默认不注入            | 存量与新 entry 均不出现在无绑定 session 的 `env` 输出;显式开启后可见 |
| 1b   | 声明式 exec 注入 + 审计        | 未声明的命令 env 干净;声明后一次跑通;审计记录可查;批准策略生效       |
| 1c   | 砍 legacy token + 短时效 OAuth | sandbox 内 `STELLA_TOKEN` 均为 scoped;legacy kind 从 resolver 移除   |
| 2    | egress allowlist               | 白名单外域名连接被拒;白名单内工具正常                                |

1a/1b/1c 可并行,2 独立。每阶段单独 issue + PR。

## 7. 风险与开放问题

- **1b 的交互成本**:secrets 清单占上下文、声明参数增加工具面。缓解:清单只含名字+一行描述;绑定(1a)覆盖高频场景,声明只走长尾。
- **legacy token 兼容面**:是否有外部脚本依赖长效 token 直调 API?砍之前需扫描使用面,必要时给 PAT 迁移路径。
- **egress 白名单在 local/none backend 的可实施性**:无网络命名空间时如何拦截(bwrap?pf/iptables?降级为审计模式?)——Phase 2 实现前需单独 spike。
- **域名推导的完备性**:工具依赖的 CDN/重定向域名难以枚举,白名单过紧会制造大量失败。需要"拒绝时的可观测反馈 + 用户一键放行"配套。
- **group session**:现有 D9 隔离(不加载人类 vault)保持不变,1a/1b 仅作用于单人 session。

## 8. Refs

- 注入单点: `internal/agent/sandbox/env.go:132,173,193,237`
- Scope 豁免: `internal/credential/enforce.go`、`internal/credential/principal.go`(`KindLegacyStellaToken`)
- NetworkPolicy: `pkg/sandbox/policy.go:12-17`
- Vault scope 骨架: `internal/vault/service.go:278,283`
- Token 签发/轮转: `internal/auth/token_service.go:88,196`、`internal/agent/sandbox/refresh.go`
- Redaction 现状: `internal/observability/tracehook/tool.go:42`
