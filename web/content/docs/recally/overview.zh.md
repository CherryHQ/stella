---
title: Recally 概览
---

Recally 是 Stella 的阅读系统。它帮助你跟进网页、PDF、RSS feeds 和你关心的主题。

当阅读不是一次性任务，而是持续关注时，就应该使用 Recally：行业新闻、政策、研究论文、竞品动态、工程博客、产品发布，或任何你不想错过的内容。

## 保存网页和 PDF

把网页或 PDF 保存到 Recally，它就会成为你的阅读列表的一部分。Stella 可以提取内容、保留来源，并让它之后可读、可搜索。

## 获取 AI 总结

Recally 可以总结已保存内容，让你快速判断什么值得深入阅读。

好的总结应该回答：

- 这篇内容在讲什么？
- 它为什么重要？
- 有什么变化？
- 接下来应该读什么？

## 围绕文章聊天

保存文章后，你可以继续提问：

- 主要观点是什么？
- 作者有哪些假设？
- 把它和另一篇文章对比。
- 把它整理成给团队的 brief。

这会把阅读从被动收藏变成主动理解。

## 自动轮询和摘要需要手动订阅

> **从旧版升级？** 自动 RSS 轮询和每日摘要广播已移除。升级后，Stella 不再自动轮询 feeds 或生成摘要，需要你手动订阅：
>
> 1. 打开任意 Agent → **Tasks** 页签 → **New Schedule** → **From template**（从模板）。
> 2. 选择 **recally-rss** 恢复定期 feed 轮询，或选择 **recally-digest** 获取每日摘要。
>
> 你保存的 feeds 和文章数据不受影响，只是自动调度方式发生了变化。

## 订阅 feeds

Recally 可以订阅 feeds，并持续获取新条目。适合长期关注的来源：公司博客、release notes、期刊、newsletter 和政策更新。

除了 RSS，你还可以关注：

- **Twitter/X 账号** —— 用类似 `https://x.com/<handle>` 的主页地址订阅，Recally 会把新推文当作 feed 条目，像其他来源一样保存和总结。仅支持个人主页 timeline；列表、搜索、单条推文和书签会被拒绝。
- **YouTube 频道** —— 订阅频道的 RSS 地址（`https://www.youtube.com/feeds/videos.xml?channel_id=...`）即可跟进新视频。
- **没有 RSS 的网站** —— 对于列出条目的页面（博客索引、release notes、"What's new" 页），订阅时选择 website feed 类型。Recally 会从页面里抓取条目链接，并像其他来源一样逐条保存。

Recally 会从 URL 自动识别来源类型，所以无论内容在哪，订阅都是同样的一步。对于没有 RSS 的页面，选择 website 类型让 Recally 直接抓取页面，而不是去找 feed。

## 维护阅读列表

使用状态、标签、星标和搜索，让阅读列表保持可用。只存链接的阅读系统最后只会变成坟场。Recally 应该帮助你决定读什么、跳过什么、之后回看什么。

## 生成 digests

Digests 会汇总保存文章和 feeds 中真正重要的内容。它适合晨间简报、每周研究回顾、行业监控或团队更新。
