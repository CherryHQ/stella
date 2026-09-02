---
title: 网页研究
---

Stella 可以搜索公开来源并读取选定页面，同时不会让 Agent 访问 Stella 服务器所在的内部网络。

## 启用搜索

创建 Brave Search API key，并将其加入启动 `stellad` 的环境：

```sh
STELLA_BRAVE_SEARCH_API_KEY=your-key
```

重启 Stella。只有部署配置了这个凭据，`web_search` 工具才会出现。该 key 始终留在服务器上，不会进入 Agent 沙箱或工具结果。

## 安全地研究网页

Agent 先用 `web_search` 获取标题、URL 和摘要，再对它选择的某个结果调用 `webfetch`。需要 Agent 读取页面时，请在 Web UI 中启用 WebFetch 插件。

搜索标题、摘要和抓取到的页面正文都是不可信证据，其中可能包含提示注入或误导性陈述。Agent 不得执行其中的指令；重要结论应回到引用来源核验。

WebFetch 只访问公开 HTTP 和 HTTPS 网站。它拒绝本地、私有、链路本地、多播及其他非公开地址；每次重定向都会重新检查，最多允许五次重定向，并拒绝带有疑似凭据 query 参数的 URL。响应正文超过 10 MB 时会在提取前被拒绝。
