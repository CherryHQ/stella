import { createFileRoute, Link } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import {
  ArrowRight,
  Shield,
  MessageCircle,
  Brain,
  Plug,
  CalendarClock,
  ListTodo,
  Rss,
  Copy,
  Check,
  Play,
  RotateCcw,
  Terminal,
  Search,
  Eye,
  FileText,
  CheckCircle2,
} from "lucide-react";
import { siGithub } from "simple-icons";
import { t } from "@/lib/docs/translations";
import { SiteHeader } from "@/components/SiteHeader";
import { useI18n } from "@/lib/i18n";
import "./index.css";

export const Route = createFileRoute("/")({ component: Home });

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

function Home() {
  const { locale } = useI18n();
  const lang = locale === "zh" ? "zh" : "en";
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
        <HeroSection lang={lang} />
        <AgentExplorerSection lang={lang} />
        <PipelineSection lang={lang} />
        <RecallySection lang={lang} />
        <FooterCTA lang={lang} />
      </main>
    </div>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// SUGGESTED GOALS FOR SIMULATOR
// ─────────────────────────────────────────────────────────────────────────────

interface MockTask {
  id: string;
  name: { en: string; zh: string };
  status: "pending" | "running" | "success";
  logs: { en: string[]; zh: string[] };
}

interface SimulatorGoal {
  label: { en: string; zh: string };
  goalText: { en: string; zh: string };
  tasks: MockTask[];
}

const SIMULATOR_GOALS: SimulatorGoal[] = [
  {
    label: { en: "Audit Q1 Expenses", zh: "审计 Q1 报销" },
    goalText: {
      en: "Process all receipts in /Q1_receipts and audit them against company policy. Flag violations.",
      zh: "处理 /Q1_receipts 中的所有发票，并根据公司政策进行审计。标记出违规报销。",
    },
    tasks: [
      {
        id: "task-1",
        name: { en: "List and extract receipts", zh: "扫描并读取发票文件" },
        status: "pending",
        logs: {
          en: [
            "Found 4 receipt images in /Q1_receipts",
            "Extracting OCR text: receipt_01.jpg (Dinner, $120)",
            "Extracting OCR text: receipt_02.jpg (Software, $29)",
            "Extracting OCR text: receipt_03.jpg (Flight, $850)",
            "Extracting OCR text: receipt_04.jpg (Spa, $200)",
          ],
          zh: [
            "在 /Q1_receipts 中发现 4 张发票图片",
            "OCR 文本提取：receipt_01.jpg (正餐, $120)",
            "OCR 文本提取：receipt_02.jpg (软件订阅, $29)",
            "OCR 文本提取：receipt_03.jpg (机票, $850)",
            "OCR 文本提取：receipt_04.jpg (水疗SPA, $200)",
          ],
        },
      },
      {
        id: "task-2",
        name: { en: "Fetch policy rules", zh: "检索公司报销管理制度" },
        status: "pending",
        logs: {
          en: [
            "Reading policy document 'travel_rules_v2.md'...",
            "Rule found: Meal limit is $100 per person.",
            "Rule found: Luxury/spa services are non-reimbursable.",
            "Rule found: Software subscription requires supervisor pre-approval.",
          ],
          zh: [
            "读取政策文档 'travel_rules_v2.md'...",
            "定位规则：单人正餐限额 $100。",
            "定位规则：奢侈品/水疗服务不可报销。",
            "定位规则：软件订阅需主管预先审批。",
          ],
        },
      },
      {
        id: "task-3",
        name: { en: "Audit receipts against policy", zh: "比对发票与审计合规性" },
        status: "pending",
        logs: {
          en: [
            "Auditing receipt_01.jpg ($120 dinner) -> Exceeds $100 limit. Flagged.",
            "Auditing receipt_02.jpg ($29 software) -> Within limits. Approved.",
            "Auditing receipt_03.jpg ($850 flight) -> Valid business travel. Approved.",
            "Auditing receipt_04.jpg ($200 spa) -> Violates 'non-reimbursable spa' rule. Flagged.",
          ],
          zh: [
            "审核 receipt_01.jpg ($120 正餐) -> 超出 $100 限制。已标记违规。",
            "审核 receipt_02.jpg ($29 软件) -> 在额度内。批准。",
            "审核 receipt_03.jpg ($850 航班) -> 合规商务差旅。批准。",
            "审核 receipt_04.jpg ($200 SPA) -> 违反“不可报销水疗”政策。已标记违规。",
          ],
        },
      },
      {
        id: "task-4",
        name: { en: "Generate final audit report", zh: "生成审计报告并提交" },
        status: "pending",
        logs: {
          en: [
            "Writing summary report to 'audit_report.md'...",
            "Approved: $879 | Flagged: $320",
            "Report complete. Routing to Human in the Loop review.",
          ],
          zh: [
            "正在将审计摘要写入 'audit_report.md'...",
            "通过金额: $879 | 违规金额: $320",
            "报告生成完毕。已送入人工审核关卡进行确认。",
          ],
        },
      },
    ],
  },
  {
    label: { en: "Research Competitors", zh: "竞品价格调研" },
    goalText: {
      en: "Crawl pricing page of competitor_x.com and generate a feature-by-feature comparison table.",
      zh: "抓取 competitor_x.com 的价格页面，并生成一份详细的功能比对表格。",
    },
    tasks: [
      {
        id: "task-1",
        name: { en: "Fetch target URLs", zh: "获取目标网页内容" },
        status: "pending",
        logs: {
          en: [
            "Initializing sandboxed WebFetch tool...",
            "HTTP GET competitor_x.com/pricing -> 200 OK",
            "Extracted HTML raw content (45KB)",
          ],
          zh: [
            "初始化沙箱 WebFetch 工具...",
            "HTTP GET competitor_x.com/pricing -> 200 OK",
            "提取 HTML 原始网页内容 (45KB)",
          ],
        },
      },
      {
        id: "task-2",
        name: { en: "Parse pricing schemes", zh: "解析订阅套餐和功能" },
        status: "pending",
        logs: {
          en: [
            "Parsing pricing blocks from DOM...",
            "Found 'Starter' plan: $19/mo, 5 seats",
            "Found 'Pro' plan: $49/mo, 20 seats",
            "Found 'Enterprise' plan: Custom, unlimited seats",
            "Extracting pricing table attributes...",
          ],
          zh: [
            "从 DOM 树解析价格板块...",
            "发现 'Starter' 套餐：$19/月，5 个席位",
            "发现 'Pro' 套餐：$49/月，20 个席位",
            "发现 'Enterprise' 套餐：定制价格，无限席位",
            "正在提取功能比对表属性...",
          ],
        },
      },
      {
        id: "task-3",
        name: { en: "Synthesize comparison data", zh: "比对本品并整合数据" },
        status: "pending",
        logs: {
          en: [
            "Comparing with Stella's own plans...",
            "Mapping overlapping capabilities (Integrations, Sandbox, Tasks)",
            "Drafting pricing differences analysis...",
          ],
          zh: [
            "比对本品 Stella 与竞品的定价...",
            "映射交叉功能点 (集成集成、沙箱执行、任务流水线)",
            "起草价格差异分析报告...",
          ],
        },
      },
      {
        id: "task-4",
        name: { en: "Export markdown comparison", zh: "输出 Markdown 比对表格" },
        status: "pending",
        logs: {
          en: [
            "Writing comparison matrix to 'competitor_comparison.md'...",
            "Comparison table successfully generated with 12 features mapped.",
            "Execution completed.",
          ],
          zh: [
            "将比对矩阵写入 'competitor_comparison.md'...",
            "比对表格生成完毕，共覆盖 12 项功能指标。",
            "运行完成。",
          ],
        },
      },
    ],
  },
];

// ─────────────────────────────────────────────────────────────────────────────
// HERO SECTION & INTERACTIVE GOAL RUNNER
// ─────────────────────────────────────────────────────────────────────────────

function HeroSection({ lang }: { lang: "en" | "zh" }) {
  const isZh = lang === "zh";
  const tr = t(lang);

  const [selectedGoalIndex, setSelectedGoalIndex] = useState(0);
  const [goalText, setGoalText] = useState(SIMULATOR_GOALS[0].goalText[lang]);
  const [isSimulating, setIsSimulating] = useState(false);
  const [simStep, setSimStep] = useState<"idle" | "planning" | "running" | "completed">("idle");
  const [tasks, setTasks] = useState<MockTask[]>([]);
  const [activeTaskId, setActiveTaskId] = useState<string | null>(null);
  const [terminalLogs, setTerminalLogs] = useState<string[]>([]);
  const [simulationIndex, setSimulationIndex] = useState(0);

  // Sync text when language changes
  useEffect(() => {
    if (!isSimulating) {
      setGoalText(SIMULATOR_GOALS[selectedGoalIndex].goalText[lang]);
    }
  }, [lang, selectedGoalIndex, isSimulating]);

  const handleSelectPreset = (idx: number) => {
    if (isSimulating) return;
    setSelectedGoalIndex(idx);
    setGoalText(SIMULATOR_GOALS[idx].goalText[lang]);
  };

  const handleStartSimulation = async () => {
    if (isSimulating) return;
    setIsSimulating(true);
    setSimStep("planning");
    setTerminalLogs([
      isZh
        ? ">> [系统] 收到新目标，正在初始化规划器..."
        : ">> [System] Goal received, initializing planner...",
    ]);

    const presetTasks = JSON.parse(
      JSON.stringify(SIMULATOR_GOALS[selectedGoalIndex].tasks),
    ) as MockTask[];
    setTasks(presetTasks.map((t) => ({ ...t, status: "pending" })));

    let curSimIndex = simulationIndex + 1;
    setSimulationIndex(curSimIndex);

    // Planning Phase
    await delay(1200);
    if (curSimIndex !== simulationIndex + 1) return;

    setSimStep("running");
    setTerminalLogs((prev) => [
      ...prev,
      isZh
        ? ">> [规划器] 规划完成。生成了 4 个相互依赖的任务。"
        : ">> [Planner] Planning complete. Created 4 interdependent tasks.",
      isZh ? ">> [执行器] 开始处理任务流依赖..." : ">> [Executor] Processing task dependencies...",
    ]);

    // Execute tasks sequentially
    for (let i = 0; i < presetTasks.length; i++) {
      if (curSimIndex !== simulationIndex + 1) return;

      const currentTask = presetTasks[i];
      setActiveTaskId(currentTask.id);

      setTasks((prev) =>
        prev.map((t) => (t.id === currentTask.id ? { ...t, status: "running" } : t)),
      );
      setTerminalLogs((prev) => [...prev, `>> [正在运行] ${currentTask.name[lang]}...`]);

      // Print logs of the running task sequentially
      const logs = currentTask.logs[lang];
      for (const logLine of logs) {
        await delay(500);
        if (curSimIndex !== simulationIndex + 1) return;
        setTerminalLogs((prev) => [...prev, `   ${logLine}`]);
      }

      setTasks((prev) =>
        prev.map((t) => (t.id === currentTask.id ? { ...t, status: "success" } : t)),
      );
      setTerminalLogs((prev) => [...prev, `>> [完成] ${currentTask.name[lang]}`]);
      await delay(400);
    }

    if (curSimIndex !== simulationIndex + 1) return;
    setSimStep("completed");
    setActiveTaskId(null);
    setTerminalLogs((prev) => [
      ...prev,
      isZh
        ? ">> [执行完成] 目标任务完全执行成功，等待最终确认。"
        : ">> [Execution Finished] All goals resolved successfully. Awaiting human confirmation.",
    ]);
  };

  const handleReset = () => {
    setIsSimulating(false);
    setSimStep("idle");
    setTasks([]);
    setActiveTaskId(null);
    setTerminalLogs([]);
    setGoalText(SIMULATOR_GOALS[selectedGoalIndex].goalText[lang]);
  };

  return (
    <section className="home-hero">
      <div className="home-shell home-hero-layout">
        <div className="home-hero-copy">
          <div className="home-eyebrow">
            {isZh ? "自部署 · 团队共享 AI 同事" : "Self-hosted · Shared AI coworkers"}
          </div>
          <h1 className="home-hero-title">
            <span>{isZh ? "团队里重复的问题" : "Your team's repeat questions"}</span>
            <em>{isZh ? "不必再麻烦" : "don't need"}</em>
            <span>{isZh ? "你的专家" : "your experts."}</span>
          </h1>
          <p className="home-hero-body">
            {isZh
              ? "为财务、HR、工程或研究配置一个共享 agent，只需一次。团队里的每个人都能在自己已经在用的聊天工具里直接问它——它在安全沙箱里把活干完，并在关键处停下来等你审批。"
              : "Set up a shared agent for finance, HR, engineering, or research once. Everyone on the team asks it in the chat tools they already use — it does the work in a safe sandbox and stops for your approval where it matters."}
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
        </div>

        {/* ─── GOAL SIMULATOR ─── */}
        <div className="home-hero-preview">
          <div className="simulator-card">
            <div className="simulator-header">
              <span className="flex items-center gap-2">
                <span className="simulator-dot bg-destructive" />
                <span className="simulator-dot bg-chart-4" />
                <span className="simulator-dot bg-chart-3" />
                <span className="text-xs font-mono ml-2 text-muted-foreground">
                  stella-terminal
                </span>
              </span>
              {isSimulating && (
                <button onClick={handleReset} className="simulator-reset-btn">
                  <RotateCcw className="size-3" />
                  <span>{isZh ? "重置" : "Reset"}</span>
                </button>
              )}
            </div>

            {/* Input State */}
            {simStep === "idle" && (
              <div className="simulator-input-view">
                <label className="simulator-label">
                  {isZh ? "输入您需要执行的业务目标：" : "Type a business goal for Stella:"}
                </label>
                <div className="simulator-input-wrapper">
                  <textarea
                    value={goalText}
                    onChange={(e) => setGoalText(e.target.value)}
                    placeholder={
                      isZh
                        ? "例如：提取发票信息，根据报销政策生成财务审计报告..."
                        : "e.g. Audit travel receipts and check compliance rules..."
                    }
                    className="simulator-textarea"
                  />
                  <button onClick={handleStartSimulation} className="simulator-run-btn">
                    <Play className="size-4 fill-current" />
                    <span>{isZh ? "开始执行" : "Run Goal"}</span>
                  </button>
                </div>

                <div className="simulator-presets">
                  <span className="text-xs text-muted-foreground">
                    {isZh ? "推荐模版：" : "Try a preset:"}
                  </span>
                  <div className="flex flex-wrap gap-1.5 mt-1.5">
                    {SIMULATOR_GOALS.map((g, i) => (
                      <button
                        key={i}
                        onClick={() => handleSelectPreset(i)}
                        className={`simulator-preset-chip ${selectedGoalIndex === i ? "active" : ""}`}
                      >
                        {g.label[lang]}
                      </button>
                    ))}
                  </div>
                </div>
              </div>
            )}

            {/* Simulating State (Planning & Running) */}
            {simStep !== "idle" && (
              <div className="simulator-running-view">
                <div className="simulator-dag-container">
                  <div className="simulator-dag-header">
                    <span className="font-mono text-xs text-muted-foreground">
                      {isZh ? "任务网络树 (DAG View)" : "Planned Task Network (DAG View)"}
                    </span>
                    <span className="simulator-badge">
                      {simStep === "planning" && (isZh ? "正在规划..." : "Planning...")}
                      {simStep === "running" && (isZh ? "正在执行任务..." : "Running...")}
                      {simStep === "completed" && (isZh ? "已完成" : "Completed")}
                    </span>
                  </div>

                  {/* Visual Node Graph */}
                  <div className="dag-graph">
                    {tasks.map((t, idx) => {
                      const isTaskRunning = t.status === "running";
                      const isTaskSuccess = t.status === "success";
                      const isTaskPending = t.status === "pending";

                      return (
                        <div key={t.id} className="dag-node-wrapper">
                          <div
                            className={`dag-node ${t.status} ${activeTaskId === t.id ? "active" : ""}`}
                          >
                            <span className="dag-node-icon">
                              {isTaskSuccess ? (
                                <CheckCircle2 className="size-3.5" />
                              ) : (
                                <span className="dag-dot" />
                              )}
                            </span>
                            <span className="dag-node-text">{t.name[lang]}</span>
                            <span className="dag-node-status-label">
                              {isTaskRunning && (isZh ? "运行中" : "Running")}
                              {isTaskSuccess && (isZh ? "成功" : "Success")}
                              {isTaskPending && (isZh ? "等待" : "Pending")}
                            </span>
                          </div>
                          {idx < tasks.length - 1 && (
                            <div className={`dag-edge ${isTaskSuccess ? "active" : ""}`} />
                          )}
                        </div>
                      );
                    })}
                  </div>
                </div>

                {/* Console Log Area */}
                <div className="simulator-terminal">
                  <div className="terminal-scroll-area">
                    {terminalLogs.map((log, i) => (
                      <div key={i} className="terminal-line">
                        {log}
                      </div>
                    ))}
                    {simStep === "planning" && <div className="terminal-cursor-blink" />}
                  </div>
                </div>

                {/* Approve Gate */}
                {simStep === "completed" && (
                  <div className="simulator-approve-gate">
                    <div className="flex items-center justify-between gap-4">
                      <div>
                        <div className="font-semibold text-sm">
                          {isZh
                            ? "需要人工审核 (Human-in-the-Loop)"
                            : "Awaiting Human-in-the-Loop Review"}
                        </div>
                        <p className="text-xs text-muted-foreground mt-0.5">
                          {isZh
                            ? "所有任务成功运行。在导出最终报告前请求人工最终授权。"
                            : "All tasks executed in sandbox. Confirm results to export final audit artifacts."}
                        </p>
                      </div>
                      <div className="flex gap-2">
                        <button
                          onClick={handleReset}
                          className="home-btn-ghost text-xs px-3 py-1.5"
                        >
                          {isZh ? "否决" : "Reject"}
                        </button>
                        <button
                          onClick={handleReset}
                          className="home-btn-primary text-xs px-3 py-1.5"
                        >
                          {isZh ? "通过并导出" : "Approve & Export"}
                        </button>
                      </div>
                    </div>
                  </div>
                )}
              </div>
            )}
          </div>
        </div>
      </div>
    </section>
  );
}

const delay = (ms: number) => new Promise((res) => setTimeout(res, ms));

// ─────────────────────────────────────────────────────────────────────────────
// AGENT EXPLORER (INTERACTIVE AGENT CATALOG)
// ─────────────────────────────────────────────────────────────────────────────

interface AgentData {
  id: string;
  name: { en: string; zh: string };
  role: { en: string; zh: string };
  instruction: { en: string; zh: string };
  tools: { icon: any; name: string }[];
  review: { en: string; zh: string };
}

const AGENTS_LIST: AgentData[] = [
  {
    id: "finance",
    name: { en: "Expense Auditor", zh: "财务报销审计员" },
    role: {
      en: "Auto-audits receipts and matches policy rules",
      zh: "自动发票解析与合规政策自动审核",
    },
    instruction: {
      en: "Read receipt OCR data. If food cost > $100 or luxury spending detected, flag and block auto-reimbursement. Require manual review.",
      zh: "读取发票 OCR 数据。如餐饮费用单次超过 $100 或存在奢侈服务消费，将其标记为不合规，阻断自动报销，并转交人工审核。",
    },
    tools: [
      { icon: Eye, name: "ocr:parse" },
      { icon: FileText, name: "policy_checker" },
      { icon: Plug, name: "erp_ledger:write" },
    ],
    review: {
      en: "Required for violations & purchases > $500",
      zh: "当检测到政策冲突或单笔超过 $500 时必经人工授权",
    },
  },
  {
    id: "hr",
    name: { en: "Referral Recruiter", zh: "内推招聘推荐官" },
    role: {
      en: "Filters resumes and syncs schedule with interviewers",
      zh: "智能匹配求职简历并协调面试日程",
    },
    instruction: {
      en: "Screen incoming resumes. Matches profile against hiring requirements. If overlap score > 80%, notify referee via Feishu and generate Google Calendar invite.",
      zh: "筛选内推简历。提取技能和经历并与岗位要求进行比对，若匹配度高于 80%，在飞书通知推荐人，并向面试官发送谷歌日程邀请。",
    },
    tools: [
      { icon: FileText, name: "resume_parser" },
      { icon: MessageCircle, name: "feishu:message" },
      { icon: CalendarClock, name: "gcal:create_event" },
    ],
    review: {
      en: "Required before sending interview invites",
      zh: "发送正式面试通知前需招聘主管最终确认",
    },
  },
  {
    id: "research",
    name: { en: "Reading Companion", zh: "Recally 研究助手" },
    role: {
      en: "Monitors RSS feeds and summarizes key web articles",
      zh: "RSS 订阅监控与精细内容摘要",
    },
    instruction: {
      en: "Monitor target RSS streams hourly. Extract new article links, download full HTML/PDF, run summary agent, and format clean daily digests.",
      zh: "每小时监控指定的 RSS 订阅源。自动抓取新文章链接，下载完整网页及 PDF，运行总结提取核心要点，整理为每日摘要推送。",
    },
    tools: [
      { icon: Rss, name: "rss:fetch" },
      { icon: Search, name: "web:fetch" },
      { icon: Brain, name: "digest_generator" },
    ],
    review: {
      en: "Review disabled (Fully automated digest routing)",
      zh: "免审直接推送 (每日阅读报告全自动投递)",
    },
  },
];

function AgentExplorerSection({ lang }: { lang: "en" | "zh" }) {
  const isZh = lang === "zh";
  const [activeId, setActiveId] = useState("finance");
  const activeAgent = AGENTS_LIST.find((a) => a.id === activeId) || AGENTS_LIST[0];

  return (
    <section className="home-systems reveal-element">
      <div className="home-shell">
        <div className="home-caps-header">
          <h2 className="home-section-title">
            {isZh ? "创建并共享你的 AI 同事" : "Create and share expert agents"}
          </h2>
          <p className="home-section-sub">
            {isZh
              ? "业务部门（HR、财务、研究等）可以自主组装 Agent，挂载工具与政策。一旦创建，全组织成员即可直接通过对话调用，无需配置，零学习成本。"
              : "A domain owner sets up an agent with its instructions, tools, and safety rules. Once it exists, anyone on the team can use it by chatting — no setup, nothing new to learn."}
          </p>
        </div>

        <div className="agent-explorer-layout">
          {/* Tabs */}
          <div className="agent-tabs">
            {AGENTS_LIST.map((agent) => (
              <button
                key={agent.id}
                onClick={() => setActiveId(agent.id)}
                className={`agent-tab-item ${activeId === agent.id ? "active" : ""}`}
              >
                <div className="text-left">
                  <div className="agent-tab-name">{agent.name[lang]}</div>
                  <div className="agent-tab-role text-xs text-muted-foreground mt-0.5 line-clamp-1">
                    {agent.role[lang]}
                  </div>
                </div>
                <ArrowRight className="size-4 opacity-0 transition-opacity agent-tab-arrow" />
              </button>
            ))}
          </div>

          {/* Details Panel */}
          <div key={activeAgent.id} className="agent-panel">
            <div className="agent-panel-header">
              <h3 className="agent-panel-title">{activeAgent.name[lang]}</h3>
              <p className="text-xs text-muted-foreground mt-0.5">{activeAgent.role[lang]}</p>
            </div>

            <div className="agent-panel-body">
              {/* Prompt Instruction */}
              <div className="agent-field">
                <span className="agent-field-label">
                  {isZh ? "系统指令 (Instructions)" : "Instructions Prompt"}
                </span>
                <div className="agent-prompt-box">
                  <code>{activeAgent.instruction[lang]}</code>
                </div>
              </div>

              {/* Tools attached */}
              <div className="agent-field">
                <span className="agent-field-label">
                  {isZh ? "挂载工具 (Tools Attached)" : "Tools and Integrations"}
                </span>
                <div className="agent-tools-row">
                  {activeAgent.tools.map((t, idx) => {
                    const ToolIcon = t.icon;
                    return (
                      <div key={idx} className="agent-tool-chip">
                        <ToolIcon className="size-3.5 text-primary" />
                        <span>{t.name}</span>
                      </div>
                    );
                  })}
                </div>
              </div>

              {/* Review boundary */}
              <div className="agent-field border-t border-border pt-4 mt-4">
                <div className="flex items-center gap-2 text-xs">
                  <Shield className="size-4 text-primary" />
                  <span className="font-semibold text-foreground">
                    {isZh ? "人工审核边界：" : "Human Review Boundary:"}
                  </span>
                  <span className="text-muted-foreground">{activeAgent.review[lang]}</span>
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
// PIPELINE SECTION
// ─────────────────────────────────────────────────────────────────────────────

interface PipelineStep {
  num: string;
  title: { en: string; zh: string };
  desc: { en: string; zh: string };
  icon: any;
}

const PIPELINE_STEPS: PipelineStep[] = [
  {
    num: "01",
    title: { en: "Goal Input", zh: "目标接收" },
    desc: {
      en: "Provide a high-level outcome in plain text via Web UI, Slack, or WeChat.",
      zh: "通过 Web UI、终端、Telegram、飞书或微信，直接用自然语言描述你需要完成的复杂任务目标。",
    },
    icon: MessageCircle,
  },
  {
    num: "02",
    title: { en: "Task DAG Generation", zh: "生成任务 DAG 拓扑图" },
    desc: {
      en: "Stella breaks down the goal into independent tasks, sorting dependencies and criteria.",
      zh: "Stella 自动将目标拆解为拓扑依赖的任务 DAG。理清任务前后置关系，并明确每步验收标准。",
    },
    icon: ListTodo,
  },
  {
    num: "03",
    title: { en: "Sandboxed Execution", zh: "安全沙箱环境运行" },
    desc: {
      en: "Tasks execute concurrently inside secure local sandbox containers utilizing API keys and tools safely.",
      zh: "所有工具调用、网络访问和脚本执行均在安全的隔离沙箱容器中并发运行，防范密钥泄露与环境破坏。",
    },
    icon: Shield,
  },
  {
    num: "04",
    title: { en: "Human in the Loop", zh: "人工介入确认" },
    desc: {
      en: "Results wait for user verification and sign-off before exporting or committing data changes.",
      zh: "高危写操作或违反预设合规边界的执行结果将阻断，等待您的最终批准方可提交和导出报告。",
    },
    icon: CheckCircle2,
  },
];

function PipelineSection({ lang }: { lang: "en" | "zh" }) {
  const isZh = lang === "zh";

  return (
    <section className="home-pillars reveal-element">
      <div className="home-shell">
        <h2 className="home-pillars-heading">
          {isZh ? "Stella 执行管道" : "The Stella Execution Pipeline"}
        </h2>
        <div className="pipeline-steps-grid">
          {PIPELINE_STEPS.map((step) => {
            const StepIcon = step.icon;
            return (
              <div key={step.num} className="pipeline-step-card">
                <div className="pipeline-step-header">
                  <span className="pipeline-step-num">{step.num}</span>
                  <StepIcon className="size-5 text-primary" />
                </div>
                <h3 className="pipeline-step-title">{step.title[lang]}</h3>
                <p className="pipeline-step-desc">{step.desc[lang]}</p>
              </div>
            );
          })}
        </div>
      </div>
    </section>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// RECALLY SECTION (READER & SUMMARY PREVIEW)
// ─────────────────────────────────────────────────────────────────────────────

function RecallySection({ lang }: { lang: "en" | "zh" }) {
  const isZh = lang === "zh";

  return (
    <section className="home-caps reveal-element">
      <div className="home-shell">
        <div className="home-caps-header">
          <div className="flex items-center gap-2 mb-2">
            <span className="home-eyebrow">Recally</span>
          </div>
          <h2 className="home-section-title">
            {isZh ? "用系统性方案管理您的阅读与知识" : "Build a systematic workspace for knowledge"}
          </h2>
          <p className="home-section-sub">
            {isZh
              ? "Stella 内置了 Recally 功能，让您可以一键保存网页与 PDF，订阅 RSS 播客，并支持在阅读文章的同时与 AI 进行深度问答交互。"
              : "Never lose track of research. Stella ships with integrated Recally support: save web pages, download PDFs, subscribe to RSS feeds, parse daily digests, and chat with articles side-by-side."}
          </p>
        </div>

        {/* Recally Reader Grid Mockup */}
        <div className="recally-mockup-container">
          <div className="recally-header">
            <div className="flex items-center gap-2">
              <span className="recally-indicator" />
              <span className="font-mono text-xs text-foreground font-semibold">
                Recally Reader Workspace
              </span>
            </div>
            <div className="recally-url-bar">
              https://recally.stella.localhost/articles/agent-workflows
            </div>
          </div>

          <div className="recally-layout">
            {/* Sidebar list */}
            <div className="recally-sidebar">
              <div className="recally-sidebar-header">{isZh ? "阅读列表" : "Reading Queue"}</div>
              <div className="recally-sidebar-list">
                <div className="recally-list-item active">
                  <div className="font-semibold text-xs truncate">
                    {isZh ? "理解 Agent 任务工作流 (DAG)" : "Understanding Agent DAG Workflows"}
                  </div>
                  <div className="text-xs text-muted-foreground mt-0.5">
                    stella.sh · {isZh ? "今天" : "Today"}
                  </div>
                </div>
                <div className="recally-list-item">
                  <div className="font-semibold text-xs truncate">
                    {isZh ? "自部署私有化大模型实践" : "Self-hosting Local LLMs Strategy"}
                  </div>
                  <div className="text-xs text-muted-foreground mt-0.5">
                    ollama.ai · {isZh ? "昨天" : "Yesterday"}
                  </div>
                </div>
                <div className="recally-list-item">
                  <div className="font-semibold text-xs truncate">
                    {isZh
                      ? "使用 Docker 与 Sandbox 保护密钥"
                      : "Sandboxing Secrets with Containers"}
                  </div>
                  <div className="text-xs text-muted-foreground mt-0.5">
                    docker.com · {isZh ? "3天前" : "3d ago"}
                  </div>
                </div>
              </div>
            </div>

            {/* Reader View */}
            <div className="recally-reader">
              <div className="recally-article-container">
                <h1 className="recally-article-title">
                  {isZh
                    ? "为什么有向无环图 (DAG) 是 Agent 复杂任务的基石？"
                    : "Why Directed Acyclic Graphs (DAG) Power Complex Agents"}
                </h1>
                <div className="recally-article-meta">
                  <span>Author: Stella Core</span> · <span>Reading Time: 5 min</span>
                </div>
                <div className="recally-article-content">
                  <p>
                    {isZh
                      ? "在传统单轮对话中，AI 遇到长任务链极易迷失。而通过将 Goal 拆解为多个有依赖关系的 Task 节点，Agent 能够独立并稳健地执行长距离推理。"
                      : "In traditional single-turn prompt execution, LLMs struggle to maintain focus over long pipelines. By decomposing a complex Goal into multiple node dependencies (a DAG), the Agent establishes structured safety borders."}
                  </p>
                  <p className="recally-highlight">
                    {isZh
                      ? "DAG 允许 Stella 并发执行没有前置依赖的任务（例如同时爬取多个网页），同时在后续节点中整合汇总，保障了大规模数据流处理的高效性。"
                      : "The DAG enables Stella to execute independent tasks in parallel (e.g. fetching concurrent web urls), before feeding consolidated outcomes into subsequent processing layers."}
                  </p>
                  <p>
                    {isZh
                      ? "最关键的是，这种可视化的任务网络使每一次执行过程都能够被人类用户审计、调整和打断，达成了完美的人机协同。"
                      : "Most importantly, this topological structure makes the execution plan fully inspectable and interruptible by human operators before final ledger commits occur."}
                  </p>
                </div>
              </div>
            </div>

            {/* In-context Chat Side */}
            <div className="recally-chat">
              <div className="recally-chat-header">
                <MessageCircle className="size-3.5 text-primary" />
                <span>{isZh ? "AI 边读边问" : "Chat with Article"}</span>
              </div>
              <div className="recally-chat-messages">
                <div className="chat-bubble user">
                  {isZh ? "一句话总结这篇文章？" : "Give me a one-sentence summary."}
                </div>
                <div className="chat-bubble bot">
                  {isZh
                    ? "文章指出：使用有向无环图 (DAG) 拆解复杂任务，是确保 AI 能够安全、并发运行且易于人类审计的黄金法则。"
                    : "The article explains that leveraging DAGs to plan tasks guarantees LLM agents can run concurrently and remain easily inspectable by human users."}
                </div>
              </div>
              <div className="recally-chat-input-wrapper">
                <input
                  type="text"
                  readOnly
                  placeholder={isZh ? "输入您的问题..." : "Ask a follow up..."}
                  className="recally-chat-input"
                />
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// FOOTER CTA WITH TABBED INSTALL switch
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
    code: "docker run -d --name stella -p 8080:8080 \\ \n  -v ~/.stella:/root/.stella cherryhq/stella:latest",
  },
  {
    id: "go",
    name: "Go Install",
    code: "go install github.com/CherryHQ/stella/cmd/stella@latest\ngo install github.com/CherryHQ/stella/cmd/stellad@latest\nstellad server",
  },
];

function FooterCTA({ lang }: { lang: "en" | "zh" }) {
  const isZh = lang === "zh";
  const [activeTab, setActiveTab] = useState("brew");
  const [copied, setCopied] = useState(false);

  const selectedCode = INSTALL_TABS.find((t) => t.id === activeTab)?.code || "";

  const handleCopy = () => {
    void navigator.clipboard.writeText(selectedCode);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };

  return (
    <section className="home-cta reveal-element">
      <div className="home-shell home-cta-layout">
        <div className="home-cta-copy">
          <h2 className="home-section-title">
            {isZh ? "即刻在本地开始使用" : "Deploy Stella locally in seconds"}
          </h2>
          <p className="home-section-sub">
            {isZh
              ? "Stella 提供多平台运行方式。无论是本地调试单机运行，还是通过 Docker 在云端进行多用户组织部署，均可几秒完成启动。"
              : "Stella runs directly on your machine. Start with Homebrew locally, or launch it with Docker for a multi-tenant corporate environment."}
          </p>
        </div>

        {/* Tabbed Installation Box */}
        <div className="install-tabs-card">
          <div className="install-tabs-header">
            <div className="flex gap-1">
              {INSTALL_TABS.map((t) => (
                <button
                  key={t.id}
                  onClick={() => setActiveTab(t.id)}
                  className={`install-tab-btn ${activeTab === t.id ? "active" : ""}`}
                >
                  {t.name}
                </button>
              ))}
            </div>

            <button onClick={handleCopy} className="install-copy-btn">
              {copied ? (
                <>
                  <Check className="size-3.5 text-chart-3" />
                  <span className="text-chart-3">{isZh ? "已复制" : "Copied"}</span>
                </>
              ) : (
                <>
                  <Copy className="size-3.5" />
                  <span>{isZh ? "复制" : "Copy"}</span>
                </>
              )}
            </button>
          </div>

          <div key={activeTab} className="install-code-body">
            <div className="flex items-start gap-3">
              <Terminal className="size-4 text-primary mt-1 flex-shrink-0" />
              <pre className="install-pre-code">{selectedCode}</pre>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
