import { createFileRoute, Link } from "@tanstack/react-router";
import { ArrowRight } from "lucide-react";
import { siGithub } from "simple-icons";
import { t } from "@/lib/docs/translations";
import { SiteHeader } from "@/components/SiteHeader";
import { useI18n } from "@/lib/i18n";
import "./index.css";

export const Route = createFileRoute("/")({ component: Home });

const copy = {
  en: {
    heroLabel: "private operations ledger",
    heroTitle: "One Stella, many relationships.",
    heroBody:
      "A long-running AI partner system for people who share work, homes, routines, and tools. Stella keeps memory attached to the right person, work attached to the right agent, and execution inside the boundaries you choose.",
    ledgerTitle: "Today in the shared workspace",
    ledgerMeta: "May 17 / Workroom",
    ledgerRows: [
      ["08:10", "Maya", "asks Operations to keep launch readiness weekly until beta settles"],
      ["08:12", "Stella", "keeps that decision in the team thread, not in your private memory"],
      ["09:30", "You", "own release notes and the Friday smoke test follow-up"],
      ["09:31", "Stella", "schedules the task inside the sandboxed workspace"],
    ],
    memoryLabel: "memory has ownership",
    memoryTitle: "The same agent can know different people differently.",
    memoryBody:
      "Stella does not flatten a home, a team, and a private project into one vague profile. Each user-agent relationship gets its own memory, while shared threads remain shared.",
    conversation: [
      ["Maya", "Can Stella keep the launch checklist moving every week?"],
      [
        "Stella",
        "Yes. Operations owns launch readiness. You own release notes. The smoke test runs Friday in the sandbox.",
      ],
      ["You", "Is that from my memory or the team thread?"],
      [
        "Stella",
        "Both, but stored separately: your responsibility is private to you; Maya's cadence belongs to the team.",
      ],
    ],
    scopes: [
      ["person", "preferences, responsibilities, private context"],
      ["agent", "role, tools, model, skills, working style"],
      ["thread", "decisions shared by a team, home, or channel"],
    ],
    workLabel: "work has boundaries",
    workTitle: "Agents do useful work where you let them.",
    workRows: [
      [
        "Channel",
        "Telegram, QQ, Feishu, WeChat, Web UI, and terminal all lead to the same system.",
      ],
      [
        "Workspace",
        "Files, commands, and background tasks stay inside explicit execution boundaries.",
      ],
      [
        "Routine",
        "Schedules, reminders, reading digests, and follow-ups keep moving after restart.",
      ],
    ],
    capabilityLabel: "capability ledger",
    capabilityTitle: "Small pieces, clear responsibilities.",
    ctaLabel: "run it yourself",
  },
  zh: {
    heroLabel: "私人事务运行记录",
    heroTitle: "一套 Stella，多组关系。",
    heroBody:
      "Stella 是为共享工作、家庭日常、长期任务和可信工具而设计的 AI 伙伴系统。记忆属于正确的人，工作属于正确的 agent，执行发生在你允许的边界内。",
    ledgerTitle: "今天的共享工作区",
    ledgerMeta: "5 月 17 日 / Workroom",
    ledgerRows: [
      ["08:10", "Maya", "要求 Operations 每周维护发布检查，直到 beta 稳定"],
      ["08:12", "Stella", "把这个决定保留在团队线程，而不是你的私人记忆"],
      ["09:30", "你", "负责 release notes 和周五的 smoke test 跟进"],
      ["09:31", "Stella", "把任务安排进带沙箱边界的工作区"],
    ],
    memoryLabel: "记忆有归属",
    memoryTitle: "同一个 agent，可以以不同方式理解不同的人。",
    memoryBody:
      "Stella 不会把家庭、团队和私人项目压成一份模糊画像。每个用户和 agent 的关系都有自己的记忆，共享线程则保持共享。",
    conversation: [
      ["Maya", "Stella 可以每周推进 launch checklist 吗？"],
      [
        "Stella",
        "可以。Operations 负责发布准备，你负责 release notes，smoke test 周五在沙箱里运行。",
      ],
      ["你", "这是来自我的记忆，还是团队线程？"],
      ["Stella", "两者都有，但分开存放：你的职责属于你，Maya 的节奏属于团队。"],
    ],
    scopes: [
      ["person", "偏好、职责、私人上下文"],
      ["agent", "角色、工具、模型、技能、工作方式"],
      ["thread", "团队、家庭或渠道共享的决定"],
    ],
    workLabel: "工作有边界",
    workTitle: "agent 只在你允许的地方做有用的事。",
    workRows: [
      ["Channel", "Telegram、QQ、飞书、微信、Web UI 和终端都进入同一套系统。"],
      ["Workspace", "文件、命令和后台任务都留在明确的执行边界中。"],
      ["Routine", "定时任务、提醒、阅读摘要和后续跟进会在重启后继续。"],
    ],
    capabilityLabel: "能力账本",
    capabilityTitle: "小组件，清晰职责。",
    ctaLabel: "自己运行",
  },
};

function Home() {
  const { locale } = useI18n();
  const lang = locale === "zh" ? "zh" : "en";

  return (
    <div className="relative isolate flex min-h-svh flex-col bg-background text-foreground">
      <SiteHeader />
      <main className="home-page flex-1">
        <HeroSection lang={lang} />
        <MemorySection lang={lang} />
        <WorkSection lang={lang} />
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
      <div className="home-shell home-hero-grid">
        <div className="home-hero-copy">
          <p className="home-kicker">{c.heroLabel}</p>
          <h1 className="home-hero-title">{c.heroTitle}</h1>
          <p className="home-hero-body">{c.heroBody}</p>
          <div className="home-actions">
            <Link to="/docs/$" params={{ _splat: "" }} className="home-button home-button-primary">
              {tr.readTheDocs}
              <ArrowRight aria-hidden className="size-4" />
            </Link>
            <a
              href="https://github.com/CherryHQ/stella"
              target="_blank"
              rel="noopener noreferrer"
              className="home-button home-button-secondary"
            >
              <svg aria-hidden viewBox="0 0 24 24" className="size-4 fill-current">
                <path d={siGithub.path} />
              </svg>
              {tr.sourceOnGithub}
            </a>
          </div>
        </div>

        <section className="home-ledger" aria-label={c.ledgerTitle}>
          <div className="home-ledger-head">
            <div>
              <p>{c.ledgerTitle}</p>
              <span>{c.ledgerMeta}</span>
            </div>
            <img src="/avatar.png" alt="Stella" />
          </div>
          <div className="home-ledger-rows">
            {c.ledgerRows.map(([time, actor, event]) => (
              <div key={`${time}-${actor}`} className="home-ledger-row">
                <span>{time}</span>
                <strong>{actor}</strong>
                <p>{event}</p>
              </div>
            ))}
          </div>
        </section>
      </div>
    </section>
  );
}

function MemorySection({ lang }: { lang: keyof typeof copy }) {
  const c = copy[lang];

  return (
    <section className="home-memory">
      <div className="home-shell home-memory-grid">
        <div className="home-memory-copy">
          <p className="home-kicker">{c.memoryLabel}</p>
          <h2 className="home-section-title">{c.memoryTitle}</h2>
          <p className="home-section-body">{c.memoryBody}</p>
        </div>

        <div className="home-memory-panel">
          <div className="home-transcript" aria-label="Conversation">
            {c.conversation.map(([speaker, text]) => (
              <div key={`${speaker}-${text}`} className="home-transcript-line">
                <span>{speaker}</span>
                <p>{text}</p>
              </div>
            ))}
          </div>
          <div className="home-scope-list">
            {c.scopes.map(([label, value]) => (
              <div key={label}>
                <span>{label}</span>
                <p>{value}</p>
              </div>
            ))}
          </div>
        </div>
      </div>
    </section>
  );
}

function WorkSection({ lang }: { lang: keyof typeof copy }) {
  const c = copy[lang];

  return (
    <section className="home-work">
      <div className="home-shell home-work-grid">
        <div>
          <p className="home-kicker">{c.workLabel}</p>
          <h2 className="home-section-title">{c.workTitle}</h2>
        </div>
        <div className="home-rulebook">
          {c.workRows.map(([label, body]) => (
            <div key={label} className="home-rule">
              <span>{label}</span>
              <p>{body}</p>
            </div>
          ))}
        </div>
      </div>
    </section>
  );
}

function CapabilitiesSection({ lang }: { lang: keyof typeof copy }) {
  const tr = t(lang);
  const c = copy[lang];
  const capabilities = [
    [tr.capUsers, tr.capUsersDesc],
    [tr.capAgents, tr.capAgentsDesc],
    [tr.capSandbox, tr.capSandboxDesc],
    [tr.capSkills, tr.capSkillsDesc],
    [tr.capReading, tr.capReadingDesc],
    [tr.capVault, tr.capVaultDesc],
    [tr.capOAuth, tr.capOAuthDesc],
    [tr.capPlugins, tr.capPluginsDesc],
    [tr.capModels, tr.capModelsDesc],
    [tr.capNotifications, tr.capNotificationsDesc],
  ];

  return (
    <section className="home-capabilities">
      <div className="home-shell home-capabilities-grid">
        <div>
          <p className="home-kicker">{c.capabilityLabel}</p>
          <h2 className="home-section-title">{c.capabilityTitle}</h2>
        </div>
        <div className="home-capability-list">
          {capabilities.map(([title, body]) => (
            <div key={title} className="home-capability">
              <h3>{title}</h3>
              <p>{body}</p>
            </div>
          ))}
        </div>
      </div>
    </section>
  );
}

function FooterCTA({ lang }: { lang: keyof typeof copy }) {
  const tr = t(lang);
  const c = copy[lang];

  return (
    <section className="home-cta">
      <div className="home-shell home-cta-grid">
        <div>
          <p className="home-kicker">{c.ctaLabel}</p>
          <h2 className="home-section-title">{tr.getStarted}</h2>
          <p className="home-section-body">{tr.getStartedBody}</p>
        </div>
        <div className="home-terminal" aria-label="Install Stella">
          <code>
            <span>$</span> brew install CherryHQ/tap/stella
          </code>
          <code>
            <span>$</span> stella server
          </code>
          <p>{tr.getStartedAlt}</p>
        </div>
      </div>
    </section>
  );
}
