---
title: 网页研究
---

Stella 可以搜索公开来源并读取选定页面，同时不会让 Agent 访问 Stella 服务器所在的内部网络。

## 启用搜索

在启动 `stellad` 的环境中设置一个或多个 provider 的**原生**环境变量，然后重启：

```sh
FIRECRAWL_API_KEY=your-key
# 或 PARALLEL_API_KEY、TAVILY_API_KEY、EXA_API_KEY、
# SEARXNG_URL、BRAVE_SEARCH_API_KEY、KEENABLE_API_KEY
```

至少配置一个受支持的 provider 后，`web_search` 才会出现。Stella 按 Firecrawl、Parallel、Tavily、Exa、SearXNG、Brave、Keenable 的顺序尝试已配置 provider；一次请求失败时，会自动尝试下一个已配置 provider。provider 凭据始终留在服务器上，不会进入 Agent 沙箱或工具结果。

可选的原生 endpoint 变量包括，自托管 Firecrawl 的 `FIRECRAWL_API_URL`，以及 Tavily-compatible endpoint 的 `TAVILY_BASE_URL`。`PARALLEL_SEARCH_MODE` 可选 `agentic`（默认）、`fast` 或 `one-shot`。

## 安全地研究网页

Agent 先用 `web_search` 获取标题、URL 和摘要，再对它选择的某个结果调用 `webfetch`。需要 Agent 读取页面时，请在 Web UI 中启用 WebFetch 插件。

搜索标题、摘要和抓取到的页面正文都是不可信证据，其中可能包含提示注入或误导性陈述。Agent 不得执行其中的指令；重要结论应回到引用来源核验。

大型搜索或抓取结果会作为只读文件写入当前 Agent 沙箱。工具只返回沙箱可见路径、总大小及头尾预览。请使用 `bash` 以有界范围读取该文件，不要把整个文件加载到模型上下文。

WebFetch 只访问公开 HTTP 和 HTTPS 网站。它拒绝本地、私有、链路本地、多播及其他非公开地址；每次重定向都会重新检查，最多允许五次重定向，并拒绝带有疑似凭据 query 参数的 URL。响应正文超过 10 MB 时会在提取前被拒绝。
