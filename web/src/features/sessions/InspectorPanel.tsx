import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { Brain, ChevronDown, FileText, Sparkles, Wrench } from "lucide-react";
import type { ComponentsTask } from "@/lib/api-client/types.gen";
import {
  getSessionMessages,
  getSessionSystemPrompt,
  listAgentSkills,
  listTools,
} from "@/lib/api-client";
import {
  agentMemoriesOptions,
  agentSchedulerJobsOptions,
  agentSkillsOptions,
} from "@/lib/queries/agents";
import { formatTime } from "@/lib/time";
import { fetchAllTasks } from "@/lib/paginated";
import type { Message, Session, Skill, Tool, Workspace } from "@/lib/types";
import { cn } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { WorkspacePanel } from "./WorkspacePanel";

interface Props {
  agentID: string;
  sessionID: string;
  session: Session | null;
  workspace: Workspace | null;
  workspaceLoading: boolean;
  onReloadWorkspace: (sid: string, path?: string) => Promise<void>;
  projectDir?: string;
}

type InspectorTab = "workspace" | "work" | "context";
type WorkFilter = "needs" | "running" | "scheduled";
type ContextSection = "session" | "tools" | "prompt" | "skills" | "memory";

const TAB_STORAGE_PREFIX = "stella:inspector-tab:";

export function InspectorPanel({
  agentID,
  sessionID,
  session,
  workspace,
  workspaceLoading,
  onReloadWorkspace,
  projectDir,
}: Props) {
  const navigate = useNavigate();
  const [tab, setTab] = useState<InspectorTab>("workspace");
  const [workFilter, setWorkFilter] = useState<WorkFilter>("needs");
  const { t } = useI18n();

  useEffect(() => {
    const stored = localStorage.getItem(`${TAB_STORAGE_PREFIX}${agentID}`) as InspectorTab | null;
    setTab(stored === "work" || stored === "context" ? stored : "workspace");
  }, [agentID]);

  const selectTab = useCallback(
    (next: InspectorTab) => {
      setTab(next);
      localStorage.setItem(`${TAB_STORAGE_PREFIX}${agentID}`, next);
    },
    [agentID],
  );

  return (
    <div className="flex h-full w-full flex-col overflow-hidden bg-card">
      <div className="flex h-12 shrink-0 items-end border-b border-border px-4">
        <div className="flex w-full gap-4 text-xs">
          <InspectorTabButton active={tab === "workspace"} onClick={() => selectTab("workspace")}>
            {t("sessions.inspector.workspace")}
          </InspectorTabButton>
          <InspectorTabButton active={tab === "work"} onClick={() => selectTab("work")}>
            {t("sessions.inspector.activity")}
          </InspectorTabButton>
          <InspectorTabButton active={tab === "context"} onClick={() => selectTab("context")}>
            {t("sessions.inspector.context")}
          </InspectorTabButton>
        </div>
      </div>

      {tab === "workspace" && (
        <WorkspacePanel
          agentID={agentID}
          sessionID={sessionID}
          workspace={workspace}
          workspaceLoading={workspaceLoading}
          onReload={onReloadWorkspace}
          projectDir={projectDir}
        />
      )}

      {tab === "work" && (
        <WorkPanel
          agentID={agentID}
          filter={workFilter}
          onFilterChange={setWorkFilter}
          onOpenWork={() =>
            void navigate({ to: "/agents/$agentId/automations", params: { agentId: agentID } })
          }
        />
      )}

      {tab === "context" && (
        <ContextPanel
          agentID={agentID}
          session={session}
          onOpenSkills={() =>
            void navigate({ to: "/agents/$agentId/skills", params: { agentId: agentID } })
          }
          onOpenMemory={() =>
            void navigate({ to: "/agents/$agentId/memories", params: { agentId: agentID } })
          }
        />
      )}
    </div>
  );
}

function InspectorTabButton({
  active,
  children,
  onClick,
}: {
  active: boolean;
  children: React.ReactNode;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "pb-2 px-1 font-medium transition-colors border-b-2 outline-none cursor-pointer text-xs",
        active
          ? "border-primary text-foreground font-semibold"
          : "border-transparent text-muted-foreground hover:text-foreground",
      )}
    >
      {children}
    </button>
  );
}

function WorkPanel({
  agentID,
  filter,
  onFilterChange,
  onOpenWork,
}: {
  agentID: string;
  filter: WorkFilter;
  onFilterChange: (filter: WorkFilter) => void;
  onOpenWork: () => void;
}) {
  const { t } = useI18n();
  const { tasks, jobs, needsTasks, runningTasks, scheduledJobs, loading } = useWorkData(agentID);
  const filterCounts = {
    needs: needsTasks.length,
    running: runningTasks.length,
    scheduled: scheduledJobs.length,
  };
  const items = useMemo(() => {
    if (filter === "needs") return needsTasks.map((task) => workTaskItem(task));
    if (filter === "running") return runningTasks.map((task) => workTaskItem(task));
    return scheduledJobs.map((job) => ({
      id: job.id,
      title: job.name,
      detail: job.description || job.cron || job.every || job.at || "Scheduled automation",
      tone: "blue" as const,
      meta: job.last_run_at ? `last ${formatShortDate(job.last_run_at)}` : "scheduled",
    }));
  }, [filter, needsTasks, runningTasks, scheduledJobs]);

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
      <div className="shrink-0 border-b border-border/70 px-3 py-2">
        <div className="mb-2 flex items-center justify-between gap-2">
          <span className="text-xs font-semibold">{t("sessions.inspector.activity")}</span>
          <Button variant="ghost" size="xs" onClick={onOpenWork} className="text-muted-foreground">
            Open Tasks
          </Button>
        </div>
        <div className="grid grid-cols-3 gap-1">
          {(["needs", "running", "scheduled"] as WorkFilter[]).map((key) => (
            <button
              key={key}
              type="button"
              onClick={() => onFilterChange(key)}
              className={cn(
                "rounded-lg border border-border/60 px-2 py-1.5 text-left transition-colors",
                filter === key
                  ? "bg-muted text-foreground"
                  : "bg-background/35 text-muted-foreground hover:bg-muted/45 hover:text-foreground",
              )}
            >
              <span className="block text-sm font-semibold leading-none text-foreground">
                {filterCounts[key]}
              </span>
              <span className="mt-1 block truncate text-[10px]">
                {key === "needs"
                  ? t("sessions.inspector.needsYou")
                  : key === "running"
                    ? t("sessions.inspector.running")
                    : t("sessions.inspector.scheduled")}
              </span>
            </button>
          ))}
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto p-2">
        {loading ? (
          <div className="flex items-center justify-center py-10">
            <div className="size-4 animate-spin rounded-full border-2 border-muted-foreground/30 border-t-muted-foreground" />
          </div>
        ) : items.length === 0 ? (
          <p className="px-2 py-8 text-center text-xs text-muted-foreground">
            {filter === "needs"
              ? t("sessions.inspector.nothingNeeds")
              : filter === "running"
                ? t("sessions.inspector.noActiveRuns")
                : t("sessions.inspector.noScheduled")}
          </p>
        ) : (
          <div className="grid gap-1">
            {items.map((item) => (
              <div
                key={item.id}
                className="grid grid-cols-[8px_minmax(0,1fr)_auto] gap-2 rounded-xl px-2 py-2 text-left transition-colors hover:bg-foreground/[0.045]"
              >
                <span
                  className={cn(
                    "mt-1.5 size-2 rounded-full",
                    item.tone === "yellow" && "bg-amber-500",
                    item.tone === "green" && "bg-emerald-500",
                    item.tone === "blue" && "bg-blue-500",
                  )}
                />
                <span className="min-w-0">
                  <span className="block truncate text-xs font-medium">{item.title}</span>
                  <span className="mt-0.5 block truncate text-[11px] text-muted-foreground">
                    {item.detail}
                  </span>
                </span>
                <span className="text-[10px] text-muted-foreground">{item.meta}</span>
              </div>
            ))}
          </div>
        )}
      </div>

      <div className="shrink-0 border-t border-border/70 px-3 py-2 text-[10px] text-muted-foreground">
        {tasks.length} tasks · {jobs.length} schedules
      </div>
    </div>
  );
}

function ContextPanel({
  agentID,
  session,
  onOpenSkills,
  onOpenMemory,
}: {
  agentID: string;
  session: Session | null;
  onOpenSkills: () => void;
  onOpenMemory: () => void;
}) {
  const { t } = useI18n();
  const [openSections, setOpenSections] = useState<Record<ContextSection, boolean>>({
    session: true,
    tools: false,
    prompt: false,
    skills: false,
    memory: false,
  });
  const [messages, setMessages] = useState<Message[]>([]);
  const [messagesLoading, setMessagesLoading] = useState(false);
  const [systemPrompt, setSystemPrompt] = useState("");
  const [promptLoading, setPromptLoading] = useState(false);
  const [tools, setTools] = useState<Tool[]>([]);
  const [toolsLoading, setToolsLoading] = useState(false);
  const [sessionSkills, setSessionSkills] = useState<Skill[]>([]);
  const [sessionSkillsLoading, setSessionSkillsLoading] = useState(false);
  const { data: skills = [] } = useQuery(agentSkillsOptions(agentID));
  const { data: memories = [] } = useQuery(agentMemoriesOptions(agentID));

  useEffect(() => {
    if (!session) {
      setMessages([]);
      setSystemPrompt("");
      setSessionSkills([]);
      return;
    }

    let cancelled = false;
    setMessagesLoading(true);
    getSessionMessages({
      path: { agentId: session.agent_id, sessionId: session.id },
      query: { limit: 1000 },
      throwOnError: true,
    })
      .then(({ data }) => {
        if (!cancelled) setMessages((data?.messages as unknown as Message[] | undefined) ?? []);
      })
      .catch((e) => {
        console.error(e);
        if (!cancelled) setMessages([]);
      })
      .finally(() => {
        if (!cancelled) setMessagesLoading(false);
      });

    setPromptLoading(true);
    getSessionSystemPrompt({
      path: { agentId: session.agent_id, sessionId: session.id },
      throwOnError: true,
    })
      .then(({ data }) => {
        if (!cancelled) {
          setSystemPrompt((data as { system_prompt: string }).system_prompt);
        }
      })
      .catch((e) => {
        console.error(e);
        if (!cancelled) setSystemPrompt("");
      })
      .finally(() => {
        if (!cancelled) setPromptLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [session?.agent_id, session?.id]);

  const loadTools = useCallback(async () => {
    if (tools.length > 0 || toolsLoading) return;
    setToolsLoading(true);
    try {
      const { data } = await listTools({ throwOnError: true });
      setTools((data?.tools as Tool[]) ?? []);
    } finally {
      setToolsLoading(false);
    }
  }, [tools.length, toolsLoading]);

  const loadSessionSkills = useCallback(async () => {
    if (!session || sessionSkills.length > 0 || sessionSkillsLoading) return;
    setSessionSkillsLoading(true);
    try {
      const { data } = await listAgentSkills({
        path: { id: session.agent_id },
        query: { session_id: session.id },
        throwOnError: true,
      });
      setSessionSkills((data?.skills ?? []) as Skill[]);
    } finally {
      setSessionSkillsLoading(false);
    }
  }, [session, sessionSkills.length, sessionSkillsLoading]);

  const toggleSection = useCallback(
    (next: ContextSection) => {
      if (next === "tools") loadTools().catch(console.error);
      if (next === "skills") loadSessionSkills().catch(console.error);
      setOpenSections((prev) => ({ ...prev, [next]: !prev[next] }));
    },
    [loadSessionSkills, loadTools],
  );

  const sessionTotalTokens = messages.reduce((sum, message) => sum + (message.token_count ?? 0), 0);

  return (
    <div className="min-h-0 flex-1 overflow-y-auto p-3">
      <div className="grid gap-3">
        <ContextSummary
          messages={messages}
          messagesLoading={messagesLoading}
          tokens={sessionTotalTokens}
          tools={tools.length}
          skills={skills.length}
        />

        <ContextSectionCard
          title={t("sessions.inspector.session")}
          detail={session ? `${channelLabel(session.channel) || "chat"} · ${session.kind}` : ""}
          icon={<FileText className="size-4" />}
          open={openSections.session}
          onToggle={() => toggleSection("session")}
        >
          {session && (
            <SessionContextCard
              session={session}
              messages={messages}
              messagesLoading={messagesLoading}
            />
          )}
        </ContextSectionCard>

        <ContextSectionCard
          title={t("sessions.inspector.tools")}
          detail={
            tools.length > 0
              ? t("sessions.inspector.available", { count: tools.length })
              : t("sessions.inspector.runtimeToolCatalog")
          }
          icon={<Wrench className="size-4" />}
          open={openSections.tools}
          onToggle={() => toggleSection("tools")}
        >
          <ToolsContext tools={tools} loading={toolsLoading} />
        </ContextSectionCard>

        <ContextSectionCard
          title={t("sessions.inspector.prompt")}
          detail={
            promptLoading
              ? t("sessions.inspector.loading")
              : systemPrompt
                ? `~${Math.round(systemPrompt.length / 4)} tokens`
                : t("sessions.inspector.systemPrompt")
          }
          icon={<FileText className="size-4" />}
          open={openSections.prompt}
          onToggle={() => toggleSection("prompt")}
        >
          <PromptContext systemPrompt={systemPrompt} loading={promptLoading} />
        </ContextSectionCard>

        <ContextSectionCard
          title={t("sessions.inspector.skills")}
          detail={
            sessionSkills.length > 0
              ? t("sessions.inspector.enabledCount", { count: sessionSkills.length })
              : t("sessions.inspector.enabledForChat")
          }
          icon={<Sparkles className="size-4" />}
          open={openSections.skills}
          onToggle={() => toggleSection("skills")}
          actionLabel={t("sessions.inspector.manage")}
          onAction={onOpenSkills}
        >
          <SkillsContext skills={sessionSkills} loading={sessionSkillsLoading} />
        </ContextSectionCard>

        <ContextSectionCard
          title={t("sessions.inspector.memory")}
          detail={
            memories.length > 0
              ? t("sessions.inspector.memoriesCount", { count: memories.length })
              : t("sessions.inspector.soulAndProfile")
          }
          icon={<Brain className="size-4" />}
          open={openSections.memory}
          onToggle={() => toggleSection("memory")}
          actionLabel={t("sessions.inspector.open")}
          onAction={onOpenMemory}
        >
          <MemoryContext memories={memories} />
        </ContextSectionCard>
      </div>
    </div>
  );
}

function ContextSummary({
  messages,
  messagesLoading,
  tokens,
  tools,
  skills,
}: {
  messages: Message[];
  messagesLoading: boolean;
  tokens: number;
  tools: number;
  skills: number;
}) {
  const { t } = useI18n();
  return (
    <section className="rounded-xl border border-border bg-card p-4">
      <div className="mb-3 text-[10px] font-semibold text-muted-foreground">
        {t("sessions.inspector.runtimeContext")}
      </div>
      <div className="grid grid-cols-4 gap-1.5">
        <ContextMetric
          label={t("sessions.inspector.msgs")}
          value={messagesLoading ? "..." : messages.length}
        />
        <ContextMetric
          label={t("sessions.inspector.tokens")}
          value={
            messagesLoading
              ? "..."
              : typeof tokens === "number"
                ? tokens.toLocaleString()
                : tokens || "-"
          }
        />
        <ContextMetric label={t("sessions.inspector.tools")} value={tools || "-"} />
        <ContextMetric label={t("sessions.inspector.skills")} value={skills || "-"} />
      </div>
    </section>
  );
}

function ContextMetric({ label, value }: { label: string; value: string | number }) {
  return (
    <div className="rounded-lg bg-muted/40 border border-border/10 px-2 py-2 text-center">
      <div className="truncate text-sm font-semibold font-mono text-foreground">{value}</div>
      <div className="mt-0.5 truncate text-[10px] text-muted-foreground">{label}</div>
    </div>
  );
}

function ContextSectionCard({
  title,
  detail,
  icon,
  open,
  children,
  onToggle,
  actionLabel,
  onAction,
}: {
  title: string;
  detail: string;
  icon: React.ReactNode;
  open: boolean;
  children: React.ReactNode;
  onToggle: () => void;
  actionLabel?: string;
  onAction?: () => void;
}) {
  return (
    <section className="overflow-hidden rounded-xl border border-border bg-card">
      <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center">
        <button
          type="button"
          onClick={onToggle}
          className="grid min-w-0 grid-cols-[2rem_minmax(0,1fr)_1rem] items-center gap-2.5 px-4 py-3 text-left cursor-pointer"
        >
          <span className="grid size-8 place-items-center rounded-lg bg-muted border border-border/10 text-muted-foreground shrink-0">
            {icon}
          </span>
          <span className="min-w-0">
            <span className="block truncate text-sm font-semibold text-foreground">{title}</span>
            <span className="mt-0.5 block truncate text-[11px] text-muted-foreground leading-none">
              {detail}
            </span>
          </span>
          <ChevronDown
            className={cn(
              "size-3.5 text-muted-foreground transition-transform duration-200",
              open && "rotate-180",
            )}
          />
        </button>
        {actionLabel && onAction && (
          <button
            type="button"
            onClick={(e) => {
              e.stopPropagation();
              onAction();
            }}
            className="mr-3 rounded-md px-2.5 py-1.5 text-[10px] font-semibold text-primary hover:bg-primary/10 cursor-pointer"
          >
            {actionLabel}
          </button>
        )}
      </div>
      {open && <div className="border-t border-border p-4">{children}</div>}
    </section>
  );
}

function SessionContextCard({
  session,
  messages,
  messagesLoading,
}: {
  session: Session;
  messages: Message[];
  messagesLoading: boolean;
}) {
  const { t } = useI18n();
  const copyID = useCallback(() => {
    navigator.clipboard.writeText(session.id).catch(console.error);
  }, [session.id]);
  const sessionTotalTokens = messages.reduce((sum, message) => sum + (message.token_count ?? 0), 0);
  const agentName = (session as Session & { agent_name?: string }).agent_name || session.agent_id;

  return (
    <div className="space-y-3.5">
      <dl className="grid grid-cols-[5.2rem_minmax(0,1fr)] gap-x-3 gap-y-2 text-xs">
        <dt className="text-muted-foreground font-semibold text-[10px]">
          {t("sessions.inspector.channel")}
        </dt>
        <dd className="truncate text-foreground font-medium">
          {channelLabel(session.channel) || "chat"}
        </dd>
        <dt className="text-muted-foreground font-semibold text-[10px]">
          {t("sessions.inspector.agent")}
        </dt>
        <dd className="truncate text-foreground font-medium">{agentName || "unknown"}</dd>
        <dt className="text-muted-foreground font-semibold text-[10px]">
          {t("sessions.inspector.kind")}
        </dt>
        <dd className="truncate capitalize text-foreground font-medium">{session.kind}</dd>
        <dt className="text-muted-foreground font-semibold text-[10px]">
          {t("sessions.inspector.active")}
        </dt>
        <dd className="truncate text-foreground font-medium">{formatTime(session.last_active)}</dd>
        <dt className="text-muted-foreground font-semibold text-[10px]">
          {t("sessions.inspector.messages")}
        </dt>
        <dd className="truncate text-foreground font-medium">
          {messagesLoading ? "Loading..." : messages.length.toLocaleString()}
        </dd>
        <dt className="text-muted-foreground font-semibold text-[10px]">
          {t("sessions.inspector.tokens")}
        </dt>
        <dd className="truncate text-foreground font-medium">
          {messagesLoading
            ? "Loading..."
            : typeof sessionTotalTokens === "number"
              ? sessionTotalTokens.toLocaleString()
              : sessionTotalTokens || "-"}
        </dd>
        <dt className="text-muted-foreground font-semibold text-[10px]">
          {t("sessions.inspector.sessionId")}
        </dt>
        <dd className="truncate font-mono text-[10px] text-muted-foreground">{session.id}</dd>
      </dl>
      <div className="flex justify-end border-t border-border/10 pt-2.5">
        <button
          type="button"
          onClick={copyID}
          className="rounded-lg px-2.5 py-1.5 text-[10px] font-semibold text-muted-foreground hover:bg-muted hover:text-foreground cursor-pointer border border-border/60 bg-transparent"
        >
          {t("sessions.inspector.copySessionId")}
        </button>
      </div>
    </div>
  );
}

function ToolsContext({ tools, loading }: { tools: Tool[]; loading: boolean }) {
  const { t } = useI18n();
  if (loading) return <ContextEmpty>{t("sessions.inspector.loadingTools")}</ContextEmpty>;
  if (tools.length === 0) return <ContextEmpty>{t("sessions.inspector.noTools")}</ContextEmpty>;

  return (
    <div className="grid gap-2">
      {tools.map((tool) => (
        <ToolContextRow key={tool.name} tool={tool} />
      ))}
    </div>
  );
}

function ToolContextRow({ tool }: { tool: Tool }) {
  const [expanded, setExpanded] = useState(false);

  return (
    <div className="border-b border-border/40 last:border-b-0 py-2 first:pt-0 last:pb-0">
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        className="grid w-full grid-cols-[1rem_minmax(0,1fr)] gap-2 text-left hover:bg-muted/20 p-1.5 rounded-lg cursor-pointer"
      >
        <span className="pt-0.5 text-[10px] text-muted-foreground">{expanded ? "▼" : "▶"}</span>
        <span className="min-w-0">
          <span className="flex min-w-0 items-center gap-2">
            <span className="truncate font-mono text-xs font-semibold text-foreground">
              {tool.name}
            </span>
            <span className="shrink-0 rounded-full border border-border px-1.5 py-0.5 text-[9px] text-muted-foreground">
              {tool.category}
            </span>
          </span>
          <span className="mt-1 block text-[11px] text-muted-foreground leading-normal">
            {tool.description}
          </span>
        </span>
      </button>
      {expanded && (
        <pre className="mt-2 overflow-x-auto rounded-lg bg-muted/40 p-2 border border-border/15 font-mono text-[10px] leading-relaxed text-muted-foreground">
          {JSON.stringify(tool.input_schema, null, 2)}
        </pre>
      )}
    </div>
  );
}

function PromptContext({ systemPrompt, loading }: { systemPrompt: string; loading: boolean }) {
  const { t } = useI18n();
  const copyPrompt = useCallback(() => {
    navigator.clipboard.writeText(systemPrompt).catch(console.error);
  }, [systemPrompt]);

  if (loading) return <ContextEmpty>{t("sessions.inspector.loadingPrompt")}</ContextEmpty>;

  return (
    <div className="grid gap-2">
      <div className="flex items-center justify-between gap-2 px-1">
        <span className="font-mono text-[10px] text-muted-foreground">
          ~{Math.round(systemPrompt.length / 4)} tokens
        </span>
        <button
          type="button"
          onClick={copyPrompt}
          className="rounded-lg px-2 py-1 font-semibold text-[10px] text-muted-foreground hover:bg-muted hover:text-foreground cursor-pointer border border-border/50"
        >
          {t("sessions.inspector.copy")}
        </button>
      </div>
      <pre className="whitespace-pre-wrap rounded-xl border border-border/60 bg-muted/40 p-3.5 font-mono text-[10px] leading-relaxed text-muted-foreground">
        {systemPrompt || t("sessions.inspector.noPrompt")}
      </pre>
    </div>
  );
}

function SkillsContext({ skills, loading }: { skills: Skill[]; loading: boolean }) {
  const { t } = useI18n();
  if (loading) return <ContextEmpty>{t("sessions.inspector.loadingSkills")}</ContextEmpty>;

  return (
    <div className="grid gap-2">
      {skills.length === 0 ? (
        <ContextEmpty>{t("sessions.inspector.noSkills")}</ContextEmpty>
      ) : (
        skills.map((skill) => <SkillContextRow key={skill.id} skill={skill} />)
      )}
    </div>
  );
}

function MemoryContext({ memories }: { memories: unknown[] }) {
  const { t } = useI18n();
  return (
    <div className="py-1">
      <p className="text-xs text-muted-foreground leading-relaxed">
        {memories.length > 0
          ? t("sessions.inspector.memoriesAvailable", { count: memories.length })
          : t("sessions.inspector.noMemories")}
      </p>
    </div>
  );
}

function SkillContextRow({ skill }: { skill: Skill }) {
  return (
    <div className="border-b border-border/40 last:border-b-0 py-2.5 first:pt-0 last:pb-0">
      <div className="flex min-w-0 items-center gap-2">
        <span className="truncate font-mono text-xs font-semibold text-foreground">
          {skill.name || skill.id}
        </span>
        <span className="shrink-0 rounded-full border border-border px-1.5 py-0.5 text-[9px] text-muted-foreground">
          {skill.scope}
        </span>
        <span
          className={cn(
            "shrink-0 rounded-full px-1.5 py-0.5 text-[9px] font-medium",
            skill.status === "active"
              ? "bg-primary/10 text-primary"
              : "bg-muted text-muted-foreground",
          )}
        >
          {skill.status}
        </span>
      </div>
      {skill.description && (
        <p className="mt-1.5 text-[11px] leading-relaxed text-muted-foreground">
          {skill.description}
        </p>
      )}
      {skill.files && skill.files.length > 0 && (
        <p className="mt-1.5 font-mono text-[10px] text-muted-foreground">
          {skill.files.length} files
        </p>
      )}
    </div>
  );
}

function ContextEmpty({ children }: { children: React.ReactNode }) {
  return <p className="px-2 py-8 text-center text-xs text-muted-foreground">{children}</p>;
}

function useWorkData(agentID: string) {
  const [tasks, setTasks] = useState<ComponentsTask[]>([]);
  const [tasksLoading, setTasksLoading] = useState(true);
  const { data: jobs = [], isLoading: jobsLoading } = useQuery(agentSchedulerJobsOptions(agentID));

  const loadTasks = useCallback(async () => {
    setTasksLoading(true);
    try {
      setTasks(await fetchAllTasks(agentID));
    } catch (e) {
      console.error(e);
      setTasks([]);
    } finally {
      setTasksLoading(false);
    }
  }, [agentID]);

  useEffect(() => {
    void loadTasks();
  }, [loadTasks]);

  const needsTasks = tasks.filter(
    (task) => task.status === "blocked" || task.status === "reviewing",
  );
  const runningTasks = tasks.filter((task) => task.status === "running");
  const scheduledJobs = jobs.filter((job) => job.enabled);

  return {
    tasks,
    jobs,
    needsTasks,
    runningTasks,
    scheduledJobs,
    loading: tasksLoading || jobsLoading,
  };
}

function workTaskItem(task: ComponentsTask) {
  return {
    id: task.id,
    title: task.title,
    detail: task.description || task.status,
    tone: task.status === "running" ? ("green" as const) : ("yellow" as const),
    meta: formatShortDate(task.updated_at),
  };
}

function formatShortDate(value?: string) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

function channelLabel(ch: string | null | undefined): string {
  if (!ch) return "";
  const m = ch.match(/:channel:([^:]+)/);
  return m ? m[1] : ch;
}
