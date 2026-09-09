---
title: MCP 服务器
---

## MCP 服务器的作用

Stella 连接外部 [Model Context Protocol](https://modelcontextprotocol.io) 服务器，
并把它们的工具提供给当前 turn。声明直接保存在普通文件中。独立服务器使用
`mcp/<name>.json`，包内服务器属于该包的完整文件树。

Stella 只通过 HTTP 传输作为 MCP 客户端：

- `streamable_http`，可流式 HTTP 传输。
- `sse`，HTTP 加 Server-Sent Events。

不支持本地 `stdio` 服务器。Stella 不会为模型声明启动本地进程。端点必须解析到公网
地址；本地开发时只有设置 `STELLA_MCP_ALLOW_PRIVATE_ENDPOINTS=1` 才允许回环和私网 URL。

## 作用域和选择

MCP 文件使用与 Skill 相同的四种作用域：

| 作用域         | 可见范围              |
| -------------- | --------------------- |
| `system`       | 所有用户和 Agent      |
| `system_agent` | 某个 Agent 的所有用户 |
| `user`         | 某个用户的所有 Agent  |
| `user_agent`   | 一个用户和一个 Agent  |

对于包，Stella 只在四个有明确类型的资源根中按以下顺序选择最具体的完整包：

```
user_agent > user > system_agent > system
```

胜出的包会整体替换更宽作用域中的包，其中的 MCP、Skill、CLI 和环境声明一起解析。
独立副本没有来源链接，不会跟随来源后续编辑。独立 MCP 文件按名称选择，不会和同名
包内声明合并。

`settings.json` 可以停用服务器，也可以添加只有管理员能设置的 `forbidden` 和
`disabled_tools` 限制。被停用的胜者会遮蔽更宽作用域。个人设置不能解除管理员禁止项。

Stella 在接纳 turn 时捕获声明。turn 中途编辑文件会在下一轮生效，本轮继续使用已捕获
的端点、工具目录和凭据引用。目录刷新会在后续轮次读取当前文件，不再依赖独立的注册记录。

## 认证

声明只保存凭据引用和公开连接字段。Bearer 令牌、OAuth 令牌、刷新令牌和客户端密钥
会独立加密保存在授权存储中。无需认证的服务器不需要密钥，即使没有配置凭据存储也可用。

系统资源可以使用 `shared`，由所有可见用户共享一个授权，也可以使用 `per_user`，
让每个用户分别授权。用户和 user-agent 声明不能请求 shared 凭据。声明的作用域和凭据
模式决定使用哪个 Vault tuple，绝不会回退到其他用户的授权。

## OAuth 连接

OAuth 2.1 服务器应对当前文件声明执行 **Connect**。Stella 会发现授权服务器，使用
Authorization Code 和 PKCE，并把授权独立保存，不写回声明文件。回调地址是
`<STELLA_BASE_URL>/api/mcp/oauth/callback`，浏览器必须可以访问它。

**Disconnect** 是独立的本地撤权操作。它撤销匹配的已存 grant、轮换 generation、关闭
匹配连接，并阻止迟到的回调或刷新；不承诺远端 provider 一定撤权。删除或编辑 MCP 文件
不会执行 Disconnect。修改端点或其他认证身份会生成新的目标，不能复用旧授权；普通 Skill
或包内容编辑会保留授权。

## 状态和探测

Probe 读取当前声明并执行一次远程发现。结果只是当前目标和凭据所有者的观测，不是持久化
MCP registration，也不是安装记录。后续 turn 在目录不存在或过期时可以再次探测。

| 状态         | 含义                       |
| ------------ | -------------------------- |
| `unknown`    | 当前目标没有成功发现结果   |
| `ok`         | 最近一次发现列出了工具     |
| `error`      | 端点或发现失败，原因已脱敏 |
| `needs_auth` | 服务器拒绝了匹配凭据       |

探测失败不会隐藏同包中的其他服务器或 Skill。工具目录只是观测，后续 turn 可以复用或
刷新；MCP 连接属于当前 Session，会随 Session 关闭。

## 工具权限

每个远程工具都可以在四种作用域单独启用或停用。管理员禁止项优先于个人启用。包的启用
状态同时控制其中所有 MCP 声明和 Skill，工具开关不能重新启用停用的包。

Native 工具是独立的系统能力。包或 MCP 服务器即使同名，也不会获得 Native 权限；删除
MCP 文件也不会停用 Native 路由。

## 市场

Web UI 可以浏览官方 [MCP Registry](https://registry.modelcontextprotocol.io)，把支持的
HTTP 声明复制到可写作用域。市场数据只提供初始声明，保存前仍需检查端点、认证类型、
凭据引用和作用域。注册表元数据不会创建父级 registration，也不会建立自动更新链接。

## 编辑文件

使用 MCP 页面或文件 API 创建、读取、编辑、复制和删除独立声明。包内 MCP 通过包文件
视图编辑。完整包复制后独立存在。使用过期摘要编辑时会返回冲突；Shell 直接写文件仍
不提供事务保证。

删除声明会让它不再参与后续选择，但不会立即撤销 OAuth grant 或清除资源字节。需要撤销
本地访问时，先对目标执行 Disconnect，再删除文件；不承诺远端 provider 一定撤权。

## 故障排除

| 现象           | 含义                             | 处理                                           |
| -------------- | -------------------------------- | ---------------------------------------------- |
| `needs_auth`   | 匹配的授权被拒绝                 | 重新连接 OAuth，或更新 bearer 密钥后重新 Probe |
| `error`        | 端点或发现失败                   | 从服务端检查端点，下一轮重试                   |
| 服务器消失     | 更窄作用域停用了它或整体替换了它 | 检查胜出作用域和 `settings.json`               |
| 工具缺失       | 当前目录不包含该工具             | 编辑声明，或等待下一次发现                     |
| `stdio` 不运行 | 不支持本地进程传输               | 通过允许的 HTTP 传输暴露服务器                 |
