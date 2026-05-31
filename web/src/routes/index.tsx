import { createFileRoute, Link } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import {
  ArrowRight,
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
    heroEyebrow: "Shared AI coworkers",
    heroTitle: ["Give every team", "professional", "agents."],
    heroSub:
      "Stella lets domain owners create shared agents with instructions, skills, tools, knowledge, and memory rules. Everyone else just chats, gives the agent a goal, and tracks the work.",
    productLabel: "stella — goal workspace",
    systemsTitle: "From shared expertise to finished work",
    systemsSub:
      "Create professional agents once. Let the organization use them like real coworkers.",
    systems: [
      {
        icon: "bot",
        title: "Create shared professional agents",
        desc: "Finance, HR, engineering, research, and operations teams can create agents that know their work. Each agent has its own role, instructions, knowledge, skills, tools, and channels.",
        highlights: [
          "Owned by domain experts",
          "Shared across the organization",
          "Personalized per user",
        ],
      },
      {
        icon: "task",
        title: "Turn goals into task DAGs",
        desc: "Give an agent a goal. Stella can plan the work, split it into tasks, connect dependencies, define acceptance criteria, track blockers, and route results through review.",
        highlights: ["Goal to plan", "Dependencies and blockers", "Acceptance criteria and review"],
      },
      {
        icon: "recally",
        title: "Read the web with Recally",
        desc: "Save web pages and PDFs, get AI summaries, chat with articles, subscribe to RSS feeds, and maintain a reading list that keeps up with what matters.",
        highlights: ["Web pages and PDFs", "RSS feeds and digests", "Chat with articles"],
      },
      {
        icon: "message",
        title: "Use agents in daily workflow",
        desc: "Talk to agents from the Web UI, terminal, Telegram, QQ, Feishu, or WeChat. Users do not need to learn the specialist software behind each workflow.",
        highlights: ["Chat-native interface", "Shared channels", "Tools stay behind the agent"],
      },
    ],
    pillars: [
      {
        title: "Agents are owned by the domain",
        body: "The people who know the workflow create the agent, write its instructions, attach knowledge, and decide review boundaries.",
      },
      {
        title: "Everyone uses the same expert",
        body: "A shared Finance agent or HR agent gives the organization one consistent professional coworker instead of scattered personal prompts.",
      },
      {
        title: "Every agent can know you",
        body: "Agents keep per-user memory and can also use shared user memory when you want your preferences to travel across agents.",
      },
      {
        title: "Goals become accountable work",
        body: "Plans, task DAGs, acceptance criteria, blockers, and review states make agent work inspectable instead of buried in chat.",
      },
      {
        title: "Reading becomes a system",
        body: "Recally turns web pages, PDFs, RSS feeds, summaries, and article chat into a living knowledge stream.",
      },
    ],
    capGroups: [
      {
        label: "Organization",
        items: [
          {
            icon: "users",
            title: "Multi-tenant, multi-user",
            desc: "Run Stella for an organization with scoped users, shared agents, and isolated data boundaries.",
          },
          {
            icon: "bot",
            title: "Shared agent catalog",
            desc: "Create agents for Finance, HR, Engineering, Research, Operations, or any repeatable professional workflow.",
          },
          {
            icon: "message",
            title: "Chat channels",
            desc: "Use the same agents from Web UI, terminal, Telegram, QQ, Feishu, and WeChat.",
          },
        ],
      },
      {
        label: "Agent capability",
        items: [
          {
            icon: "zap",
            title: "Skills",
            desc: "Reusable playbooks teach agents consistent methods for review, screening, reporting, and follow-up.",
          },
          {
            icon: "plug",
            title: "Tools and OAuth",
            desc: "Agents can use connected services, APIs, files, notifications, and plugin-provided tools.",
          },
          {
            icon: "brain",
            title: "Memory and knowledge",
            desc: "Combine professional reference material with per-user memory and shared preference context.",
          },
        ],
      },
      {
        label: "Execution",
        items: [
          {
            icon: "task",
            title: "Task system",
            desc: "Plan goals, track dependencies, define acceptance criteria, and route work through review.",
          },
          {
            icon: "recally",
            title: "Recally",
            desc: "Save web pages and PDFs, subscribe to RSS, summarize, chat, and generate digests.",
          },
          {
            icon: "shield",
            title: "Sandbox and secrets",
            desc: "Keep execution, network access, credentials, and workspaces inside controlled boundaries.",
          },
        ],
      },
    ],
    ctaTitle: "Run it yourself",
    ctaSub:
      "Start locally, create one shared professional agent, then add users, tools, tasks, and channels.",
    ctaAlt: "Also available via go install and direct binary download.",
  },
  zh: {
    heroEyebrow: "共享 AI 同事",
    heroTitle: ["为每个团队创建", "专业", "Agent。"],
    heroSub:
      "Stella 让业务负责人创建带 instructions、skills、tools、knowledge 和记忆策略的共享 Agent。其他人只需要聊天、交付 goal，并追踪工作进展。",
    productLabel: "stella — goal 工作区",
    systemsTitle: "从共享专业能力到完成工作",
    systemsSub: "一次创建专业 Agent，让组织像使用真正同事一样使用它。",
    systems: [
      {
        icon: "bot",
        title: "创建共享专业 Agent",
        desc: "财务、HR、工程、研究和运营团队都可以创建懂自己工作的 Agent。每个 Agent 都有自己的角色、instructions、knowledge、skills、tools 和渠道。",
        highlights: ["由业务专家负责", "组织内共享使用", "按用户个性化"],
      },
      {
        icon: "task",
        title: "把 goal 变成任务 DAG",
        desc: "给 Agent 一个 goal。Stella 可以规划工作、拆分 tasks、连接依赖、定义验收标准、追踪 blocker，并把结果送入 review。",
        highlights: ["Goal 到 plan", "依赖和 blocker", "验收标准和 review"],
      },
      {
        icon: "recally",
        title: "用 Recally 阅读世界",
        desc: "保存网页和 PDF，获取 AI 总结，围绕文章聊天，订阅 RSS，并维护一个持续跟进重点主题的阅读列表。",
        highlights: ["网页和 PDF", "RSS 和 digests", "围绕文章聊天"],
      },
      {
        icon: "message",
        title: "在日常工作流中使用 Agent",
        desc: "从 Web UI、终端、Telegram、QQ、飞书或微信和 Agent 对话。用户不需要学习每个工作流背后的专业软件。",
        highlights: ["聊天式入口", "共享渠道", "工具藏在 Agent 后面"],
      },
    ],
    pillars: [
      {
        title: "Agent 由业务领域负责",
        body: "懂工作流的人创建 Agent，编写 instructions，挂载 knowledge，并决定 review 边界。",
      },
      {
        title: "所有人使用同一个专家",
        body: "共享财务 Agent 或 HR Agent 给组织一个一致的专业同事，而不是到处散落的个人 prompts。",
      },
      {
        title: "每个 Agent 都可以认识你",
        body: "Agent 保留每用户记忆，也可以在需要时使用共享用户记忆，让偏好跨 Agent 延续。",
      },
      {
        title: "Goal 变成可负责的工作",
        body: "Plan、任务 DAG、验收标准、blocker 和 review 状态让 Agent 工作可检查，而不是埋在聊天里。",
      },
      {
        title: "阅读成为系统",
        body: "Recally 把网页、PDF、RSS、总结和文章聊天变成持续更新的知识流。",
      },
    ],
    capGroups: [
      {
        label: "组织",
        items: [
          {
            icon: "users",
            title: "多租户、多用户",
            desc: "为组织运行 Stella，管理用户、共享 Agent 和隔离的数据边界。",
          },
          {
            icon: "bot",
            title: "共享 Agent 目录",
            desc: "为财务、HR、工程、研究、运营或任何可重复的专业工作流创建 Agent。",
          },
          {
            icon: "message",
            title: "聊天渠道",
            desc: "从 Web UI、终端、Telegram、QQ、飞书和微信使用同一批 Agent。",
          },
        ],
      },
      {
        label: "Agent 能力",
        items: [
          {
            icon: "zap",
            title: "Skills",
            desc: "可复用 playbooks 教 Agent 稳定执行 review、筛选、报告和 follow-up。",
          },
          {
            icon: "plug",
            title: "Tools 和 OAuth",
            desc: "Agent 可以使用连接服务、API、文件、通知和 plugin 提供的 tools。",
          },
          {
            icon: "brain",
            title: "Memory 和 knowledge",
            desc: "把专业参考资料、每用户记忆和共享偏好上下文结合起来。",
          },
        ],
      },
      {
        label: "执行",
        items: [
          {
            icon: "task",
            title: "任务系统",
            desc: "规划 goal，追踪依赖，定义验收标准，并把工作送入 review。",
          },
          {
            icon: "recally",
            title: "Recally",
            desc: "保存网页和 PDF，订阅 RSS，总结、聊天并生成 digests。",
          },
          {
            icon: "shield",
            title: "沙箱和密钥",
            desc: "把执行、网络访问、凭证和工作区限制在受控边界内。",
          },
        ],
      },
    ],
    ctaTitle: "自己运行",
    ctaSub: "从本地开始，创建一个共享专业 Agent，然后添加用户、tools、tasks 和渠道。",
    ctaAlt: "也支持 go install 和直接下载二进制文件。",
  },
};

const ICON_MAP = {
  brain: Brain,
  shield: Shield,
  message: MessageCircle,
  clock: Clock,
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
  const isZh = lang === "zh";
  const metrics = isZh
    ? [
        ["Agent", "财务 / HR / 工程"],
        ["Work", "Goal 到任务 DAG"],
        ["Review", "验收 + 人工确认"],
      ]
    : [
        ["Agent", "Finance / HR / Eng"],
        ["Work", "Goal to task DAG"],
        ["Review", "Criteria + approval"],
      ];

  return (
    <section className="home-hero">
      <div className="home-hero-grain" />
      <div className="home-shell">
        <div className="home-hero-frame">
          <span>
            {isZh ? "Self-hosted agent operating surface" : "Self-hosted agent operating surface"}
          </span>
          <span>
            {isZh
              ? "Multi-user / shared agents / reviewable work"
              : "Multi-user / shared agents / reviewable work"}
          </span>
        </div>
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
            <dl
              className="home-hero-metrics"
              aria-label={isZh ? "Stella 核心模型" : "Stella operating model"}
            >
              {metrics.map(([label, value]) => (
                <div key={label}>
                  <dt>{label}</dt>
                  <dd>{value}</dd>
                </div>
              ))}
            </dl>
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
  const agents = isZh
    ? [
        ["Finance", "报销 / 发票 / 审批", "4.2k memories"],
        ["HR", "内推 / 筛选 / 面试", "12 workflows"],
        ["Engineering", "评审 / 发布 / 事故", "38 skills"],
      ]
    : [
        ["Finance", "Reimbursements / invoices", "4.2k memories"],
        ["HR", "Referrals / screening", "12 workflows"],
        ["Engineering", "Review / release / incidents", "38 skills"],
      ];
  const tasks = isZh
    ? [
        ["01", "提取票据字段", "done"],
        ["02", "检查报销制度", "done"],
        ["03", "补齐参与人名单", "blocked"],
        ["04", "财务 review", "review"],
      ]
    : [
        ["01", "Extract receipt fields", "done"],
        ["02", "Check reimbursement policy", "done"],
        ["03", "Collect attendee details", "blocked"],
        ["04", "Finance review", "review"],
      ];

  return (
    <div className="home-product" aria-label={c.productLabel}>
      <div className="home-product-chrome">
        <span className="home-product-status" />
        <span className="home-product-chrome-label">
          {isZh ? "ORG WORKBENCH" : "ORG WORKBENCH"}
        </span>
        <span className="home-product-title">{c.productLabel}</span>
      </div>
      <div className="home-product-body">
        <section className="home-product-panel home-product-panel-agents">
          <div className="home-product-panel-head">
            <span>{isZh ? "共享 Agent" : "Shared agents"}</span>
            <Bot className="size-4" />
          </div>
          <div className="home-agent-list">
            {agents.map(([name, desc, meta], index) => (
              <div
                key={name}
                className={index === 0 ? "home-agent-row is-active" : "home-agent-row"}
              >
                <div>
                  <strong>{name}</strong>
                  <span>{desc}</span>
                </div>
                <small>{meta}</small>
              </div>
            ))}
          </div>
          <div className="home-agent-policy">
            <Lock className="size-3.5" />
            <span>
              {isZh ? "每用户记忆 + 共享知识边界" : "Per-user memory + shared knowledge boundary"}
            </span>
          </div>
        </section>

        <section className="home-product-panel home-product-panel-work">
          <div className="home-product-panel-head">
            <span>{isZh ? "Goal intake" : "Goal intake"}</span>
            <MessageCircle className="size-4" />
          </div>
          <div className="home-goal-card">
            <p>
              {isZh
                ? "检查客户晚餐报销材料；缺什么告诉我，有例外就安排财务 review。"
                : "Check this client dinner reimbursement. Tell me what is missing and route exceptions to finance review."}
            </p>
            <div className="home-goal-meta">
              <span>{isZh ? "Agent: Finance" : "Agent: Finance"}</span>
              <span>{isZh ? "Tools: files, policy, notify" : "Tools: files, policy, notify"}</span>
            </div>
          </div>
          <div className="home-task-dag" aria-label={isZh ? "任务 DAG" : "Task DAG"}>
            {tasks.map(([id, title, state]) => (
              <div key={id} className={`home-task-node is-${state}`}>
                <span>{id}</span>
                <strong>{title}</strong>
                <small>{state}</small>
              </div>
            ))}
          </div>
        </section>

        <section className="home-product-panel home-product-panel-review">
          <div className="home-product-panel-head">
            <span>{isZh ? "Review queue" : "Review queue"}</span>
            <Shield className="size-4" />
          </div>
          <div className="home-review-card">
            <span className="home-review-state">{isZh ? "NEEDS HUMAN" : "NEEDS HUMAN"}</span>
            <strong>
              {isZh ? "票据金额超出团队晚餐阈值" : "Receipt exceeds team dinner threshold"}
            </strong>
            <p>
              {isZh
                ? "Agent 已引用制度条款并准备 review packet。"
                : "Agent cited the policy rule and prepared the review packet."}
            </p>
          </div>
          <div className="home-review-actions">
            <span>{isZh ? "验收标准 4/5" : "Criteria 4/5"}</span>
            <span>{isZh ? "等待财务确认" : "Waiting on finance"}</span>
          </div>
        </section>
      </div>

      <div className="home-product-features">
        <span className="home-product-badge home-product-badge-1">
          <Brain className="size-3" />
          {isZh ? "按用户记忆偏好" : "Per-user memory"}
        </span>
        <span className="home-product-badge home-product-badge-2">
          <ListTodo className="size-3" />
          {isZh ? "Goal 到任务 DAG" : "Goal to task DAG"}
        </span>
        <span className="home-product-badge home-product-badge-3">
          <Lock className="size-3" />
          {isZh ? "Review gate" : "Review gates"}
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
