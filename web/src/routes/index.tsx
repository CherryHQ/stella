import { createFileRoute, Link } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import {
  ArrowRight,
  Sparkles,
  Shield,
  MessageCircle,
  Clock,
  Brain,
  Users,
  Bot,
  Zap,
  BookOpen,
  Lock,
  Plug,
  CalendarClock,
  ListTodo,
  Rss,
} from "lucide-react";
import { siGithub } from "simple-icons";
import { t } from "@/lib/docs/translations";
import { SiteHeader } from "@/components/SiteHeader";
import { useI18n } from "@/lib/i18n";
import "./index.css";

function useReveal() {
  const ref = useRef<HTMLElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const targets = el.querySelectorAll(
      ".home-pillar, .home-cap, .home-caps-header, .home-cta-copy, .home-terminal, .home-product, .home-product-features, .home-system, .home-system--hero",
    );
    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (entry.isIntersecting) {
            entry.target.classList.add("is-visible");
            observer.unobserve(entry.target);
          }
        }
      },
      { threshold: 0.15 },
    );
    for (const target of targets) observer.observe(target);
    return () => observer.disconnect();
  }, []);
  return ref;
}

function useScrollProgress() {
  const [progress, setProgress] = useState(0);
  useEffect(() => {
    const onScroll = () => {
      const scrollTop = document.documentElement.scrollTop;
      const scrollHeight =
        document.documentElement.scrollHeight - document.documentElement.clientHeight;
      setProgress(scrollHeight > 0 ? scrollTop / scrollHeight : 0);
    };
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
  }, []);
  return progress;
}

export const Route = createFileRoute("/")({ component: Home });

const copy = {
  en: {
    heroEyebrow: "AI partner infrastructure",
    heroTitle: ["Your people deserve", "their own", "AI."],
    heroSub:
      "Stella is a self-hosted AI partner system — memory belongs to the person, work belongs to the agent, execution stays inside boundaries you choose.",
    productLabel: "stella — sessions",
    systemsTitle: "What Stella does for you",
    systemsSub:
      "Not just a chatbot. A system that reads, remembers, schedules, and follows through.",
    systems: [
      {
        icon: "zap",
        title: "Skills that grow with you",
        desc: "Teach Stella new workflows by installing skill bundles — code review, content writing, incident response, or anything you build. Each skill brings its own prompts, tools, and working style so the agent gets better at what your team actually does.",
        highlights: ["Browse a shared registry", "Install in one step", "Write your own"],
      },
      {
        icon: "recally",
        title: "A reading habit that keeps up",
        desc: "Drop a link and Stella saves, summarizes, and indexes it. Subscribe to RSS feeds and get daily digests that surface what matters. Your personal library that reads everything so you can focus on what's worth your time.",
        highlights: ["Save any article", "Subscribe to RSS feeds", "Daily AI-generated digests"],
      },
      {
        icon: "scheduler",
        title: "Schedules that actually run",
        desc: "Set up morning briefings, recurring health checks, or one-time reminders. Stella runs the instruction on schedule, uses the right tools, and notifies you with results — no cron tab to babysit.",
        highlights: [
          "Cron, interval, or one-shot",
          "Agent runs the task autonomously",
          "Results sent to your channel",
        ],
      },
      {
        icon: "task",
        title: "Tasks with a real lifecycle",
        desc: "Create tasks that the agent tracks from start to finish. Tasks flow through pending, running, review, and done — with priorities, dependencies, and human-in-the-loop checkpoints when the agent needs your call.",
        highlights: [
          "Priority and dependency tracking",
          "Human approval gates",
          "Status across restarts",
        ],
      },
    ],
    pillars: [
      {
        title: "Memory has ownership",
        body: "Each user-agent relationship gets dedicated memory. Your coding partner learns how you work; the same agent understands a teammate on their own terms.",
      },
      {
        title: "Work has boundaries",
        body: "Agents run in sandboxed workspaces with controlled tool access and network policy. Useful work, clear limits.",
      },
      {
        title: "Channels are just doors",
        body: "Telegram, QQ, Feishu, WeChat, Web UI, and terminal all connect to the same partner system.",
      },
      {
        title: "Routines keep moving",
        body: "Schedules, reminders, reading digests, and background tasks persist across restarts and notify the right people.",
      },
      {
        title: "Agents have roles",
        body: "Create agents for coding, writing, operations, family routines. Each gets its own model, tools, skills, and working style.",
      },
    ],
    capGroups: [
      {
        label: "Identity & Access",
        items: [
          {
            icon: "users",
            title: "Multi-tenant identity",
            desc: "One Stella instance serves many people with isolated secrets, memories, and agent relationships.",
          },
          {
            icon: "lock",
            title: "Secrets vault",
            desc: "API keys and tokens stored encrypted. Available at runtime, never exposed in conversations.",
          },
          {
            icon: "shield",
            title: "Sandboxed execution",
            desc: "Per-agent workspaces with explicit network policy and tool boundaries.",
          },
        ],
      },
      {
        label: "Agents & Skills",
        items: [
          {
            icon: "bot",
            title: "Specialized agents",
            desc: "Pre-built templates for coding, research, writing, and review. Focused agents in seconds.",
          },
          {
            icon: "zap",
            title: "Skills system",
            desc: "Installable playbooks that teach Stella new workflows. Browse registries or write your own.",
          },
          {
            icon: "plug",
            title: "Plugins & MCP",
            desc: "Extend with custom code or connect any Model Context Protocol server. Zero custom wiring.",
          },
        ],
      },
      {
        label: "Productivity",
        items: [
          {
            icon: "book",
            title: "Reading assistant",
            desc: "Save articles, subscribe to RSS, get daily digests. Your personal library that summarizes everything.",
          },
          {
            icon: "sparkles",
            title: "Multi-provider models",
            desc: "Anthropic, OpenAI, or any compatible API. Switch models per agent, per task.",
          },
          {
            icon: "clock",
            title: "Proactive notifications",
            desc: "Stella reaches out when something needs attention. Task done, job failed, or something you should know.",
          },
        ],
      },
    ],
    ctaTitle: "Run it yourself",
    ctaSub: "Start locally, then add people, agents, tools, and channels as you grow.",
    ctaAlt: "Also available via go install and direct binary download.",
  },
  zh: {
    heroEyebrow: "AI 伙伴基础设施",
    heroTitle: ["你的团队值得拥有", "专属", "AI。"],
    heroSub:
      "Stella 是一套自托管的 AI 伙伴系统——记忆属于正确的人，工作属于正确的 agent，执行发生在你选择的边界内。",
    productLabel: "stella — 会话",
    systemsTitle: "Stella 为你做什么",
    systemsSub: "不只是聊天机器人。一个能阅读、记忆、调度、持续执行的系统。",
    systems: [
      {
        icon: "zap",
        title: "与你一起成长的技能",
        desc: "通过安装技能包来教 Stella 新的工作流——代码审查、内容写作、事故响应，或任何你自己构建的流程。每个技能带有自己的 prompt、工具和工作方式，让 agent 越来越擅长你团队真正在做的事。",
        highlights: ["浏览共享注册表", "一步安装", "自定义编写"],
      },
      {
        icon: "recally",
        title: "跟得上你的阅读习惯",
        desc: "丢一个链接，Stella 就会保存、总结并索引它。订阅 RSS 源，获取每日摘要，呈现真正重要的内容。你的个人图书馆会读完一切，让你专注于值得花时间的内容。",
        highlights: ["保存任何文章", "订阅 RSS 源", "AI 生成的每日摘要"],
      },
      {
        icon: "scheduler",
        title: "真正会执行的日程",
        desc: "设置晨报简报、定期健康检查或一次性提醒。Stella 按时执行指令、使用合适的工具，并将结果通知你——无需操心 cron 任务。",
        highlights: ["Cron、间隔或一次性", "agent 自主执行任务", "结果推送到你的渠道"],
      },
      {
        icon: "task",
        title: "有完整生命周期的任务",
        desc: "创建 agent 从头到尾跟踪的任务。任务流经 pending、running、review、done——支持优先级、依赖关系，以及在 agent 需要你决策时的人工审批节点。",
        highlights: ["优先级和依赖跟踪", "人工审批门控", "跨重启保持状态"],
      },
    ],
    pillars: [
      {
        title: "记忆有归属",
        body: "每个用户与 agent 的关系都有专属记忆。你的编程伙伴可以学习你的方式，同一个 agent 也能以另一套上下文理解队友。",
      },
      {
        title: "工作有边界",
        body: "agent 在沙箱工作区中运行，拥有受控的工具访问和网络策略。有用的工作，清晰的限制。",
      },
      {
        title: "渠道只是入口",
        body: "Telegram、QQ、飞书、微信、Web UI 和终端都连接到同一套伙伴系统。",
      },
      {
        title: "日程持续运行",
        body: "定时任务、提醒、阅读摘要和后台任务跨重启保留，并通知正确的人。",
      },
      {
        title: "agent 各司其职",
        body: "为编程、写作、运营、家庭事务创建不同 agent。每个都有自己的模型、工具、技能和工作方式。",
      },
    ],
    capGroups: [
      {
        label: "身份与访问",
        items: [
          {
            icon: "users",
            title: "多用户身份",
            desc: "一套 Stella 服务多人，每人拥有独立的密钥、记忆和 agent 关系。",
          },
          {
            icon: "lock",
            title: "密钥保险库",
            desc: "API 密钥和令牌加密存储。运行时可用，对话中不会暴露。",
          },
          {
            icon: "shield",
            title: "沙箱执行",
            desc: "按 agent 配置工作区，拥有明确的网络策略和工具边界。",
          },
        ],
      },
      {
        label: "智能体与技能",
        items: [
          {
            icon: "bot",
            title: "专业智能体",
            desc: "预建编程、研究、写作和审查模板。几秒钟内创建专注的智能体。",
          },
          {
            icon: "zap",
            title: "技能系统",
            desc: "可安装的工作流剧本，教 Stella 学习新流程。浏览注册表或自己编写。",
          },
          {
            icon: "plug",
            title: "插件与 MCP",
            desc: "用自定义代码扩展或连接任何 Model Context Protocol 服务器。",
          },
        ],
      },
      {
        label: "生产力",
        items: [
          {
            icon: "book",
            title: "阅读助手",
            desc: "保存文章、订阅 RSS、获取每日摘要。你的个人图书馆，自动总结一切。",
          },
          {
            icon: "sparkles",
            title: "多供应商模型",
            desc: "Anthropic、OpenAI 或任何兼容 API。按智能体、按任务切换模型。",
          },
          {
            icon: "clock",
            title: "主动通知",
            desc: "需要你关注时 Stella 会主动联系你。任务完成、作业失败或你应该知道的事。",
          },
        ],
      },
    ],
    ctaTitle: "自己运行",
    ctaSub: "从本地开始，然后按需添加用户、agent、工具和渠道。",
    ctaAlt: "也支持 go install 和直接下载二进制文件。",
  },
};

const ICON_MAP = {
  brain: Brain,
  shield: Shield,
  message: MessageCircle,
  clock: Clock,
  sparkles: Sparkles,
  users: Users,
  bot: Bot,
  zap: Zap,
  book: BookOpen,
  lock: Lock,
  plug: Plug,
  recally: Rss,
  scheduler: CalendarClock,
  task: ListTodo,
};

function Home() {
  const { locale } = useI18n();
  const lang = locale === "zh" ? "zh" : "en";
  const mainRef = useReveal();
  const progress = useScrollProgress();

  return (
    <div className="relative isolate flex min-h-svh flex-col bg-background text-foreground">
      <a href="#main-content" className="home-skip-link">
        {lang === "zh" ? "跳到主要内容" : "Skip to main content"}
      </a>
      <div className="home-progress" style={{ transform: `scaleX(${progress})` }} />
      <SiteHeader />
      <main ref={mainRef} id="main-content" className="home-page flex-1">
        <HeroSection lang={lang} />
        <SystemsSection lang={lang} />
        <PillarsSection lang={lang} />
        <CapabilitiesSection lang={lang} />
        <FooterCTA lang={lang} />
      </main>
    </div>
  );
}

function HeroSection({ lang }: { lang: keyof typeof copy }) {
  const tr = t(lang);
  const c = copy[lang];

  return (
    <section className="home-hero">
      <div className="home-hero-orb" />
      <div className="home-hero-grain" />
      <div className="home-shell">
        <div className="home-hero-layout">
          <div className="home-hero-copy">
            <span className="home-eyebrow">{c.heroEyebrow}</span>
            <h1 className="home-hero-title">
              {c.heroTitle[0]} <em>{c.heroTitle[1]}</em> {c.heroTitle[2]}
            </h1>
            <p className="home-hero-body">{c.heroSub}</p>
            <div className="home-actions">
              <Link to="/docs/$" params={{ _splat: "" }} className="home-btn home-btn-primary">
                {tr.readTheDocs}
                <ArrowRight aria-hidden className="size-4" />
              </Link>
              <a
                href="https://github.com/CherryHQ/stella"
                target="_blank"
                rel="noopener noreferrer"
                className="home-btn home-btn-ghost"
              >
                <svg aria-hidden viewBox="0 0 24 24" className="size-4 fill-current">
                  <path d={siGithub.path} />
                </svg>
                {tr.sourceOnGithub}
              </a>
            </div>
          </div>

          <ProductPreview lang={lang} />
        </div>
      </div>
    </section>
  );
}

function ProductPreview({ lang }: { lang: keyof typeof copy }) {
  const c = copy[lang];
  const isZh = lang === "zh";

  return (
    <div className="home-product" aria-label={c.productLabel}>
      <div className="home-product-chrome">
        <span className="home-product-dot" />
        <span className="home-product-dot" />
        <span className="home-product-dot" />
        <span className="home-product-title">{c.productLabel}</span>
      </div>
      <div className="home-product-body">
        <div className="home-product-sidebar">
          <div className="home-product-nav-item home-product-nav-active">
            <MessageCircle className="size-3.5" />
            <span>{isZh ? "会话" : "Sessions"}</span>
          </div>
          <div className="home-product-nav-item">
            <Bot className="size-3.5" />
            <span>{isZh ? "智能体" : "Agents"}</span>
          </div>
          <div className="home-product-nav-item">
            <BookOpen className="size-3.5" />
            <span>{isZh ? "阅读" : "Recally"}</span>
          </div>
          <div className="home-product-nav-item">
            <CalendarClock className="size-3.5" />
            <span>{isZh ? "调度" : "Scheduler"}</span>
          </div>
          <div className="home-product-nav-item">
            <ListTodo className="size-3.5" />
            <span>{isZh ? "任务" : "Tasks"}</span>
          </div>
        </div>

        <div className="home-product-chat">
          <div className="home-product-msg home-product-msg-user">
            <p>
              {isZh
                ? "帮我审查这个 PR 的安全性，重点关注 auth 中间件的改动"
                : "Review this PR for security issues, focus on the auth middleware changes"}
            </p>
          </div>
          <div className="home-product-msg home-product-msg-assistant">
            <div className="home-product-avatar">S</div>
            <div className="home-product-msg-content">
              <div className="home-product-tool">
                <Zap className="size-3" />
                <span>read_file</span>
                <span className="home-product-tool-status">✓</span>
              </div>
              <div className="home-product-tool">
                <Zap className="size-3" />
                <span>github_pr_diff</span>
                <span className="home-product-tool-status">✓</span>
              </div>
              <p>
                {isZh
                  ? "发现两个问题：1) session token 未设置 httpOnly 标志 2) CORS 配置允许通配符来源..."
                  : "Found 2 issues: 1) session token missing httpOnly flag 2) CORS config allows wildcard origins..."}
              </p>
            </div>
          </div>
          <div className="home-product-msg home-product-msg-user">
            <p>{isZh ? "修复它们并提交" : "Fix them and commit"}</p>
          </div>
          <div className="home-product-msg home-product-msg-assistant">
            <div className="home-product-avatar">S</div>
            <div className="home-product-msg-content">
              <div className="home-product-tool home-product-tool-running">
                <Zap className="size-3" />
                <span>edit_file</span>
                <span className="home-product-dots">
                  <span />
                  <span />
                  <span />
                </span>
              </div>
            </div>
          </div>

          <div className="home-product-input">
            <span className="home-product-input-placeholder">
              {isZh ? "发送消息..." : "Send a message..."}
            </span>
          </div>
        </div>
      </div>

      <div className="home-product-features">
        <span className="home-product-badge home-product-badge-1">
          <Brain className="size-3" />
          {isZh ? "记住你的偏好" : "Remembers your style"}
        </span>
        <span className="home-product-badge home-product-badge-2">
          <Shield className="size-3" />
          {isZh ? "沙箱执行" : "Sandboxed execution"}
        </span>
        <span className="home-product-badge home-product-badge-3">
          <Lock className="size-3" />
          {isZh ? "自托管" : "Self-hosted"}
        </span>
      </div>
    </div>
  );
}

function SystemsSection({ lang }: { lang: keyof typeof copy }) {
  const c = copy[lang];
  const [hero, ...rest] = c.systems;
  const HeroIcon = ICON_MAP[hero.icon as keyof typeof ICON_MAP];

  return (
    <section className="home-systems">
      <div className="home-shell">
        <div className="home-caps-header">
          <h2 className="home-section-title">{c.systemsTitle}</h2>
          <p className="home-section-sub">{c.systemsSub}</p>
        </div>
        <div className="home-system home-system--hero">
          <div className="home-system-hero-copy">
            <div className="home-system-icon">
              <HeroIcon className="size-5" aria-hidden />
            </div>
            <h3 className="home-system-title">{hero.title}</h3>
            <p className="home-system-desc">{hero.desc}</p>
          </div>
          <ul className="home-system-highlights home-system-highlights--hero">
            {hero.highlights.map((h) => (
              <li key={h}>{h}</li>
            ))}
          </ul>
        </div>
        <div className="home-systems-grid">
          {rest.map((system) => {
            const Icon = ICON_MAP[system.icon as keyof typeof ICON_MAP];
            return (
              <div key={system.title} className="home-system">
                <div className="home-system-icon">
                  <Icon className="size-5" aria-hidden />
                </div>
                <h3 className="home-system-title">{system.title}</h3>
                <p className="home-system-desc">{system.desc}</p>
                <ul className="home-system-highlights">
                  {system.highlights.map((h) => (
                    <li key={h}>{h}</li>
                  ))}
                </ul>
              </div>
            );
          })}
        </div>
      </div>
    </section>
  );
}

function PillarsSection({ lang }: { lang: keyof typeof copy }) {
  const c = copy[lang];

  return (
    <section className="home-pillars">
      <div className="home-shell">
        <h2 className="home-pillars-heading">{lang === "zh" ? "设计原则" : "How Stella works"}</h2>
        <ol className="home-pillars-list">
          {c.pillars.map((pillar, i) => (
            <li key={pillar.title} className="home-pillar">
              <span className="home-pillar-num" aria-hidden>
                {String(i + 1).padStart(2, "0")}
              </span>
              <div>
                <h3 className="home-pillar-title">{pillar.title}</h3>
                <p className="home-pillar-body">{pillar.body}</p>
              </div>
            </li>
          ))}
        </ol>
      </div>
    </section>
  );
}

function CapabilitiesSection({ lang }: { lang: keyof typeof copy }) {
  const c = copy[lang];

  return (
    <section className="home-caps">
      <div className="home-shell">
        <div className="home-caps-header">
          <h2 className="home-section-title">
            {lang === "zh" ? "一切内置" : "Everything built in"}
          </h2>
          <p className="home-section-sub">
            {lang === "zh"
              ? "无需插件迷宫。你需要的能力随 Stella 一起发布。"
              : "No plugin maze. The capabilities you need ship with Stella."}
          </p>
        </div>
        <div className="home-caps-groups">
          {c.capGroups.map((group) => (
            <div key={group.label} className="home-cap-group">
              <h3 className="home-cap-group-label">{group.label}</h3>
              <div className="home-cap-group-items">
                {group.items.map((item) => {
                  const Icon = ICON_MAP[item.icon as keyof typeof ICON_MAP];
                  return (
                    <div key={item.title} className="home-cap">
                      <h4>
                        <Icon aria-hidden className="home-cap-inline-icon" />
                        {item.title}
                      </h4>
                      <p>{item.desc}</p>
                    </div>
                  );
                })}
              </div>
            </div>
          ))}
        </div>
      </div>
    </section>
  );
}

function FooterCTA({ lang }: { lang: keyof typeof copy }) {
  const c = copy[lang];

  return (
    <section className="home-cta">
      <div className="home-shell home-cta-layout">
        <div className="home-cta-copy">
          <h2 className="home-section-title">{c.ctaTitle}</h2>
          <p className="home-section-sub">{c.ctaSub}</p>
        </div>
        <div className="home-terminal">
          <code>
            <span className="home-terminal-prompt">$</span> brew install CherryHQ/tap/stella
          </code>
          <code>
            <span className="home-terminal-prompt">$</span> stella server
          </code>
          <p className="home-terminal-alt">{c.ctaAlt}</p>
        </div>
      </div>
    </section>
  );
}
