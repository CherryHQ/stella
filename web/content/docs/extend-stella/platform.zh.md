---
title: Platform API
description: 插件代码可用的作用域主机服务参考。
---

## Platform 为什么存在

`Platform` 是通过能力上下文传递的插件作用域服务表面。

它的存在解决了两个问题：

- 插件代码应该只能看到被允许使用的服务
- 主机内部应该保持内部状态

Stella 不会把全局服务容器交给插件，而是通过 `ToolContext`、`RuntimeContext` 和 `AdminContext` 这样的类型化上下文来传递 `Platform`。

## 声明能力

`Platform` **不是**环境变量。插件只能通过在 `PluginInfo.RequiredCapabilities` 中声明来访问主机端口。主机：

- **授予**仅声明的能力，
- **验证**在密封时每个声明的能力都由一个注入的主机服务支持（未声明或缺乏支持的能力会导致闭合失败），并且
- **暴露**一个插件作用域的 `Platform`，其对未声明能力的访问器返回 `nil`。

使用类型化的 `pkgplugins.Capability` 值声明你需要的内容，例如一个托管的通道运行时：

```go
Meta: pkgplugins.PluginInfo{
    // ...
    RequiredCapabilities: []pkgplugins.Capability{
        pkgplugins.CapabilityChannelPlatform,
        pkgplugins.CapabilityLogger,
        pkgplugins.CapabilityRuntimeLookup,
    },
},
```

调用未声明的访问器返回 `nil` — 始终 nil 检查服务（`if svc := ctx.Platform.Notifier(); svc == nil { ... }`）而不是假设环境访问。这些声明只能来自静态 Go 注册；manifest 插件不能声明或获得 `Platform` 能力。

## 可用服务

`Platform` 暴露（每个都由匹配的 `Capability` 门控）：

- `Logger()`
- `ConfigStore()`
- `StateStore()`
- `Scheduler()`
- `Notifier()`
- `Auth()`
- `RuntimeLookup()`
- `ChannelPlatform()`
- `ReflectPlatform()`

这些中的一些是通用的并对许多插件安全的。一些专门针对特定的运行时类型。

## Logger

使用 `Logger()` 获得插件作用域的结构化日志。

```go
Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
    ctx.Platform.Logger().Info("building tool")
    return NewTool(), nil
}
```

主机自动将日志记录器作用域设置为该插件。

## ConfigStore

`ConfigStore()` 给插件访问其自身配置行的权限。

```go
state, err := ctx.Platform.ConfigStore().Get(ctx)
if err != nil {
    return nil, err
}
```

当插件需要在正常管理流程之外读取或更新其自身的所需配置时，请使用它。

## StateStore

`StateStore()` 给插件一个用于操作状态的作用域持久化存储。

这与配置不同。使用它来处理派生状态，如光标、检查点或审查水位线。

```go
err := ctx.Platform.StateStore().Set(ctx, pkgplugins.StateScope{
    Kind: pkgplugins.StateScopeSession,
    ID:   sessionID,
}, "review_watermark", map[string]any{
    "reviewed_at": timestamp,
})
```

插件不传递自己的插件 ID。存储已经有作用域。

## Scheduler

`Scheduler()` 给一个用于插件拥有的任务的作用域调度器。

```go
err := ctx.Platform.Scheduler().ReconcileJobs(ctx, []pkgplugins.SchedulerJobSpec{{
    Key:         "review",
    RuntimeName: RuntimeName,
    Name:        "Reflect Review",
    Schedule: pkgplugins.SchedulerSchedule{
        Every: "30m",
    },
    Enabled: true,
}})
```

同样，插件不提供其插件 ID。作用域由主机处理。

## Notifier

`Notifier()` 给对用户可见通知的访问权。

`notify` 工具是最清楚的例子：

```go
Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
    service := ctx.Platform.Notifier()
    if service == nil {
        return nil, nil
    }
    return &Tool{service: service}, nil
}
```

当你的插件需要发送完成更新、告警或调度器驱动的消息时使用它。

## Auth

`Auth()` 提供用户和关联身份的查找。

当插件需要理解当前用户或通过 Stella 的身份模型路由特定平台的行为时使用它。

## RuntimeLookup

`RuntimeLookup()` 按插件 ID 和运行时名称解析运行中的托管运行时。

这通常由状态处理程序或需要检查他们拥有的另一个运行时的插件使用。

```go
handle, ok := build.Platform.RuntimeLookup().Lookup(PluginID, RuntimeName)
if !ok {
    return map[string]any{"state": "stopped"}, nil
}
snap, err := handle.Snapshot(ctx)
if err != nil {
    return nil, err
}
return map[string]any{"state": snap.State, "metadata": snap.Metadata}, nil
```

## ChannelPlatform

`ChannelPlatform()` 专门用于托管通道运行时。

它给访问：

- 父上下文
- 通道处理程序
- 通知注册表

Telegram 插件使用它来构造其托管运行时：

```go
channelRuntime := platform.ChannelPlatform()
parent := channelRuntime.ParentContext()
handler := channelRuntime.Handler()
notifications := channelRuntime.Notifications()
```

这个服务只对通道运行时插件有意义。

## ReflectPlatform

`ReflectPlatform()` 专门用于 Reflect 运行时。

它给访问：

- 父上下文
- 内存提供程序
- reflect 存储
- 工作区
- 提供程序注册表构造

这是有意专门的。它存在是因为 Reflect 是一个具有狭窄但不寻常的依赖的复杂托管运行时。

## 上下文决定你得到什么

`Platform` 不是全部。每个能力上下文还带有能力特定的字段。

示例：

`ToolContext`：

- `Platform`
- `Paths`
  - `UserRoot`
  - `ToolsBinDir`
  - `StellaHome`
  - `AgentRoot`
  - `ProjectRoot`
- `Runtime`

`ProjectRoot` 是当前附加的项目（如果有的话）。项目感知工具应该从 `ProjectRoot` 解析相对路径，而不是依赖运行器 cwd 细节。

`Runtime` 是一个用于工具文件和进程操作的能力接口。插件工具应该使用它，而不是直接导入沙箱内部。

`RuntimeContext`：

- `Platform`
- `State`

`MemoryContext`：

- `Platform`
- `State`
- `DB`
- `StellaHome`
- `SummarizerFn`

该设计保持 `Platform` 专注，同时仍给每个能力它实际需要的额外输入。

## Platform 使用指南

在以下情况下使用 `Platform`：

- 服务属于主机
- 插件应该只访问该服务的自己的作用域部分
- 服务在构建或运行时需要

不要将 `Platform` 用作通用垃圾场。如果能力需要一个新的主机拥有的服务，故意添加并记录为什么。

## 良好模式

- 将所有主机访问保持在 `Build`、`Run` 或运行时构造函数内
- 使用 `StateStore()` 来处理派生的操作状态，而不是配置
- 使用 `ConfigStore()` 处理所需配置，而不是运行时快照
- 在状态路径中使用 `RuntimeLookup()` 而不是存储全局指针
- 保持专门服务专门化，如 `ChannelPlatform()` 和 `ReflectPlatform()`

## 不良模式

- 在包级变量中缓存全局主机服务包
- 把插件 ID 传回已经作用域的存储
- 混合配置持久化和操作状态持久化
- 通过绕过作用域 API 读取另一个插件的状态

当插件代码保持在主机给它的边界内时，该设计效果最好。
