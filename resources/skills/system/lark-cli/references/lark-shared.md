---
name: lark-shared
version: 2.0.0
description: "飞书/Lark CLI 共享基础（Stella 适配版）：说明用户级原生配置、身份选择、scope 与权限错误处理。"
---

# lark-cli 共享规则

本技能指导你在 Stella 中通过 lark-cli 操作飞书/Lark 资源。

## Stella 与 lark-cli 的边界

- Stella 只负责安装 lark-cli，并把它的原生配置和数据目录指向当前用户的共享沙盒目录。
- Stella 不注入 lark-cli 凭据，不初始化应用，不绑定 Channel 应用，也不替用户选择认证方式。
- 用户在一个私聊 Agent 工作区完成的 lark-cli 配置和登录，可在该用户的其他 Agent 工作区复用。
- Stella 的通用 `oauth` tool、飞书 Channel 凭据和飞书登录都不能授权 lark-cli。
- 群聊会话不加载任何用户的个人 lark-cli 状态。需要个人身份时，请用户转到私聊。

配置、登录、登出和 profile 管理都使用 lark-cli 自己的命令，并遵循用户当前选择：

```bash
lark-cli config --help
lark-cli auth --help
lark-cli profile --help
```

不要替用户猜测 App ID、App Secret、brand、profile 或授权 scope。

## 非交互授权流程

授权分为“配置应用”和“登录用户”两步。每个会阻塞等待用户的步骤都必须拆成两轮：先拿到验证链接并发给用户，立即结束当前回复；用户确认完成后，再继续收尾。禁止在同一轮里发出链接后继续阻塞等待。

### 1. 配置应用

当 `lark-cli config show` 表明尚无可用配置时：

```bash
lark-cli config init --new --force-init > /tmp/lark-init-url.txt 2>&1 &
```

只等到 `/tmp/lark-init-url.txt` 出现验证链接，读取并发给用户，然后结束当前回复，不等待授权完成。用户回来确认后，运行：

```bash
lark-cli config show
```

### 2. 登录用户

先用非阻塞模式获取验证链接和 `device_code`：

```bash
lark-cli auth login --no-wait --json --recommend | python3 -c '
import sys, json
data = json.load(sys.stdin)
print(data["verification_url"])
print("device_code:", data["device_code"])
'
```

保留返回的 `device_code`，只把 `verification_url` 发给用户，然后结束当前回复。用户确认授权后，运行：

```bash
lark-cli auth login --device-code "<device_code>"
```

`device_code` 有效期为 10 分钟；过期后重新发起登录。`--recommend` 用于请求推荐权限，避免不必要的逐项审批。

## 身份选择

| 身份          | 使用方式    | 适用场景                                              |
| ------------- | ----------- | ----------------------------------------------------- |
| user 用户身份 | `--as user` | 员工个人资源、需要员工本人作为发起人/操作者的业务流程 |
| bot 应用身份  | `--as bot`  | 用户已明确配置应用身份，且操作不依赖个人私有资源      |

- 尊重用户当前配置的默认身份。
- 审批创建、个人云盘上传、邮箱、个人日历和“以我名义”操作必须使用 user。
- 不得因 user 未登录、token 过期或 scope 不足而切换 bot。
- Bot 不能代表员工成为审批发起人，也通常看不到员工私有资源。

## 权限错误处理

保留并检查接口、错误码、`message`、`permission_violations`、`console_url` 和 `hint`：

- **应用 scope 未开通**：停止调用，把缺少的 scope 与 `console_url` 原样交给用户或应用管理员。
- **用户 token 未授权该 scope**：说明缺少的 scope，由用户决定是否通过 lark-cli 原生授权补充。
- **token 过期或撤销**：先让 lark-cli 自行刷新；失败后说明状态，由用户决定是否重新登录。
- **身份不支持**：停止并说明接口支持的身份，不切换身份绕过。

任何 API 失败后都不得悄悄更换身份、直接调用飞书 API、改用 curl/Python 绕过，或用新的幂等键重试写操作。`--dry-run` 只验证请求构造，不代表真实权限可用。

## 更新检查

lark-cli 命令执行后，如果 JSON 输出包含 `_notice.update`：

1. 完成当前请求后告知用户当前版本和最新版本。
2. Stella 固定并分发 lark-cli 与内置 skills 版本；不要在沙盒里自行运行 npm/npx 更新。
3. 把升级请求交给 Stella 管理员统一验证和发布。

## 安全规则

- 禁止输出 App Secret、access token、refresh token 等密钥。
- 写入或删除操作前确认用户意图。
- 用 `--dry-run` 预览危险请求。
- 同一业务写操作使用稳定的幂等键；失败后不得换键重试。
