import { useCallback, useEffect, useMemo, useState } from "react";
import { ChevronDown, FileText, Wrench } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { getSessionSystemPrompt } from "@/lib/api-client";
import { Button } from "@/components/ui/button";
import type { Session, Tool, Workspace } from "@/lib/types";
import { agentToolsOptions } from "@/lib/queries/agents";
import { sessionContextItemsOptions } from "@/lib/queries/session-context";
import { cn } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";
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

type InspectorTab = "files" | "context";
type ContextSection = "session" | "tools" | "prompt";

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
  const [tab, setTab] = useState<InspectorTab>("files");
  const { t } = useI18n();

  useEffect(() => {
    const stored = localStorage.getItem(`${TAB_STORAGE_PREFIX}${agentID}`) as InspectorTab | null;
    setTab(stored === "context" ? "context" : "files");
  }, [agentID]);

  const selectTab = useCallback(
    (next: InspectorTab) => {
      setTab(next);
      localStorage.setItem(`${TAB_STORAGE_PREFIX}${agentID}`, next);
    },
    [agentID],
  );

  return (
    <div className="flex h-full w-full min-w-0 flex-col overflow-hidden bg-sidebar">
      <div className="flex h-12 shrink-0 items-end border-b border-border px-4">
        <div className="flex w-full min-w-0 gap-4 overflow-hidden text-xs">
          <InspectorTabButton active={tab === "files"} onClick={() => selectTab("files")}>
            {t("sessions.inspector.workspace")}
          </InspectorTabButton>
          <InspectorTabButton active={tab === "context"} onClick={() => selectTab("context")}>
            {t("sessions.inspector.context")}
          </InspectorTabButton>
        </div>
      </div>

      {tab === "files" && (
        <WorkspacePanel
          agentID={agentID}
          sessionID={sessionID}
          workspace={workspace}
          workspaceLoading={workspaceLoading}
          onReload={onReloadWorkspace}
          projectDir={projectDir}
        />
      )}

      {tab === "context" && <ContextMonitor agentID={agentID} session={session} />}
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
        "min-w-0 truncate pb-2 px-1 font-medium transition-colors border-b-2 outline-none cursor-pointer text-xs",
        active
          ? "border-primary text-foreground font-semibold"
          : "border-transparent text-muted-foreground hover:text-foreground",
      )}
    >
      {children}
    </button>
  );
}

// ── Context Monitor ─────────────────────────────────────────────────────────

function ContextMonitor({ agentID, session }: { agentID: string; session: Session | null }) {
  const { t } = useI18n();
  const [openSections, setOpenSections] = useState<Record<ContextSection, boolean>>({
    session: false,
    tools: false,
    prompt: false,
  });
  const [systemPrompt, setSystemPrompt] = useState("");
  const [promptLoading, setPromptLoading] = useState(false);

  const contextQuery = useQuery(
    sessionContextItemsOptions(session?.agent_id ?? "", session?.id ?? ""),
  );

  useEffect(() => {
    if (!session) {
      setSystemPrompt("");
      return;
    }

    let cancelled = false;
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

  const toolsQuery = useQuery(agentToolsOptions(openSections.tools ? agentID : ""));
  const tools = toolsQuery.data ?? [];

  const toggleSection = useCallback((next: ContextSection) => {
    setOpenSections((prev) => ({ ...prev, [next]: !prev[next] }));
  }, []);

  const promptTokens = useMemo(() => Math.round(systemPrompt.length / 4), [systemPrompt]);

  return (
    <div className="min-h-0 flex-1 overflow-y-auto p-3">
      <div className="grid gap-3">
        <ContextSectionCard
          title={t("sessions.inspector.prompt")}
          detail={
            promptLoading
              ? t("sessions.inspector.loading")
              : systemPrompt
                ? `~${formatTokenCount(promptTokens)} tokens`
                : t("sessions.inspector.systemPrompt")
          }
          icon={<FileText className="size-4" />}
          open={openSections.prompt}
          onToggle={() => toggleSection("prompt")}
        >
          <PromptContext systemPrompt={systemPrompt} loading={promptLoading} />
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
          <ToolsContext tools={tools} loading={toolsQuery.isLoading} />
        </ContextSectionCard>

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
              meta={contextQuery.data?.meta}
              metaLoading={contextQuery.isLoading}
            />
          )}
        </ContextSectionCard>
      </div>
    </div>
  );
}

// ── Section Card ────────────────────────────────────────────────────────────

function ContextSectionCard({
  title,
  detail,
  icon,
  open,
  children,
  onToggle,
}: {
  title: string;
  detail: string;
  icon: React.ReactNode;
  open: boolean;
  children: React.ReactNode;
  onToggle: () => void;
}) {
  return (
    <section className="overflow-hidden rounded-xl border border-border bg-card">
      <button
        type="button"
        onClick={onToggle}
        className="grid w-full min-w-0 grid-cols-[2rem_minmax(0,1fr)_1rem] items-center gap-2.5 px-4 py-3 text-left cursor-pointer"
      >
        <span className="grid size-8 place-items-center rounded-lg bg-muted border border-border/10 text-muted-foreground shrink-0">
          {icon}
        </span>
        <span className="min-w-0">
          <span className="block truncate text-sm font-semibold text-foreground">{title}</span>
          <span className="mt-0.5 block truncate text-xs text-muted-foreground leading-none">
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
      {open && <div className="border-t border-border p-4">{children}</div>}
    </section>
  );
}

// ── Session Context ─────────────────────────────────────────────────────────

function SessionContextCard({
  session,
  meta,
  metaLoading,
}: {
  session: Session;
  meta?: {
    message_count: number;
    source_token_count: number;
    active_token_count: number;
    summary_depth: number;
  };
  metaLoading: boolean;
}) {
  const { t } = useI18n();
  const copyID = useCallback(() => {
    navigator.clipboard.writeText(session.id).catch(console.error);
  }, [session.id]);
  const agentName = (session as Session & { agent_name?: string }).agent_name || session.agent_id;

  return (
    <div className="space-y-3.5">
      <dl className="grid grid-cols-[5.2rem_minmax(0,1fr)] gap-x-3 gap-y-2 text-xs">
        <dt className="text-muted-foreground font-semibold text-xs">
          {t("sessions.inspector.channel")}
        </dt>
        <dd className="truncate text-foreground font-medium">
          {channelLabel(session.channel) || "chat"}
        </dd>
        <dt className="text-muted-foreground font-semibold text-xs">
          {t("sessions.inspector.agent")}
        </dt>
        <dd className="truncate text-foreground font-medium">{agentName || "unknown"}</dd>
        <dt className="text-muted-foreground font-semibold text-xs">
          {t("sessions.inspector.kind")}
        </dt>
        <dd className="truncate capitalize text-foreground font-medium">{session.kind}</dd>
        <dt className="text-muted-foreground font-semibold text-xs">
          {t("sessions.inspector.active")}
        </dt>
        <dd className="truncate text-foreground font-medium">{formatTime(session.last_active)}</dd>
        <dt className="text-muted-foreground font-semibold text-xs">
          {t("sessions.inspector.messages")}
        </dt>
        <dd className="truncate text-foreground font-medium">
          {metaLoading ? "..." : (meta?.message_count ?? 0).toLocaleString()}
        </dd>
        <dt className="text-muted-foreground font-semibold text-xs">
          {t("sessions.inspector.tokens")}
        </dt>
        <dd className="truncate text-foreground font-medium">
          {metaLoading
            ? "..."
            : `${(meta?.active_token_count ?? 0).toLocaleString()} / ${(meta?.source_token_count ?? 0).toLocaleString()}`}
        </dd>
        <dt className="text-muted-foreground font-semibold text-xs">
          {t("sessions.inspector.longTerm")}
        </dt>
        <dd className="truncate text-foreground font-medium">
          {metaLoading
            ? "..."
            : t("sessions.inspector.summaryDepth", { count: meta?.summary_depth ?? 0 })}
        </dd>
        <dt className="text-muted-foreground font-semibold text-xs">
          {t("sessions.inspector.sessionId")}
        </dt>
        <dd className="truncate font-mono text-xs text-muted-foreground">{session.id}</dd>
      </dl>
      <div className="flex justify-end border-t border-border/10 pt-2.5">
        <Button type="button" variant="outline" size="xs" onClick={copyID}>
          {t("sessions.inspector.copySessionId")}
        </Button>
      </div>
    </div>
  );
}

// ── Tools Context ───────────────────────────────────────────────────────────

function ToolsContext({ tools, loading }: { tools: Tool[]; loading: boolean }) {
  const { t } = useI18n();
  if (loading) return <ContextEmpty>{t("sessions.inspector.loadingTools")}</ContextEmpty>;
  if (tools.length === 0) return <ContextEmpty>{t("sessions.inspector.noTools")}</ContextEmpty>;

  return (
    <div className="grid gap-2">
      {Object.entries(groupToolsBySource(tools)).map(([source, items]) => (
        <div key={source} className="grid gap-1">
          <div className="text-xs font-semibold text-muted-foreground">{source}</div>
          {items.map((tool) => (
            <ToolContextRow key={tool.name} tool={tool} />
          ))}
        </div>
      ))}
    </div>
  );
}

function groupToolsBySource(tools: Tool[]) {
  return tools.reduce<Record<string, Tool[]>>((acc, tool) => {
    const source = tool.source ?? "builtin";
    acc[source] = [...(acc[source] ?? []), tool];
    return acc;
  }, {});
}

function ToolContextRow({ tool }: { tool: Tool }) {
  const { t } = useI18n();
  const [expanded, setExpanded] = useState(false);

  return (
    <div className="border-b border-border/40 last:border-b-0 py-2 first:pt-0 last:pb-0">
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        className="grid w-full grid-cols-[1rem_minmax(0,1fr)] gap-2 text-left hover:bg-muted/20 p-1.5 rounded-lg cursor-pointer"
      >
        <span className="pt-0.5 text-xs text-muted-foreground">{expanded ? "▼" : "▶"}</span>
        <span className="min-w-0">
          <span className="flex min-w-0 items-center gap-2">
            <span className="truncate font-mono text-xs font-semibold text-foreground">
              {tool.name}
            </span>
            <span className="shrink-0 rounded-full border border-border px-1.5 py-0.5 text-xs text-muted-foreground">
              {tool.enabled ? tool.origin : `${tool.origin} off`}
            </span>
          </span>
          <span className="mt-1 block text-xs text-muted-foreground leading-normal">
            {tool.description}
          </span>
        </span>
      </button>
      {expanded &&
        (tool.input_schema ? (
          <pre className="mt-2 overflow-x-auto rounded-lg bg-muted/40 p-2 border border-border/15 font-mono text-xs leading-relaxed text-muted-foreground">
            {JSON.stringify(tool.input_schema, null, 2)}
          </pre>
        ) : (
          <p className="mt-2 px-1.5 text-xs text-muted-foreground">
            {t("sessions.inspector.noToolSchema")}
          </p>
        ))}
    </div>
  );
}

// ── Prompt Context ──────────────────────────────────────────────────────────

function PromptContext({ systemPrompt, loading }: { systemPrompt: string; loading: boolean }) {
  const { t } = useI18n();
  const copyPrompt = useCallback(() => {
    navigator.clipboard.writeText(systemPrompt).catch(console.error);
  }, [systemPrompt]);

  if (loading) return <ContextEmpty>{t("sessions.inspector.loadingPrompt")}</ContextEmpty>;

  return (
    <div className="grid gap-2">
      <div className="flex items-center justify-between gap-2 px-1">
        <span className="font-mono text-xs text-muted-foreground">
          ~{Math.round(systemPrompt.length / 4)} tokens
        </span>
        <Button type="button" variant="outline" size="xs" onClick={copyPrompt}>
          {t("sessions.inspector.copy")}
        </Button>
      </div>
      <pre className="whitespace-pre-wrap rounded-xl border border-border/60 bg-muted/40 p-3.5 font-mono text-xs leading-relaxed text-muted-foreground">
        {systemPrompt || t("sessions.inspector.noPrompt")}
      </pre>
    </div>
  );
}

// ── Utilities ───────────────────────────────────────────────────────────────

function ContextEmpty({ children }: { children: React.ReactNode }) {
  return <p className="px-2 py-8 text-center text-xs text-muted-foreground">{children}</p>;
}

function formatTokenCount(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`;
  return String(n);
}

function formatTime(value?: string | null): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function channelLabel(ch: string | null | undefined): string {
  if (!ch) return "";
  const m = ch.match(/:channel:([^:]+)/);
  return m ? m[1] : ch;
}
