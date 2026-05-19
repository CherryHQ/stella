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

const REVEAL_HIDDEN = ["opacity-0", "translate-y-5"];
const REVEAL_VISIBLE = ["opacity-100", "translate-y-0"];
const REVEAL_BASE =
  "transition-[opacity,transform] duration-[600ms] [transition-timing-function:cubic-bezier(0.16,1,0.3,1)]";

function useReveal() {
  const ref = useRef<HTMLElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const prefersReduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    const targets = el.querySelectorAll("[data-reveal]");
    if (prefersReduced) {
      for (const target of targets) {
        target.classList.remove(...REVEAL_HIDDEN);
        target.classList.add(...REVEAL_VISIBLE);
      }
      return;
    }
    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (entry.isIntersecting) {
            entry.target.classList.remove(...REVEAL_HIDDEN);
            entry.target.classList.add(...REVEAL_VISIBLE);
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
    <div className="relative isolate flex min-h-svh flex-col bg-background font-sans text-foreground">
      <a
        href="#main-content"
        className="absolute -top-full left-4 z-[200] rounded-lg bg-foreground px-4 py-2 text-sm font-semibold text-background no-underline focus:top-4"
      >
        {lang === "zh" ? "跳到主要内容" : "Skip to main content"}
      </a>
      <div
        className="fixed top-0 right-0 left-0 z-[100] h-0.5 origin-left bg-primary will-change-transform"
        style={{ transform: `scaleX(${progress})` }}
      />
      <SiteHeader />
      <main ref={mainRef} id="main-content" className="flex-1">
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
    <section className="relative overflow-hidden py-[clamp(6rem,10vw,11rem)] pb-[clamp(4rem,7vw,8rem)]">
      {/* Orb */}
      <div
        className="pointer-events-none absolute -top-[20%] -right-[10%] h-[120%] w-[70%] animate-orb-drift rounded-full blur-[80px]"
        style={{
          background:
            "radial-gradient(ellipse at center, rgba(0,102,204,0.06) 0%, rgba(245,245,247,0.03) 40%, transparent 70%)",
        }}
      />
      {/* Grain */}
      <div
        className="pointer-events-none absolute inset-0 opacity-[0.02]"
        style={{
          backgroundImage:
            "url(\"data:image/svg+xml,%3Csvg viewBox='0 0 256 256' xmlns='http://www.w3.org/2000/svg'%3E%3Cfilter id='n'%3E%3CfeTurbulence type='fractalNoise' baseFrequency='0.85' numOctaves='4' stitchTiles='stitch'/%3E%3C/filter%3E%3Crect width='100%25' height='100%25' filter='url(%23n)'/%3E%3C/svg%3E\")",
          backgroundRepeat: "repeat",
          backgroundSize: "200px 200px",
        }}
      />

      <div className="mx-auto w-[min(100%-3rem,72rem)]">
        <div className="grid grid-cols-1 items-center gap-[clamp(2.5rem,5vw,5rem)] lg:grid-cols-[1fr_1.3fr]">
          {/* Copy */}
          <div className="relative animate-fade-up motion-reduce:animate-none">
            <span
              className="inline-block animate-shimmer rounded-full bg-primary/[0.12] px-3.5 py-1.5 font-mono text-[0.7rem] font-medium uppercase tracking-wider text-primary motion-reduce:animate-none"
              style={{
                backgroundSize: "200% 100%",
                backgroundImage:
                  "linear-gradient(90deg, rgba(0,102,204,0.12) 40%, rgba(0,102,204,0.24) 50%, rgba(0,102,204,0.12) 60%)",
              }}
            >
              {c.heroEyebrow}
            </span>
            <h1 className="mt-7 mb-6 text-[clamp(2.6rem,6vw,4.5rem)] font-semibold leading-[1.07] tracking-[-0.02em] text-foreground">
              {c.heroTitle[0]}{" "}
              <em className="relative not-italic text-primary after:absolute after:inset-x-[-0.05em] after:bottom-[0.02em] after:h-[0.12em] after:rounded-[0.06em] after:bg-primary/20 after:content-['']">
                {c.heroTitle[1]}
              </em>{" "}
              {c.heroTitle[2]}
            </h1>
            <p className="max-w-[36rem] text-lg font-normal leading-[1.47] tracking-[-0.022em] text-muted-foreground">
              {c.heroSub}
            </p>
            <div className="mt-9 flex flex-wrap gap-3 max-sm:flex-col">
              <Link
                to="/docs/$"
                params={{ _splat: "" }}
                className="inline-flex items-center gap-2 rounded-full bg-primary px-6 py-3 text-sm font-normal text-primary-foreground shadow-sm transition-all hover:-translate-y-px hover:shadow-lg active:scale-[0.97] max-sm:min-h-11 max-sm:justify-center"
              >
                {tr.readTheDocs}
                <ArrowRight aria-hidden className="size-4" />
              </Link>
              <a
                href="https://github.com/CherryHQ/stella"
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-2 rounded-full border border-foreground/[0.12] bg-transparent px-6 py-3 text-sm font-normal text-foreground transition-all hover:-translate-y-px hover:bg-foreground/[0.08] active:scale-[0.97] max-sm:min-h-11 max-sm:justify-center"
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
    <div
      className="relative overflow-hidden rounded-2xl border border-foreground/[0.12] bg-card shadow-xl animate-fade-up [animation-delay:300ms] motion-reduce:animate-none"
      aria-label={c.productLabel}
    >
      {/* Chrome bar */}
      <div className="flex items-center gap-1.5 border-b border-foreground/[0.12] bg-foreground/[0.02] px-4 py-2.5">
        <span className="size-[9px] rounded-full bg-foreground/[0.14]" />
        <span className="size-[9px] rounded-full bg-foreground/[0.14]" />
        <span className="size-[9px] rounded-full bg-foreground/[0.14]" />
        <span className="ml-auto font-mono text-[0.68rem] font-medium tracking-wide text-muted-foreground">
          {c.productLabel}
        </span>
      </div>

      {/* Body */}
      <div className="grid min-h-[320px] grid-cols-[7.5rem_1fr] max-sm:grid-cols-1">
        {/* Sidebar */}
        <div className="border-r border-foreground/[0.12] bg-foreground/[0.015] py-3 max-sm:hidden">
          <div className="flex items-center gap-2 border-r-2 border-primary bg-primary/[0.08] px-3 py-2 text-[0.72rem] font-medium text-primary">
            <MessageCircle className="size-3.5" />
            <span>{isZh ? "会话" : "Sessions"}</span>
          </div>
          {[
            { icon: Bot, label: isZh ? "智能体" : "Agents" },
            { icon: BookOpen, label: isZh ? "阅读" : "Recally" },
            { icon: CalendarClock, label: isZh ? "调度" : "Scheduler" },
            { icon: ListTodo, label: isZh ? "任务" : "Tasks" },
          ].map(({ icon: Icon, label }) => (
            <div
              key={label}
              className="flex cursor-default items-center gap-2 px-3 py-2 text-[0.72rem] font-medium text-muted-foreground"
            >
              <Icon className="size-3.5" />
              <span>{label}</span>
            </div>
          ))}
        </div>

        {/* Chat */}
        <div className="flex flex-col gap-3 overflow-hidden p-4">
          <div className="max-w-[92%] self-end">
            <p className="m-0 rounded-xl rounded-br-sm bg-foreground/[0.06] px-3.5 py-2 text-[0.78rem] leading-normal text-foreground">
              {isZh
                ? "帮我审查这个 PR 的安全性，重点关注 auth 中间件的改动"
                : "Review this PR for security issues, focus on the auth middleware changes"}
            </p>
          </div>

          <div className="flex max-w-[92%] items-start gap-2">
            <div className="flex size-6 shrink-0 items-center justify-center rounded-full bg-primary/[0.14] text-[0.65rem] font-semibold text-primary">
              S
            </div>
            <div className="flex flex-col gap-1.5">
              <div className="inline-flex w-fit items-center gap-1.5 rounded-md bg-foreground/[0.04] px-2 py-1 font-mono text-[0.65rem] text-muted-foreground">
                <Zap className="size-3" />
                <span>read_file</span>
                <span className="font-semibold text-emerald-500">✓</span>
              </div>
              <div className="inline-flex w-fit items-center gap-1.5 rounded-md bg-foreground/[0.04] px-2 py-1 font-mono text-[0.65rem] text-muted-foreground">
                <Zap className="size-3" />
                <span>github_pr_diff</span>
                <span className="font-semibold text-emerald-500">✓</span>
              </div>
              <p className="m-0 text-[0.78rem] leading-relaxed text-foreground">
                {isZh
                  ? "发现两个问题：1) session token 未设置 httpOnly 标志 2) CORS 配置允许通配符来源..."
                  : "Found 2 issues: 1) session token missing httpOnly flag 2) CORS config allows wildcard origins..."}
              </p>
            </div>
          </div>

          <div className="max-w-[92%] self-end">
            <p className="m-0 rounded-xl rounded-br-sm bg-foreground/[0.06] px-3.5 py-2 text-[0.78rem] leading-normal text-foreground">
              {isZh ? "修复它们并提交" : "Fix them and commit"}
            </p>
          </div>

          <div className="flex max-w-[92%] items-start gap-2">
            <div className="flex size-6 shrink-0 items-center justify-center rounded-full bg-primary/[0.14] text-[0.65rem] font-semibold text-primary">
              S
            </div>
            <div className="flex flex-col gap-1.5">
              <div className="inline-flex w-fit items-center gap-1.5 rounded-md border border-primary/20 bg-primary/[0.05] px-2 py-1 font-mono text-[0.65rem] text-muted-foreground">
                <Zap className="size-3" />
                <span>edit_file</span>
                <span className="ml-1 inline-flex gap-0.5">
                  {[0, 150, 300].map((delay) => (
                    <span
                      key={delay}
                      className="size-1 animate-dot-bounce rounded-full bg-primary motion-reduce:animate-none"
                      style={{ animationDelay: `${delay}ms` }}
                    />
                  ))}
                </span>
              </div>
            </div>
          </div>

          <div className="mt-auto rounded-xl border border-foreground/[0.12] bg-background px-3.5 py-2.5">
            <span className="text-xs text-foreground/30">
              {isZh ? "发送消息..." : "Send a message..."}
            </span>
          </div>
        </div>
      </div>

      {/* Feature badges */}
      <div className="pointer-events-none absolute inset-0">
        {[
          {
            label: isZh ? "记住你的偏好" : "Remembers your style",
            icon: Brain,
            pos: "top-[12%] right-[-3rem]",
            delay: "1.2s",
          },
          {
            label: isZh ? "沙箱执行" : "Sandboxed execution",
            icon: Shield,
            pos: "right-[-2.5rem] bottom-[30%]",
            delay: "1.6s",
          },
          {
            label: isZh ? "自托管" : "Self-hosted",
            icon: Lock,
            pos: "bottom-[8%] left-[-2rem]",
            delay: "2s",
          },
        ].map(({ label, icon: Icon, pos, delay }) => (
          <span
            key={label}
            className={`absolute inline-flex animate-badge-in items-center gap-1.5 whitespace-nowrap rounded-lg border border-foreground/[0.12] bg-card px-2.5 py-1 text-[0.65rem] font-medium text-muted-foreground opacity-0 shadow-md motion-reduce:animate-none motion-reduce:opacity-100 max-lg:hidden ${pos}`}
            style={{ animationDelay: delay }}
          >
            <Icon className="size-3" />
            {label}
          </span>
        ))}
      </div>
    </div>
  );
}

function SystemsSection({ lang }: { lang: keyof typeof copy }) {
  const c = copy[lang];
  const [hero, ...rest] = c.systems;
  const HeroIcon = ICON_MAP[hero.icon as keyof typeof ICON_MAP];

  return (
    <section className="border-t border-foreground/[0.12] bg-secondary py-[clamp(4rem,8vw,8rem)]">
      <div className="mx-auto w-[min(100%-3rem,72rem)]">
        <div data-reveal className={`mb-14 opacity-0 translate-y-5 ${REVEAL_BASE}`}>
          <h2 className="text-[clamp(2rem,4vw,3.2rem)] font-semibold leading-[1.07] tracking-[-0.02em] text-foreground">
            {c.systemsTitle}
          </h2>
          <p className="mt-4 max-w-[38rem] text-lg font-normal leading-[1.47] tracking-[-0.022em] text-muted-foreground">
            {c.systemsSub}
          </p>
        </div>

        {/* Hero system card */}
        <div
          data-reveal
          className={`mb-6 grid items-center gap-10 rounded-2xl border border-foreground/[0.12] bg-card p-8 opacity-0 translate-y-5 transition-all hover:-translate-y-0.5 hover:border-primary/30 hover:shadow-lg lg:grid-cols-[1fr_auto] ${REVEAL_BASE}`}
        >
          <div>
            <div className="mb-4 flex size-10 items-center justify-center rounded-xl bg-primary/10 text-primary">
              <HeroIcon className="size-5" aria-hidden />
            </div>
            <h3 className="mb-2.5 text-lg font-semibold tracking-[-0.022em] text-foreground">
              {hero.title}
            </h3>
            <p className="text-sm font-normal leading-[1.47] tracking-[-0.022em] text-muted-foreground">
              {hero.desc}
            </p>
          </div>
          <ul className="flex list-none flex-col gap-2.5 p-0 lg:m-0">
            {hero.highlights.map((h) => (
              <li
                key={h}
                className="rounded-md bg-primary/[0.08] px-3 py-1 text-xs font-medium text-primary"
              >
                {h}
              </li>
            ))}
          </ul>
        </div>

        {/* Grid */}
        <div className="grid gap-6 sm:grid-cols-2 lg:grid-cols-3">
          {rest.map((system, i) => {
            const Icon = ICON_MAP[system.icon as keyof typeof ICON_MAP];
            return (
              <div
                key={system.title}
                data-reveal
                className={`rounded-2xl border border-foreground/[0.12] bg-card p-8 opacity-0 translate-y-5 transition-all hover:-translate-y-0.5 hover:border-primary/30 hover:shadow-lg ${REVEAL_BASE}`}
                style={{ transitionDelay: `${(i + 1) * 100}ms` }}
              >
                <div className="mb-4 flex size-10 items-center justify-center rounded-xl bg-primary/10 text-primary">
                  <Icon className="size-5" aria-hidden />
                </div>
                <h3 className="mb-2.5 text-lg font-semibold tracking-[-0.022em] text-foreground">
                  {system.title}
                </h3>
                <p className="text-sm font-normal leading-[1.47] tracking-[-0.022em] text-muted-foreground">
                  {system.desc}
                </p>
                <ul className="mt-5 flex list-none flex-wrap gap-2 p-0">
                  {system.highlights.map((h) => (
                    <li
                      key={h}
                      className="rounded-md bg-primary/[0.08] px-3 py-1 text-xs font-medium text-primary"
                    >
                      {h}
                    </li>
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
    <section className="border-t border-foreground/[0.12] bg-foreground py-[clamp(5rem,9vw,9rem)] text-background">
      <div className="mx-auto w-[min(100%-3rem,72rem)]">
        <h2 className="mb-12 font-mono text-xs font-semibold uppercase tracking-widest text-primary">
          {lang === "zh" ? "设计原则" : "How Stella works"}
        </h2>
        <ol className="m-0 flex list-none flex-col p-0">
          {c.pillars.map((pillar, i) => (
            <li
              key={pillar.title}
              data-reveal
              className={`group grid grid-cols-[3.5rem_1fr] items-baseline gap-4 border-t border-background/10 py-8 opacity-0 translate-y-5 last:pb-0 max-sm:grid-cols-[2.5rem_1fr] ${REVEAL_BASE}`}
              style={{ transitionDelay: `${i * 80}ms` }}
            >
              <span className="font-mono text-xs font-medium tracking-wide text-primary">
                {String(i + 1).padStart(2, "0")}
              </span>
              <div>
                <h3 className="mb-2.5 text-[clamp(1.3rem,2.5vw,1.65rem)] font-semibold leading-[1.15] tracking-[-0.02em] text-background/[0.94] transition-colors group-hover:text-primary">
                  {pillar.title}
                </h3>
                <p className="max-w-3xl text-[0.92rem] font-normal leading-[1.47] tracking-[-0.022em] text-background/[0.56]">
                  {pillar.body}
                </p>
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
    <section className="border-t border-foreground/[0.12] py-[clamp(4rem,6vw,6rem)]">
      <div className="mx-auto w-[min(100%-3rem,72rem)]">
        <div data-reveal className={`mb-14 opacity-0 translate-y-5 ${REVEAL_BASE}`}>
          <h2 className="text-[clamp(2rem,4vw,3.2rem)] font-semibold leading-[1.07] tracking-[-0.02em] text-foreground">
            {lang === "zh" ? "一切内置" : "Everything built in"}
          </h2>
          <p className="mt-4 max-w-[38rem] text-lg font-normal leading-[1.47] tracking-[-0.022em] text-muted-foreground">
            {lang === "zh"
              ? "无需插件迷宫。你需要的能力随 Stella 一起发布。"
              : "No plugin maze. The capabilities you need ship with Stella."}
          </p>
        </div>
        <div className="grid gap-10 sm:grid-cols-2 lg:grid-cols-3">
          {c.capGroups.map((group) => (
            <div key={group.label}>
              <h3 className="mb-5 border-b-2 border-primary pb-3 font-mono text-xs font-semibold uppercase tracking-wider text-primary">
                {group.label}
              </h3>
              <div className="flex flex-col gap-6">
                {group.items.map((item) => {
                  const Icon = ICON_MAP[item.icon as keyof typeof ICON_MAP];
                  return (
                    <div
                      key={item.title}
                      data-reveal
                      className={`opacity-0 translate-y-5 ${REVEAL_BASE}`}
                    >
                      <h4 className="mb-1 flex items-center gap-1.5 text-sm font-semibold tracking-[-0.022em] text-foreground">
                        <Icon aria-hidden className="size-3.5 shrink-0 text-primary" />
                        {item.title}
                      </h4>
                      <p className="text-[0.82rem] font-normal leading-[1.47] tracking-[-0.022em] text-muted-foreground">
                        {item.desc}
                      </p>
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
    <section className="border-t border-foreground/[0.12] bg-secondary py-[clamp(5rem,8vw,8rem)]">
      <div className="mx-auto grid w-[min(100%-3rem,72rem)] items-center gap-16 max-lg:grid-cols-1 lg:grid-cols-2">
        <div data-reveal className={`max-w-md opacity-0 translate-y-5 ${REVEAL_BASE}`}>
          <h2 className="text-[clamp(2rem,4vw,3.2rem)] font-semibold leading-[1.07] tracking-[-0.02em] text-foreground">
            {c.ctaTitle}
          </h2>
          <p className="mt-4 text-lg font-normal leading-[1.47] tracking-[-0.022em] text-muted-foreground">
            {c.ctaSub}
          </p>
        </div>
        <div
          data-reveal
          className={`grid gap-2 rounded-2xl bg-foreground p-5 text-background opacity-0 translate-y-5 shadow-xl ${REVEAL_BASE}`}
          style={{ transitionDelay: "150ms" }}
        >
          <code className="block overflow-x-auto whitespace-nowrap rounded-xl bg-background/[0.08] px-3.5 py-3 font-mono text-sm leading-normal">
            <span className="mr-2 text-primary">$</span> brew install CherryHQ/tap/stella
          </code>
          <code className="block overflow-x-auto whitespace-nowrap rounded-xl bg-background/[0.08] px-3.5 py-3 font-mono text-sm leading-normal">
            <span className="mr-2 text-primary">$</span> stella server
          </code>
          <p className="mt-1 px-3.5 text-xs text-background/[0.52]">{c.ctaAlt}</p>
        </div>
      </div>
    </section>
  );
}
