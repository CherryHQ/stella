import { useCallback, useEffect } from "react";
import type { TFunction } from "i18next";
import { useQuery } from "@tanstack/react-query";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import type { ComponentsGoal } from "@/lib/api-client/types.gen";
import { goalsOptions } from "@/lib/queries/goals";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/messages";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { useAppShell } from "@/layouts/AppShell";
import { StatusDot, StatusPill, avatarInitials, goalNeedsYou, statusLabel } from "./lib";

export type GoalsView = "triage" | "board" | "table";
const VIEWS: GoalsView[] = ["triage", "board", "table"];
const VIEW_LABEL: Record<GoalsView, MessageKey> = {
  triage: "goals.viewTriage",
  board: "goals.viewBoard",
  table: "goals.viewTable",
};

export function GoalsPage() {
  const { t } = useI18n();
  const { agentId } = useParams({ from: "/_app/agents/$agentId/automations/" });
  const search = useSearch({ from: "/_app/agents/$agentId/automations/" });
  const view = (search as Record<string, unknown>).view as string | undefined;
  const navigate = useNavigate();
  const { setHeaderTitle, setHeaderActions } = useAppShell();
  const { data: goals = [], isLoading } = useQuery(goalsOptions(agentId));

  const cur: GoalsView = VIEWS.includes(view as GoalsView) ? (view as GoalsView) : "triage";
  const setView = useCallback(
    (v: GoalsView) =>
      void navigate({
        to: "/agents/$agentId/automations",
        params: { agentId },
        search: { view: v } as Record<string, unknown>,
      }),
    [agentId, navigate],
  );
  const openGoal = (g: ComponentsGoal) =>
    void navigate({
      to: "/agents/$agentId/automations/goals/$goalId",
      params: { agentId, goalId: g.id },
    });

  useEffect(() => {
    setHeaderTitle(
      <div className="min-w-0">
        <div className="truncate font-mono text-[10px] font-semibold text-muted-foreground">
          {t("goals.eyebrow")}
        </div>
        <h1 className="truncate text-[15px] font-semibold tracking-[-0.01em]">
          {t("goals.title")}
        </h1>
      </div>,
    );
    setHeaderActions(
      <div className="flex items-center gap-3">
        <div className="inline-flex gap-1 rounded-full bg-muted p-0.5">
          {VIEWS.map((v) => (
            <button
              key={v}
              onClick={() => setView(v)}
              className={cn(
                "rounded-full px-3.5 py-1 text-xs font-medium transition-colors",
                cur === v
                  ? "bg-card text-foreground shadow-sm"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              {t(VIEW_LABEL[v])}
            </button>
          ))}
        </div>
        <span className="font-mono text-xs text-muted-foreground">
          {t("goals.unit", { count: goals.length })}
        </span>
      </div>,
    );
    return () => {
      setHeaderTitle(null);
      setHeaderActions(null);
    };
  }, [cur, goals.length, setHeaderActions, setHeaderTitle, setView, t]);

  return (
    <div className="flex h-full min-h-0 flex-col overflow-hidden bg-background">
      <div className="min-h-0 flex-1 overflow-y-auto">
        {isLoading ? (
          <div className="flex items-center justify-center py-20">
            <div className="size-4 animate-spin rounded-full border-2 border-muted-foreground/30 border-t-muted-foreground" />
          </div>
        ) : goals.length === 0 ? (
          <Empty />
        ) : (
          <div className="mx-auto max-w-[1140px] px-6 py-6">
            {cur === "triage" && <Triage goals={goals} onOpen={openGoal} />}
            {cur === "board" && <Board goals={goals} onOpen={openGoal} />}
            {cur === "table" && <Table goals={goals} onOpen={openGoal} />}
          </div>
        )}
      </div>
    </div>
  );
}

function Empty() {
  const { t } = useI18n();
  return (
    <div className="flex flex-col items-center justify-center py-24 text-center">
      <p className="text-sm font-medium text-muted-foreground">{t("goals.empty")}</p>
      <p className="mt-1 max-w-xs text-xs text-muted-foreground">{t("goals.emptyDesc")}</p>
    </div>
  );
}

function hookText(t: TFunction, g: ComponentsGoal): string | null {
  if (g.status === "blocked") return t("goals.hookBlocked");
  if (g.status === "reviewing") return t("goals.hookReviewing");
  return null;
}

const byUpdatedDesc = (a: ComponentsGoal, b: ComponentsGoal) =>
  new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime();

interface ViewProps {
  goals: ComponentsGoal[];
  onOpen: (g: ComponentsGoal) => void;
}

function Triage({ goals, onOpen }: ViewProps) {
  const { t } = useI18n();
  const needs = goals.filter(goalNeedsYou).sort(byUpdatedDesc);
  const prog = goals
    .filter((g) => g.status === "running" || g.status === "planning")
    .sort(byUpdatedDesc);
  const closed = goals
    .filter((g) => ["done", "failed", "cancelled", "draft"].includes(g.status))
    .sort(byUpdatedDesc);

  const Section = ({ label, arr, dim }: { label: string; arr: ComponentsGoal[]; dim?: boolean }) =>
    arr.length ? (
      <section className="mb-7">
        <div className="mb-3 flex items-center gap-2.5">
          <span className="font-mono text-[11px] font-semibold text-muted-foreground">{label}</span>
          <span className="rounded-full bg-muted px-2 py-0.5 font-mono text-[11px] text-muted-foreground">
            {arr.length}
          </span>
          <span className="h-px flex-1 bg-border" />
        </div>
        <div className="space-y-2">
          {arr.map((g) => (
            <Row key={g.id} g={g} onOpen={onOpen} dim={dim} />
          ))}
        </div>
      </section>
    ) : null;

  return (
    <>
      <Section label={t("goals.secNeedsYou")} arr={needs} />
      <Section label={t("goals.secInProgress")} arr={prog} />
      <Section label={t("goals.secClosed")} arr={closed} dim />
    </>
  );
}

function Row({
  g,
  onOpen,
  dim,
}: {
  g: ComponentsGoal;
  onOpen: (g: ComponentsGoal) => void;
  dim?: boolean;
}) {
  const { t } = useI18n();
  const hook = hookText(t, g);
  const needs = goalNeedsYou(g);
  return (
    <button
      type="button"
      onClick={() => onOpen(g)}
      className={cn(
        "flex w-full items-center gap-3.5 rounded-2xl border bg-card px-4 py-3 text-left transition-shadow hover:shadow-md",
        needs ? "border-primary/30" : "border-border",
        dim && "opacity-65 hover:opacity-100",
      )}
    >
      <StatusPill status={g.status} label={statusLabel(t, g.status)} />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate font-serif text-[15px] font-semibold">{g.title}</span>
          {g.priority === "urgent" && (
            <span className="rounded-md border border-chart-4/25 bg-chart-4/10 px-1.5 py-0.5 font-mono text-[10px] font-medium text-chart-4">
              urgent
            </span>
          )}
        </div>
        <div
          className={cn(
            "mt-1 truncate text-[12.5px]",
            hook ? "font-medium text-primary/90" : "text-muted-foreground",
          )}
        >
          {hook ?? g.description ?? ""}
        </div>
      </div>
      <span className="shrink-0 font-mono text-[11px] text-muted-foreground">
        {formatTime(g.updated_at)}
      </span>
    </button>
  );
}

const BOARD_COLS: {
  labelKey: MessageKey;
  status: string;
  match: (g: ComponentsGoal) => boolean;
}[] = [
  {
    labelKey: "goals.colPlanning",
    status: "draft",
    match: (g) => ["draft", "planning"].includes(g.status),
  },
  {
    labelKey: "goals.colRunning",
    status: "running",
    match: (g) => g.status === "running",
  },
  { labelKey: "goals.colNeedsYou", status: "reviewing", match: goalNeedsYou },
  {
    labelKey: "goals.colClosed",
    status: "done",
    match: (g) => ["done", "failed", "cancelled"].includes(g.status),
  },
];

function Board({ goals, onOpen }: ViewProps) {
  const { t } = useI18n();
  return (
    <div className="grid grid-cols-1 items-start gap-3.5 sm:grid-cols-2 lg:grid-cols-4">
      {BOARD_COLS.map((col) => {
        const items = goals.filter(col.match).sort(byUpdatedDesc);
        return (
          <div key={col.labelKey} className="min-h-[180px] rounded-2xl bg-muted p-2.5">
            <div className="flex items-center gap-2 px-1.5 pb-2.5 pt-1">
              <StatusDot status={col.status} />
              <span className="font-mono text-[11px] font-semibold">{t(col.labelKey)}</span>
              <span className="ml-auto font-mono text-[11px] text-muted-foreground">
                {items.length}
              </span>
            </div>
            <div className="space-y-2">
              {items.map((g) => (
                <button
                  key={g.id}
                  type="button"
                  onClick={() => onOpen(g)}
                  className="block w-full rounded-xl border border-border bg-card px-3 py-2.5 text-left transition-shadow hover:shadow-md"
                >
                  <div className="font-serif text-[13.5px] font-semibold leading-snug">
                    {g.title}
                  </div>
                  <div className="mt-2 flex flex-wrap items-center gap-1.5">
                    <StatusPill status={g.status} label={statusLabel(t, g.status)} />
                    {g.priority === "urgent" && (
                      <span className="rounded-md border border-chart-4/25 bg-chart-4/10 px-1.5 py-0.5 font-mono text-[10px] font-medium text-chart-4">
                        urgent
                      </span>
                    )}
                  </div>
                  {goalNeedsYou(g) && (
                    <div className="mt-1.5 text-[11.5px] font-medium text-primary/90">
                      {hookText(t, g)}
                    </div>
                  )}
                </button>
              ))}
              {!items.length && (
                <div className="py-5 text-center text-xs text-muted-foreground">—</div>
              )}
            </div>
          </div>
        );
      })}
    </div>
  );
}

const TABLE_ORDER = [
  "blocked",
  "reviewing",
  "running",
  "planning",
  "draft",
  "failed",
  "done",
  "cancelled",
];

function Table({ goals, onOpen }: ViewProps) {
  const { t } = useI18n();
  const rows = [...goals].sort(
    (a, b) => TABLE_ORDER.indexOf(a.status) - TABLE_ORDER.indexOf(b.status),
  );
  return (
    <div className="overflow-hidden rounded-2xl border border-border bg-card">
      <table className="w-full border-collapse">
        <thead>
          <tr className="border-b border-border bg-muted/50 text-left font-mono text-[10.5px] text-muted-foreground">
            <th className="w-[120px] px-3.5 py-2.5 font-semibold">{t("goals.colStatus")}</th>
            <th className="px-3.5 py-2.5 font-semibold">{t("goals.colGoal")}</th>
            <th className="w-[120px] px-3.5 py-2.5 font-semibold">{t("goals.colAttention")}</th>
            <th className="w-[150px] px-3.5 py-2.5 font-semibold">{t("goals.colAgent")}</th>
            <th className="w-[90px] px-3.5 py-2.5 font-semibold">{t("goals.colUpdated")}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((g) => (
            <tr
              key={g.id}
              onClick={() => onOpen(g)}
              className="cursor-pointer border-b border-border last:border-0 hover:bg-accent/40"
            >
              <td className="px-3.5 py-3">
                <StatusPill status={g.status} label={statusLabel(t, g.status)} />
              </td>
              <td className="px-3.5 py-3">
                <div className="font-medium">{g.title}</div>
                {g.description && (
                  <div className="mt-0.5 truncate text-[11.5px] text-muted-foreground">
                    {g.description}
                  </div>
                )}
              </td>
              <td className="px-3.5 py-3">
                {goalNeedsYou(g) ? (
                  <span className="rounded-md border border-primary/25 bg-primary/10 px-2 py-0.5 font-mono text-[11px] font-medium text-primary">
                    {g.status === "blocked" ? t("goals.actUnblock") : t("goals.actReview")}
                  </span>
                ) : g.priority === "urgent" ? (
                  <span className="rounded-md border border-chart-4/25 bg-chart-4/10 px-2 py-0.5 font-mono text-[11px] font-medium text-chart-4">
                    urgent
                  </span>
                ) : (
                  <span className="font-mono text-xs text-muted-foreground">—</span>
                )}
              </td>
              <td className="px-3.5 py-3">
                {g.agent_id ? (
                  <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
                    <span className="grid size-[18px] place-items-center rounded-full bg-accent text-[9px] font-bold text-accent-foreground">
                      {avatarInitials(g.agent_id)}
                    </span>
                  </span>
                ) : (
                  <span className="font-mono text-xs text-muted-foreground">—</span>
                )}
              </td>
              <td className="px-3.5 py-3">
                <span className="font-mono text-[11.5px] text-muted-foreground">
                  {formatTime(g.updated_at)}
                </span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
