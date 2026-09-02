---
title: 网页研究
---

Stella 可以搜索公开来源并读取选定页面，同时不会让 Agent 访问 Stella 服务器所在的内部网络。

## 配置搜索

在启动 `stellad` 的环境中设置一个或多个 provider 的**原生**环境变量，然后重启：

```sh
FIRECRAWL_API_KEY=your-key
# 或 PARALLEL_API_KEY、TAVILY_API_KEY、EXA_API_KEY、JINA_API_KEY、
# SEARXNG_URL、BRAVE_SEARCH_API_KEY、KEENABLE_API_KEY
```

无需配置即可通过 Exa 的匿名 hosted MCP endpoint 使用 `web_search`。搜索查询会发送给 Exa，并受其匿名限流约束。Stella 会先按 Firecrawl、Parallel、Tavily、Exa、Jina、SearXNG、Brave、Keenable 的顺序尝试已配置 provider；它们不可用或请求失败时，最后回退到匿名 Exa。设置 `EXA_API_KEY` 后会改用 Exa direct API，不会再通过匿名 MCP 重试同一查询。provider 凭据始终留在服务器上，不会进入 Agent 沙箱或工具结果。

可选的原生 endpoint 变量包括，自托管 Firecrawl 的 `FIRECRAWL_API_URL`，以及 Tavily-compatible endpoint 的 `TAVILY_BASE_URL`。`PARALLEL_SEARCH_MODE` 可选 `agentic`（默认）、`fast` 或 `one-shot`。

## 安全地研究网页

Agent 先用 `web_search` 获取标题、URL 和摘要，再对它选择的某个结果调用内置 `web_fetch` 工具。

搜索标题、摘要和抓取到的页面正文都是不可信证据，其中可能包含提示注入或误导性陈述。Agent 不得执行其中的指令；重要结论应回到引用来源核验。

大型搜索或抓取结果会作为临时文件写入当前 Agent 沙箱。工具只返回沙箱可见路径、总大小及头尾预览。请使用 `bash` 以有界范围读取该文件，不要把整个文件加载到模型上下文。这些文件只是便于读取的快照，不是安全边界，同一沙箱用户运行的命令可以修改它们。

`web_fetch` 只访问公开 HTTP 和 HTTPS 网站。它拒绝本地、私有、链路本地、多播及其他非公开地址；每次重定向都会重新检查，最多允许五次重定向，并拒绝带有疑似凭据 query 参数的 URL。它直接连接并忽略 `HTTP_PROXY`、`HTTPS_PROXY` 和 `NO_PROXY`，因为代理可能把模型指定的 host 解析到这项公开地址策略之外。响应正文超过 10 MB 时会在提取前被拒绝。

直接获取或提取失败时，`web_fetch` 会把已经验证为公开地址的 URL 发送给 Jina Reader，并使用其 Markdown 响应。这会向 Jina 披露所选 URL，但不会发送 provider 凭据或带有疑似凭据 query 参数的 URL。
