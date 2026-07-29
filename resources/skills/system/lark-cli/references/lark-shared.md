---
name: lark-shared
version: 2.0.0
description: "飞书/Lark CLI 共享基础（Stella 适配版）：说明 Channel 应用预配置、lark-cli 原生设备授权、身份选择、scope 与权限错误处理。"
---

# lark-cli 共享规则

本技能指导你在 Stella 中通过 lark-cli 操作飞书/Lark 资源。

## Stella 与 lark-cli 的边界

- Stella 在个人会话启动时，使用当前 Agent 唯一启用的飞书/Lark Channel App ID、App Secret 和 brand，可信地初始化 lark-cli 应用配置。
- 应用配置、加密 keychain 和用户 token 均隔离在“当前员工 × 当前 Agent”的工作区；不同员工或不同 Agent 不共享登录态。
- 模型不得运行 `lark-cli config init`、索要 App Secret 或读取 lark-cli keychain。若提示应用未配置，应让管理员检查当前 Agent 的 Channel 配置。
- Stella 的通用 `oauth` tool、`/auth feishu` 和 Credentials 中的 Feishu/Lark OAuth 连接不为 lark-cli 授权，不能作为恢复路径。
- 群聊会话不建立个人 lark-cli 登录态。需要用户身份时，请用户转到与该 Agent 的私聊。

## 原生用户授权状态机

执行任何 `--as user` 操作前先运行：

```bash
lark-cli auth status
```

若未登录、token 被撤销，或当前用户缺少本次操作所需 scope：

1. 从模块文档或接口错误中确定完成当前任务所需的**最小用户 scope**，不要请求 `all`，也不要为了省事全选权限。
2. 启动非阻塞设备授权：

   ```bash
   lark-cli auth login --scope "<scope1 scope2>" --no-wait --json
   ```

3. 把返回的 `verification_url` 原样发给用户；同时生成二维码：

   ```bash
   lark-cli auth qrcode "<verification_url>" --output lark-auth.png
   ```

   链接必须保持不变，不得自行编码、解码或拼接参数。向用户展示链接和二维码后结束本回合，不要阻塞等待。

4. 用户明确回复已完成后，用同一次返回的 `device_code` 续接：

   ```bash
   lark-cli auth login --device-code "<device_code>"
   ```

5. 再次运行 `lark-cli auth status`，确认当前 user 是发起请求的员工，再继续原任务。

设备码过期或续接失败时，原样说明错误，并重新从第 1 步启动一次新授权；不得并行生成多个设备码。

## 身份选择

| 身份          | 使用方式    | 适用场景                                              |
| ------------- | ----------- | ----------------------------------------------------- |
| user 用户身份 | `--as user` | 员工个人资源、需要员工本人作为发起人/操作者的业务流程 |
| bot 应用身份  | `--as bot`  | 明确允许应用身份、且不依赖用户私有资源的后台操作      |

- 默认使用 `--as user`。
- 审批创建、个人云盘上传、邮箱、个人日历和“以我名义”操作必须使用 user。
- 不得因 user 未登录、token 过期或 scope 不足而切换 bot。
- Bot 不能代表员工成为审批发起人，也通常看不到员工私有资源。

## 权限错误处理

先保留并检查接口、错误码、`message`、`permission_violations`、`console_url` 和 `hint`：

- **应用 scope 未开通**：停止调用，把缺少的 scope 与 `console_url` 原样交给管理员。管理员只开通当前功能需要的 scope并发布应用版本。
- **用户 token 未授权该 scope**：使用上面的原生设备流，仅增量请求缺少的用户 scope。
- **token 过期或撤销**：先让 lark-cli 自行刷新；刷新失败再走一次原生设备流。
- **身份不支持**：停止并说明接口支持的身份，不切换身份绕过。

任何 API 失败后都不得悄悄更换身份、直接调用飞书 API、改用 curl/Python 绕过，或用新的幂等键重试写操作。`--dry-run` 只验证请求构造，不代表真实权限可用。

## 更新检查

lark-cli 命令执行后，如果检测到新版本，JSON 输出中会包含 `_notice.update` 字段（含 `message`、`command` 等）。

**当你在输出中看到 `_notice.update` 时，完成用户当前请求后，主动提议帮用户更新**：

1. 告知用户当前版本和最新版本号
2. Stella 固定并分发 lark-cli 与内置 skills 版本；不要在沙盒里自行运行 npm/npx 更新。
3. 把版本提示交给 Stella 管理员，由管理员升级 Stella 后统一验证和发布。

**规则**：不要静默忽略更新提示。即使当前任务与更新无关，也应在完成用户请求后补充告知。

## 安全规则

- **禁止输出密钥**（appSecret、accessToken）到终端明文。
- **写入/删除操作前必须确认用户意图**。
- 用 `--dry-run` 预览危险请求。
- 同一业务写操作使用稳定的幂等键；失败后不得换键重试。
