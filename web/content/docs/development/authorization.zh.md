---
title: 授权
---

> 本节面向为 Stella 贡献代码的开发者。

Stella 中每个受保护的操作都从一个可信的 `authz.Authority` 开始——调用方（用户、被委派的 agent、group 回合或具名系统 worker）的已验证身份。传输层从会话声明或运行时上下文派生它；模型提供的参数绝不构成身份。之后发生什么，取决于资源。

这里有两种执行机制，选错是本领域最常见的错误。

## 两种机制

**由策略支撑的资源**开一次 `authz.Authorizer` evaluation，并据此决定一个类型化的 `authz.Request`。当资源存在必须由策略表达的真实区分时使用它：多个持久 scope、管理员管理的层级、按角色的差异，或需要运营者自定义策略。Agent、Session、Workspace、Goal、Workflow、Scheduler、Skill、Vault 均由策略支撑。它们的内建规则位于 `internal/authz/policy`，且每个资源在该包的 activation 目录中标记为 active。

**由归属/能力支撑的资源**把 Authority 绑定到域 `Access` 对象上，并用按用户限定的持久查询强制边界——完全没有策略 evaluation。当资源是单一、粗粒度的按用户能力、没有 scope、管理员或角色区分时使用它：把它交给策略引擎只会写出四条复制粘贴的规则，且永远只说“owner 可操作自己的”。Connections、Email、Share、Recally 由能力支撑。它们的目录项保持 **inactive**，故为它们误写自定义策略会 fail closed。

不要“为了对称”而新增策略资源。如果你唯一会写的规则是“owner 可操作自己的”，那就该用能力机制。反过来，如果你发现自己在域内手写 scope 或管理员检查，那就该用策略机制。

## 资源矩阵

| 资源                | 机制      | 执行者                                                                   |
| ------------------- | --------- | ------------------------------------------------------------------------ |
| Agent               | 策略      | `agentaccess.Service` + 内建规则                                         |
| Session / Workspace | 策略      | `agent/session/access.Service`                                           |
| Goal                | 策略      | `goal.Service`（持久 worker authority）                                  |
| Workflow            | 策略      | `workflow.Service`                                                       |
| Scheduler           | 策略      | `scheduler.Service`（system/plugin job 隐藏）                            |
| Skill               | 策略      | `skillaccess.Service`（四个 scope）                                      |
| Vault               | 策略      | `vault.Service`（user/user_agent/system/system_agent + agent-read 门禁） |
| Connections         | 归属/能力 | `connections.Service.Access`——OAuth bundle/flow 以用户为键               |
| Email               | 归属/能力 | `email.Service.Access`——配置存于用户 vault 命名空间                      |
| Share               | 归属/能力 | `share.Service.Access`——`WHERE user_id = ?` + os.Root 工件               |
| Recally             | 归属/能力 | `recally.Service.Access`——按 uid 限定的 store                            |

公开分享内容两者皆非：它是一个能力 URL（见下方配方）。

## 配方

### 授权一个端点（由策略支撑）

从已验证的会话声明派生 Authority，开一次 evaluation，决定资源：

```go
authority, err := info.authority()      // 来自请求的 AuthInfo
acc, err := s.vaultSvc.Begin(r.Context(), authority)
// acc 的方法在单一版本内决定 ResourceVault。
```

### 授权一个集合（由策略支撑）

list 是一个决策；逐行可见性是**同一** evaluation 内的第二个决策。先决定集合的 `ActionList`，再用每行加载出的事实构造 `ActionRead` 请求过滤每一行。绝不信任调用方提供的 `is_owner`；在 PEP 处由加载的行与 Authority 派生它。

### 授权一个持久 worker（由策略支撑）

worker 没有实时请求。从持久可信状态重建 Authority——`agentaccess.WorkerAgentAuthority(ownerID, agentID)`——并在每次动作时重新决策。绝不持久化决策；持久化事实并重新派生。

### 折叠跨域门禁（由策略支撑）

当一个决策需要另一资源的门禁（例如 agent 作用域的 vault 操作还须证明对该 agent 的读权限）时，复用已打开的 evaluation：`agents.AuthorizeWithin(ctx, eval, authority, agentID, authz.ActionRead)`。不要再开一次 `Begin`——那会在不同版本上决策。

### 授权一个用户能力（由归属/能力支撑）

绑定 Authority 一次；捕获用户；把每个查询都限定到它：

```go
func (s *Service) Access(authority authz.Authority) (*Access, error) {
	if s == nil {
		return nil, fmt.Errorf("service unavailable")
	}
	if !authority.Valid() {
		return nil, authz.ErrForbidden        // 无效身份
	}
	userID := string(authority.Actor().UserID())
	if userID == "" {
		return nil, authz.ErrUnauthenticated  // 有效但无用户（例如系统 agent）
	}
	return &Access{svc: s, userID: userID}, nil
}
```

先行拒绝无效或无用户的 Authority，让每个方法都能假定存在真实的行为用户。通过把持久查询限定到捕获的 `userID` 来强制归属——外来行直接不存在，绝不泄露。被委派的 agent 以其用户身份行事（这些能力在用户的各 agent 间共享），因此捕获用户，而非 executor agent。

这些域还有两项额外义务：

- **以父为键的写入。** 仅以父 id 为键的表（recally 文章正文、feed entry）不能信任“已加载父对象”的调用方。在写入内部以 uid 限定加载父对象，使外来父对象在任何变更前即不存在。
- **工作区限制。** Share 工件读取必须停留在行为 agent 的工作区内：agent 作用域的行为方受限于其绑定 agent，且文件通过 `os.Root` 读取，故 symlink 替换无法逃逸。

### 服务一个公开能力 URL

公开分享视图没有会话、没有 Authority。它仅凭一个不可猜测的 token 授权：以 token **哈希**查找分享，遵守其过期时间，绝不接受裸 id。这完全在任何 `Access` 之外。

```go
share, err := s.q.GetShareByTokenHash(r.Context(), share.TokenHash(token))
// 没有 Authority；token 哈希 + 过期时间就是全部能力。
```

### 调用原始 Service 方法（仅限可信调用方）

原始 `Service` 方法（被 `Access` 包装的那些）完全跳过身份，只能从没有实时用户请求、有据可查的宿主侧路径调用：

- **OAuth 回调与令牌刷新**——以持久的 flow/user 为键，而非请求。
- **Recally 启动回填**——没有调用方的维护性扫描。
- **Vault 宿主侧管道**——MCP、OAuth、邮箱配置、频道配置、沙箱环境加载器、密钥发放，作为宿主（而非用户）读写凭据。

如果新的调用点不属于这些，改为经 `Access`/`Begin` 路由。
