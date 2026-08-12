import { createFileRoute, Link } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import {
  ArrowRight,
  Bot,
  Check,
  CheckCircle2,
  AlertTriangle,
  Clock,
  MessageSquare,
  MessageCircle,
  Send,
  Globe,
  Shield,
  Brain,
  Plug,
  CalendarClock,
  Rss,
  Search,
  Eye,
  FileText,
  Copy,
  Terminal,
} from "lucide-react";
import { siGithub } from "simple-icons";
import { t } from "@/lib/docs/translations";
import { SiteHeader } from "@/components/SiteHeader";
import { useI18n } from "@/lib/i18n";
import "./index.css";

export const Route = createFileRoute("/")({ component: Home });

type Lang = "en" | "zh";
type Copy = { en: string; zh: string };

function useReveal() {
  const ref = useRef<HTMLElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const targets = el.querySelectorAll(".reveal-element");
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

// The left rail label — a mono margin-note that names this beat of the story.
// The continuous hairline these hang off is the page's signature.
function RailLabel({ children }: { children: ReactNode }) {
  return (
    <aside className="rail-label" aria-hidden>
      {children}
    </aside>
  );
}

function Home() {
  const { locale } = useI18n();
  const lang: Lang = locale === "zh" ? "zh" : "en";
  const mainRef = useReveal();
  const progress = useScrollProgress();

  return (
    <div className="relative isolate flex min-h-svh flex-col bg-background text-foreground home-page">
      <a href="#main-content" className="home-skip-link">
        {lang === "zh" ? "跳到主要内容" : "Skip to main content"}
      </a>
      <div className="home-progress" style={{ transform: `scaleX(${progress})` }} />
      <SiteHeader />
      <main ref={mainRef} id="main-content" className="flex-1">
        <div className="rail-track">
          <HeroSection lang={lang} />
          <PillarsSection lang={lang} />
          <AgentExplorerSection lang={lang} />
          <RecallySection lang={lang} />
        </div>
        <FooterCTA lang={lang} />
      </main>
    </div>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// HERO — the signature: one agent answering inside the channels a team already
// uses. Each tab is a real scenario for a real person, not an abstract demo.
// ─────────────────────────────────────────────────────────────────────────────

type Step = { kind: "ok" | "warn"; en: string; zh: string };
type Bubble =
  | { type: "schedule"; en: string; zh: string }
  | { type: "ask"; who: Copy; initial: string; en: string; zh: string }
  | { type: "reply"; en: string; zh: string }
  | { type: "work"; steps: Step[] }
  | { type: "gate"; en: string; zh: string; primary: Copy; ghost: Copy }
  | { type: "auto"; en: string; zh: string };

interface Channel {
  id: string;
  name: string;
  icon: typeof MessageSquare;
  agent: Copy;
  thread: Copy;
  bubbles: Bubble[];
}

const CHANNELS: Channel[] = [
  {
    id: "feishu",
    name: "飞书",
    icon: MessageSquare,
    agent: { en: "Finance coworker", zh: "财务同事" },
    thread: { en: "#team-ops", zh: "运营大群" },
    bubbles: [
      {
        type: "ask",
        who: { en: "Lena · new hire", zh: "林薇 · 新人" },
        initial: "L",
        en: "How do I file my Q1 travel receipts? Not sure two of them are even allowed.",
        zh: "Q1 的差旅票怎么报？有两张我拿不准能不能报。",
      },
      {
        type: "reply",
        en: "On it. I read the travel policy and checked all four receipts you uploaded.",
        zh: "我来。已经读了差旅报销制度，核对了你上传的 4 张票。",
      },
      {
        type: "work",
        steps: [
          { kind: "ok", en: "Flight ¥850 — within policy", zh: "机票 ¥850 — 合规" },
          { kind: "ok", en: "Software ¥29 — within policy", zh: "软件 ¥29 — 合规" },
          {
            kind: "warn",
            en: "Dinner ¥120 — over the ¥100 limit",
            zh: "正餐 ¥120 — 超单人 ¥100 限额",
          },
          { kind: "warn", en: "Spa ¥200 — not reimbursable", zh: "SPA ¥200 — 不可报销" },
        ],
      },
      {
        type: "gate",
        en: "Two need your sign-off before I submit the rest.",
        zh: "两张需你确认，其余我可以直接提交。",
        primary: { en: "Approve & submit", zh: "批准并提交" },
        ghost: { en: "View detail", zh: "查看明细" },
      },
    ],
  },
  {
    id: "wechat",
    name: "微信",
    icon: MessageCircle,
    agent: { en: "Hiring coworker", zh: "招聘同事" },
    thread: { en: "Eng referrals", zh: "工程内推群" },
    bubbles: [
      {
        type: "ask",
        who: { en: "Wei · engineer", zh: "张工 · 工程师" },
        initial: "W",
        en: "Referring a backend candidate — resume's in the group. Worth a look?",
        zh: "我内推一个后端，简历发群里了，合适吗？",
      },
      {
        type: "reply",
        en: "Read it against the open role. Strong match — here's why.",
        zh: "已对照在招岗位看过，匹配度不错，理由如下。",
      },
      {
        type: "work",
        steps: [
          { kind: "ok", en: "5 yrs Go, distributed systems", zh: "5 年 Go、分布式经验" },
          { kind: "ok", en: "82% overlap with the role", zh: "与岗位要求重合 82%" },
          { kind: "ok", en: "Drafted an interview invite + slot", zh: "已起草面试邀约和时间" },
        ],
      },
      {
        type: "gate",
        en: "I'll send the invite once you say go.",
        zh: "你点头我就把邀约发出去。",
        primary: { en: "Approve & send", zh: "批准发送" },
        ghost: { en: "Reschedule", zh: "改时间" },
      },
    ],
  },
  {
    id: "telegram",
    name: "Telegram",
    icon: Send,
    agent: { en: "Research coworker", zh: "研究同事" },
    thread: { en: "Morning digest", zh: "晨间摘要" },
    bubbles: [
      { type: "schedule", en: "Runs every weekday at 7:30", zh: "每个工作日 7:30 自动运行" },
      {
        type: "reply",
        en: "Your six feeds moved over the weekend. Three are worth your time.",
        zh: "你订阅的 6 个源周末有更新，挑了 3 条要紧的。",
      },
      {
        type: "work",
        steps: [
          { kind: "ok", en: "Competitor X launched a team plan", zh: "竞品 X 上线团队版" },
          { kind: "ok", en: "New self-hosting LLM write-up", zh: "一篇自部署 LLM 实践" },
          { kind: "ok", en: "Pricing shift in your space", zh: "你所在赛道的定价变动" },
        ],
      },
      {
        type: "auto",
        en: "No approval needed — delivered straight to you.",
        zh: "无需审核，已直接推送给你。",
      },
    ],
  },
  {
    id: "web",
    name: "Web",
    icon: Globe,
    agent: { en: "Engineering coworker", zh: "工程同事" },
    thread: { en: "Pre-review", zh: "预审" },
    bubbles: [
      {
        type: "ask",
        who: { en: "Chen · engineer", zh: "小陈 · 工程师" },
        initial: "C",
        en: "Can you pre-review PR #568 before I ping the team?",
        zh: "这个 PR #568 能帮我先过一遍再喊大家吗？",
      },
      {
        type: "reply",
        en: "Read the whole diff. Three things to look at, one likely bug.",
        zh: "整份 diff 都看了。三处要注意，一处可能有问题。",
      },
      {
        type: "work",
        steps: [
          { kind: "ok", en: "Migration matches the schema", zh: "迁移脚本与 schema 一致" },
          { kind: "warn", en: "One transaction may deadlock", zh: "一处事务可能死锁" },
          { kind: "ok", en: "Left inline comments", zh: "已贴好行内评论" },
        ],
      },
      {
        type: "gate",
        en: "Want me to post the review for you?",
        zh: "要我直接把评审意见提交上去吗？",
        primary: { en: "Post review", zh: "提交评审" },
        ghost: { en: "Keep private", zh: "仅给我看" },
      },
    ],
  },
];

const STEP_DELAY = 820;

function ChannelThread({ lang }: { lang: Lang }) {
  const isZh = lang === "zh";
  const [active, setActive] = useState(0);
  const [shown, setShown] = useState(0);
  const [pinned, setPinned] = useState(false);

  const channel = CHANNELS[active];
  const total = channel.bubbles.length;

  // Reveal the active scenario one bubble at a time. Replays whenever the
  // channel changes (auto-rotation or a tab click).
  useEffect(() => {
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      setShown(total);
      return;
    }
    setShown(0);
    const timers = channel.bubbles.map((_, i) =>
      window.setTimeout(() => setShown(i + 1), 450 + i * STEP_DELAY),
    );
    return () => timers.forEach(clearTimeout);
  }, [active, total, channel.bubbles]);

  // Drift to the next channel on its own, until the visitor takes over by
  // clicking a tab.
  useEffect(() => {
    if (pinned || window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
    const tm = window.setTimeout(
      () => setActive((a) => (a + 1) % CHANNELS.length),
      450 + total * STEP_DELAY + 3400,
    );
    return () => clearTimeout(tm);
  }, [active, total, pinned]);

  const pick = (i: number) => {
    setPinned(true);
    setActive(i);
  };

  const ChannelIcon = channel.icon;

  return (
    <div className="thread-card">
      <div className="thread-tabs" role="tablist" aria-label={isZh ? "渠道" : "Channels"}>
        {CHANNELS.map((c, i) => {
          const Icon = c.icon;
          return (
            <button
              key={c.id}
              role="tab"
              aria-selected={active === i}
              onClick={() => pick(i)}
              className={`thread-tab ${active === i ? "active" : ""}`}
            >
              <Icon aria-hidden className="size-3.5" />
              <span>{c.name}</span>
            </button>
          );
        })}
      </div>

      <div className="thread-meta">
        <span className="thread-meta-thread">
          <ChannelIcon aria-hidden className="size-3" />
          {channel.thread[lang]}
        </span>
        <span className="thread-meta-agent">
          <span className="thread-meta-dot" />
          {channel.agent[lang]}
        </span>
      </div>

      <div className="thread-body" key={channel.id}>
        {channel.bubbles.slice(0, shown).map((b, i) => (
          <BubbleView
            key={`${channel.id}-${i}`}
            bubble={b}
            lang={lang}
            agent={channel.agent[lang]}
          />
        ))}
        {shown < total && <TypingDots />}
      </div>
    </div>
  );
}

function BubbleView({ bubble, lang, agent }: { bubble: Bubble; lang: Lang; agent: string }) {
  if (bubble.type === "schedule") {
    return (
      <div className="thread-row thread-row-note">
        <span className="thread-note">
          <Clock aria-hidden className="size-3" />
          {bubble[lang]}
        </span>
      </div>
    );
  }

  if (bubble.type === "ask") {
    return (
      <div className="thread-row thread-row-ask">
        <div className="thread-ask">
          <div className="thread-ask-who">{bubble.who[lang]}</div>
          <div className="thread-bubble thread-bubble-ask">{bubble[lang]}</div>
        </div>
        <span className="thread-avatar thread-avatar-user">{bubble.initial}</span>
      </div>
    );
  }

  return (
    <div className="thread-row thread-row-agent">
      <span className="thread-avatar thread-avatar-agent">
        <Bot aria-hidden className="size-4" />
      </span>
      <div className="thread-agent">
        <div className="thread-agent-name">{agent}</div>
        {bubble.type === "reply" && (
          <div className="thread-bubble thread-bubble-agent">{bubble[lang]}</div>
        )}
        {bubble.type === "work" && (
          <div className="thread-work">
            {bubble.steps.map((s, i) => (
              <div key={i} className={`thread-step ${s.kind}`}>
                {s.kind === "ok" ? (
                  <CheckCircle2 aria-hidden className="size-3.5" />
                ) : (
                  <AlertTriangle aria-hidden className="size-3.5" />
                )}
                <span>{s[lang]}</span>
              </div>
            ))}
          </div>
        )}
        {bubble.type === "gate" && (
          <div className="thread-gate">
            <div className="thread-gate-text">
              <Shield aria-hidden className="size-3.5" />
              {bubble[lang]}
            </div>
            <div className="thread-gate-actions">
              <span className="thread-chip thread-chip-primary">{bubble.primary[lang]}</span>
              <span className="thread-chip">{bubble.ghost[lang]}</span>
            </div>
          </div>
        )}
        {bubble.type === "auto" && (
          <div className="thread-bubble thread-bubble-auto">
            <CheckCircle2 aria-hidden className="size-3.5" />
            {bubble[lang]}
          </div>
        )}
      </div>
    </div>
  );
}

function TypingDots() {
  return (
    <div className="thread-row thread-row-agent">
      <span className="thread-avatar thread-avatar-agent">
        <Bot aria-hidden className="size-4" />
      </span>
      <div className="thread-typing" aria-hidden>
        <span />
        <span />
        <span />
      </div>
    </div>
  );
}

function HeroSection({ lang }: { lang: Lang }) {
  const isZh = lang === "zh";
  const tr = t(lang);

  return (
    <section className="home-hero rail-section">
      <div className="home-shell rail-grid">
        <RailLabel>{isZh ? "新人开口" : "The ask"}</RailLabel>
        <div className="rail-body home-hero-body">
          <div className="home-hero-layout">
            <div className="home-hero-copy">
              <div className="home-eyebrow">
                {isZh ? "自部署 · 团队共享 AI 同事" : "Self-hosted · Shared AI coworkers"}
              </div>
              <h1 className="home-h1">
                <span>{isZh ? "团队里重复的问题" : "Your team's repeat questions"}</span>
                <em>{isZh ? "不必再麻烦" : "don't need"}</em>
                <span>{isZh ? "你的专家" : "your experts."}</span>
              </h1>
              <p className="home-lead">
                {isZh
                  ? "财务、HR、研究的 agent 只配一次。那个本来要私聊专家的新人，直接在群里问就行——拿到答案、活也干完，关键处还停下来等你拍板。"
                  : "Set up a finance, HR, or research agent once. The new hire who'd normally DM your expert just asks the group chat — and gets the answer, the work done, and your sign-off where it matters."}
              </p>
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
              <p className="home-hero-foot">
                {isZh
                  ? "飞书 · 微信 · Telegram · QQ · Web · 终端 — 都是同一个 AI 同事的入口"
                  : "Feishu · WeChat · Telegram · QQ · Web · CLI — every one a door to the same coworker"}
              </p>
            </div>

            <div className="home-hero-preview">
              <ChannelThread lang={lang} />
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// PILLARS — why a shared agent fits a team, in value terms (no mechanism names).
// ─────────────────────────────────────────────────────────────────────────────

interface Pillar {
  icon: typeof MessageCircle;
  title: Copy;
  body: Copy;
}

const PILLARS: Pillar[] = [
  {
    icon: MessageCircle,
    title: { en: "Nobody learns a new system", zh: "没人需要学新系统" },
    body: {
      en: "It lives in the Feishu, WeChat, or Telegram group your team already uses. No new app, no seats to hand out — people just ask.",
      zh: "它就在团队已经在用的飞书、微信、Telegram 群里。没有新 app，不用逐人开账号——大家张口问就行。",
    },
  },
  {
    icon: Brain,
    title: { en: "It remembers each teammate", zh: "它记得每个人" },
    body: {
      en: "Every person gets their own context. The new hire and the finance lead ask the same agent and never re-explain who they are.",
      zh: "每个人都有自己的上下文。新人和财务负责人问的是同一个 agent，却都不用重新解释自己是谁。",
    },
  },
  {
    icon: Shield,
    title: { en: "It works within your limits", zh: "它在你的边界内做事" },
    body: {
      en: "Connect your tools and your Library. The agent does the work — and stops for a human on anything you mark as risky.",
      zh: "接上你的工具和知识库。agent 把活干完，但凡你标为高风险的事，都会停下来等人确认。",
    },
  },
];

function PillarsSection({ lang }: { lang: Lang }) {
  const isZh = lang === "zh";
  return (
    <section className="home-pillars rail-section reveal-element">
      <div className="home-shell rail-grid">
        <RailLabel>{isZh ? "为什么留得住" : "Why it sticks"}</RailLabel>
        <div className="rail-body">
          <div className="home-block-head">
            <h2 className="home-h2">
              {isZh ? "配一次，全团队都能问" : "Set it up once, the whole team just asks"}
            </h2>
            <p className="home-lead">
              {isZh
                ? "一个部门负责人把 agent 装好——挂上工具、政策和知识库。从那一刻起，组织里的每个人都能直接对话调用，零配置、零学习成本。"
                : "A department lead sets the agent up — tools, policy, Library. From then on, anyone in the org uses it by chatting. Nothing to configure, nothing to learn."}
            </p>
          </div>
          <ul className="pillars-list">
            {PILLARS.map((p, i) => {
              const Icon = p.icon;
              return (
                <li key={i} className="pillar-row">
                  <div className="pillar-row-head">
                    <Icon aria-hidden className="size-4 pillar-row-icon" />
                    <h3 className="pillar-row-title">{p.title[lang]}</h3>
                  </div>
                  <p className="pillar-row-body">{p.body[lang]}</p>
                </li>
              );
            })}
          </ul>
        </div>
      </div>
    </section>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// AGENT EXPLORER — the builder side: one owner defines an expert, the team uses it.
// ─────────────────────────────────────────────────────────────────────────────

interface AgentData {
  id: string;
  name: Copy;
  role: Copy;
  instruction: Copy;
  tools: { icon: typeof Eye; name: string }[];
  review: Copy;
}

const AGENTS_LIST: AgentData[] = [
  {
    id: "finance",
    name: { en: "Expense auditor", zh: "报销审计员" },
    role: {
      en: "Reads receipts and checks them against policy",
      zh: "读发票，并对照公司政策核对",
    },
    instruction: {
      en: "Read each receipt. If a meal is over ¥100 or a luxury item shows up, flag it, hold the auto-reimbursement, and ask a human.",
      zh: "逐张读发票。若单餐超过 ¥100 或出现奢侈消费，标记出来、暂停自动报销，并转人工确认。",
    },
    tools: [
      { icon: Eye, name: "read-receipt" },
      { icon: FileText, name: "policy-check" },
      { icon: Plug, name: "ledger-write" },
    ],
    review: {
      en: "Asks a human on any violation or anything over ¥500",
      zh: "出现违规或单笔超过 ¥500 时必经人工确认",
    },
  },
  {
    id: "hr",
    name: { en: "Referral screener", zh: "内推筛选员" },
    role: {
      en: "Matches resumes to roles and lines up interviews",
      zh: "把简历与岗位匹配，并安排面试",
    },
    instruction: {
      en: "Read incoming resumes against the open role. Above an 80% match, notify the referrer and draft a calendar invite for the interviewer.",
      zh: "对照在招岗位读简历。匹配度高于 80% 时，通知推荐人，并为面试官起草日程邀约。",
    },
    tools: [
      { icon: FileText, name: "read-resume" },
      { icon: MessageCircle, name: "notify" },
      { icon: CalendarClock, name: "schedule" },
    ],
    review: {
      en: "Asks a human before any interview invite goes out",
      zh: "正式面试邀约发出前必经人工确认",
    },
  },
  {
    id: "research",
    name: { en: "Reading companion", zh: "阅读助手" },
    role: {
      en: "Watches feeds and writes the daily digest",
      zh: "盯订阅源，写每日摘要",
    },
    instruction: {
      en: "Check the team's feeds each hour. Pull new articles, read them in full, and write a short morning digest worth someone's time.",
      zh: "每小时查看团队订阅源。抓取新文章、通读全文，写一份值得一读的晨间摘要。",
    },
    tools: [
      { icon: Rss, name: "watch-feeds" },
      { icon: Search, name: "read-web" },
      { icon: Brain, name: "summarize" },
    ],
    review: {
      en: "Runs fully on its own — digests just arrive",
      zh: "完全自动运行——摘要按时送达",
    },
  },
];

function AgentExplorerSection({ lang }: { lang: Lang }) {
  const isZh = lang === "zh";
  const [activeId, setActiveId] = useState("finance");
  const activeAgent = AGENTS_LIST.find((a) => a.id === activeId) || AGENTS_LIST[0];

  return (
    <section className="home-builder rail-section reveal-element">
      <div className="home-shell rail-grid">
        <RailLabel>{isZh ? "做一个专家" : "Build one expert"}</RailLabel>
        <div className="rail-body">
          <div className="home-block-head">
            <h2 className="home-h2">
              {isZh ? "做一个，全团队共享" : "Build one expert, share it with everyone"}
            </h2>
            <p className="home-lead">
              {isZh
                ? "你来定它该做什么、能用哪些工具、什么时候必须问人。建好之后，团队对话即用——不必再有人重复回答同一个问题。"
                : "You decide what it does, which tools it can touch, and when it must ask a human. Once it exists, the team uses it by chatting — and no one answers that same question again."}
            </p>
          </div>

          <div className="builder-layout">
            <div className="builder-tabs">
              {AGENTS_LIST.map((agent) => (
                <button
                  key={agent.id}
                  onClick={() => setActiveId(agent.id)}
                  className={`builder-tab ${activeId === agent.id ? "active" : ""}`}
                >
                  <div className="builder-tab-text">
                    <div className="builder-tab-name">{agent.name[lang]}</div>
                    <div className="builder-tab-role">{agent.role[lang]}</div>
                  </div>
                  <ArrowRight aria-hidden className="size-4 builder-tab-arrow" />
                </button>
              ))}
            </div>

            <div key={activeAgent.id} className="home-card builder-panel">
              <div className="builder-panel-head">
                <h3 className="builder-panel-title">{activeAgent.name[lang]}</h3>
                <p className="builder-panel-role">{activeAgent.role[lang]}</p>
              </div>

              <div className="builder-field">
                <span className="builder-label">
                  {isZh ? "你告诉它做什么" : "What it's told to do"}
                </span>
                <div className="builder-prompt">{activeAgent.instruction[lang]}</div>
              </div>

              <div className="builder-field">
                <span className="builder-label">{isZh ? "它能用的工具" : "Tools it can use"}</span>
                <div className="builder-tools">
                  {activeAgent.tools.map((tool, idx) => {
                    const ToolIcon = tool.icon;
                    return (
                      <span key={idx} className="builder-tool">
                        <ToolIcon aria-hidden className="size-3.5" />
                        {tool.name}
                      </span>
                    );
                  })}
                </div>
              </div>

              <div className="builder-review">
                <Shield aria-hidden className="size-4" />
                <span className="builder-review-label">
                  {isZh ? "什么时候问人：" : "When it asks a human:"}
                </span>
                <span className="builder-review-text">{activeAgent.review[lang]}</span>
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// RECALLY — the reading and research surface, shown doing real work.
// ─────────────────────────────────────────────────────────────────────────────

function RecallySection({ lang }: { lang: Lang }) {
  const isZh = lang === "zh";

  return (
    <section className="home-reading rail-section reveal-element">
      <div className="home-shell rail-grid">
        <RailLabel>{isZh ? "它替你读" : "It reads for you"}</RailLabel>
        <div className="rail-body">
          <div className="home-block-head">
            <div className="home-eyebrow">Recally</div>
            <h2 className="home-h2">
              {isZh ? "团队的阅读和研究，自己会整理" : "Your team's reading, kept and summarized"}
            </h2>
            <p className="home-lead">
              {isZh
                ? "保存网页和 PDF、订阅 RSS、收每日摘要，还能一边读一边问。周一站会前，研究同事已经把周末的更新理好了。"
                : "Save pages and PDFs, subscribe to feeds, get a daily digest, and ask questions while you read. By Monday standup, the research coworker has the weekend's updates ready."}
            </p>
          </div>

          <div className="home-card reader-card">
            <div className="reader-chrome">
              <div className="reader-dots" aria-hidden>
                <span className="bg-destructive" />
                <span className="bg-chart-4" />
                <span className="bg-chart-3" />
              </div>
              <span className="reader-url">
                recally · {isZh ? "阅读工作台" : "reader workspace"}
              </span>
            </div>

            <div className="reader-layout">
              <div className="reader-sidebar">
                <div className="reader-sidebar-head">{isZh ? "阅读队列" : "Reading queue"}</div>
                <div className="reader-list">
                  <div className="reader-item active">
                    <div className="reader-item-title">
                      {isZh ? "自部署团队 AI：真正要紧的事" : "Self-hosting team AI: what matters"}
                    </div>
                    <div className="reader-item-meta">stella.sh · {isZh ? "今天" : "Today"}</div>
                  </div>
                  <div className="reader-item">
                    <div className="reader-item-title">
                      {isZh ? "把入职问题砍掉一半" : "Cutting onboarding questions in half"}
                    </div>
                    <div className="reader-item-meta">blog · {isZh ? "昨天" : "Yesterday"}</div>
                  </div>
                  <div className="reader-item">
                    <div className="reader-item-title">
                      {isZh ? "知识库怎么不变成垃圾场" : "Keeping a Library usable"}
                    </div>
                    <div className="reader-item-meta">notes · {isZh ? "3 天前" : "3d ago"}</div>
                  </div>
                </div>
              </div>

              <div className="reader-main">
                <h3 className="reader-article-title">
                  {isZh
                    ? "自部署团队 AI：真正要紧的，不是模型"
                    : "Self-hosting team AI: the model isn't the hard part"}
                </h3>
                <div className="reader-article-meta">
                  {isZh ? "作者 Stella 团队 · 5 分钟读完" : "Stella team · 5 min read"}
                </div>
                <div className="reader-article-body">
                  <p>
                    {isZh
                      ? "大多数团队卡住，不是因为模型不够强，而是因为答案散落在几个人的脑子里。一旦专家请假，问题就堆起来。"
                      : "Most teams don't stall because the model is weak. They stall because the answers live in a few people's heads — and when the expert is out, the questions pile up."}
                  </p>
                  <p className="reader-highlight">
                    {isZh
                      ? "把数据留在你自己的机器上，把知识装进一个团队都能问的同事里——这才是自部署真正买到的东西。"
                      : "Keep the data on your own machine, and put the knowledge into a coworker the whole team can ask. That's what self-hosting actually buys you."}
                  </p>
                  <p>
                    {isZh
                      ? "每个人按各自的权限看到内容，敏感操作停下来等人确认——有用的工作，有清楚的边界。"
                      : "Everyone sees only what their permissions allow, and sensitive moves wait for a human. Useful work, with clear edges."}
                  </p>
                </div>
              </div>

              <div className="reader-chat">
                <div className="reader-chat-head">
                  <MessageCircle aria-hidden className="size-3.5" />
                  {isZh ? "边读边问" : "Ask while you read"}
                </div>
                <div className="reader-chat-body">
                  <div className="reader-bubble user">
                    {isZh ? "一句话讲这篇说了啥？" : "One sentence — what's the point?"}
                  </div>
                  <div className="reader-bubble bot">
                    {isZh
                      ? "把知识从几个人脑子里搬进一个团队都能问的 AI 同事，自部署保证数据和权限都在你手里。"
                      : "Move knowledge out of a few people's heads into a coworker the team can ask — self-hosted, so the data and permissions stay yours."}
                  </div>
                </div>
                <div className="reader-chat-input">
                  <input
                    type="text"
                    readOnly
                    placeholder={isZh ? "再追问一句…" : "Ask a follow-up…"}
                  />
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// FOOTER CTA — get it running, on your own machine.
// ─────────────────────────────────────────────────────────────────────────────

interface InstallTab {
  id: string;
  name: string;
  code: string;
}

const INSTALL_TABS: InstallTab[] = [
  { id: "brew", name: "Homebrew", code: "brew install CherryHQ/tap/stella\nstellad server" },
  {
    id: "docker",
    name: "Docker",
    code: "docker run -d --name stella -p 8080:8080 \\\n  -v ~/.stella:/root/.stella cherryhq/stella:latest",
  },
  {
    id: "go",
    name: "Go install",
    code: "go install github.com/CherryHQ/stella/cmd/stellad@latest\nstellad server",
  },
];

function FooterCTA({ lang }: { lang: Lang }) {
  const isZh = lang === "zh";
  const [activeTab, setActiveTab] = useState("brew");
  const [copied, setCopied] = useState(false);

  const selectedCode = INSTALL_TABS.find((tab) => tab.id === activeTab)?.code || "";

  const handleCopy = () => {
    void navigator.clipboard.writeText(selectedCode);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };

  return (
    <section className="home-cta reveal-element">
      <div className="home-shell home-cta-layout">
        <div className="home-cta-copy">
          <h2 className="home-h2">
            {isZh ? "在你自己的机器上跑起来" : "Run it on your own machine"}
          </h2>
          <p className="home-lead">
            {isZh
              ? "本机几秒启动，或用 Docker 部署给整个团队。你的数据、你的模型密钥，都留在你掌控的基础设施上。"
              : "Up in seconds locally, or deploy with Docker for the whole team. Your data and your model keys stay on infrastructure you control."}
          </p>
        </div>

        <div className="home-card install-card">
          <div className="install-head">
            <div className="install-tabs">
              {INSTALL_TABS.map((tab) => (
                <button
                  key={tab.id}
                  onClick={() => setActiveTab(tab.id)}
                  className={`install-tab ${activeTab === tab.id ? "active" : ""}`}
                >
                  {tab.name}
                </button>
              ))}
            </div>
            <button onClick={handleCopy} className="install-copy">
              {copied ? (
                <>
                  <Check aria-hidden className="size-3.5 text-success" />
                  <span className="text-success">{isZh ? "已复制" : "Copied"}</span>
                </>
              ) : (
                <>
                  <Copy aria-hidden className="size-3.5" />
                  <span>{isZh ? "复制" : "Copy"}</span>
                </>
              )}
            </button>
          </div>
          <div key={activeTab} className="install-body">
            <Terminal aria-hidden className="size-4 install-body-icon" />
            <pre className="install-code">{selectedCode}</pre>
          </div>
        </div>
      </div>
    </section>
  );
}
