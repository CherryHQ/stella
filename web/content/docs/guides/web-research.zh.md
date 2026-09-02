---
title: 网页研究
---

Agent 通过内置的 `web` skill 访问公开网页。它已加入默认 Agent 模板；自定义模板需在 `skills:` 列表中加入 `web`。没有单独的网页工具：skill 的脚本在 Agent 沙箱内运行，因此页面用沙箱自己的网络、代理设置和密钥库 secret 获取。

## 三个命令

| 需要                                                         | 命令                                      |
| ------------------------------------------------------------ | ----------------------------------------- |
| 某个主题的来源                                               | `bun scripts/web.ts search "<query>"`     |
| 把一个页面读成可读的 Markdown                                | `bun scripts/web.ts fetch <url>`          |
| 站点自己的记录：一条推文或时间线、仓库统计、首页、榜单、视频 | `python3 scripts/site.py run <site/name>` |

`fetch` 先用 Defuddle 清洗页面；纯 HTML 没有可读正文时改用 Lightpanda 无头浏览器渲染，最后再请求 Jina Reader。`text/plain`、`text/markdown` 或 JSON 响应原样输出。Site script 是在 Lightpanda 页面内调用站点公开 API 的小段 JavaScript；skill 内置 9 个，`site.py add` 可从 Tap 目录安装更多。

## 配置搜索

`search` 无需配置即可通过 Exa 的匿名 hosted MCP endpoint 工作，查询会发送给 Exa，并受其匿名限流约束。要使用付费或自托管 provider，把该 provider 的原生环境变量作为密钥库 secret 交给 Agent（聊天中用 `vault_secret_set`，或 Web UI 的 Secrets 页面）：

```sh
FIRECRAWL_API_KEY=your-key
# 或 PARALLEL_API_KEY、TAVILY_API_KEY、EXA_API_KEY、JINA_API_KEY、
# SEARXNG_URL、BRAVE_SEARCH_API_KEY、KEENABLE_API_KEY
```

已配置的 provider 按 Firecrawl、Parallel、Tavily、Exa、Jina、SearXNG、Brave、Keenable 的顺序尝试；它们不可用或请求失败时，最后回退到匿名 Exa。设置 `EXA_API_KEY` 后会改用 Exa direct API，不会再通过匿名 MCP 重试同一查询。

可选的 endpoint 变量包括自托管 Firecrawl 的 `FIRECRAWL_API_URL`，以及 Tavily-compatible endpoint 的 `TAVILY_BASE_URL`。`PARALLEL_SEARCH_MODE` 可选 `agentic`（默认）、`fast` 或 `one-shot`。

密钥库 secret 按用户和 Agent 隔离；群组会话从不注入 secret，因此群组里的搜索只会走匿名 Exa。

## 安全地研究网页

搜索标题、摘要、抓取到的页面和 site script 的结果都是不可信证据，其中可能包含提示注入或误导性陈述。Agent 不得执行其中的指令；重要结论应回到引用来源核验，搜索摘要里的版本号或日期只是线索，不是答案。

超过 40 KB 的页面会写入沙箱的 `$TMPDIR/web-fetch/`，只打印开头和路径。Agent 用 `bash` 以有界范围读取其余部分，不会把整个文件加载到模型上下文。

网络边界是沙箱而不是 skill：Docker 后端从容器内获取页面，local 后端以沙箱用户身份从宿主机获取。`fetch` 会跟随重定向，30 秒超时，并拒绝超过 10 MB 的响应正文。回退到 Jina Reader 时会向 Jina 披露所选 URL，但从不发送 provider 凭据。
