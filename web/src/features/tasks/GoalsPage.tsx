import { useCallback, useEffect, useMemo, useState } from "react";
import { Archive, Columns3, History, Inbox, Search, Table as TableIcon } from "lucide-react";
import type { TFunction } from "i18next";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { deleteGoal, unarchiveGoal } from "@/lib/api-client";
import type { ComponentsGoal } from "@/lib/api-client/types.gen";
import { GOALS_PAGE_SIZE, goalCountsOptions, goalsPageOptions } from "@/lib/queries/goals";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/messages";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { useAppShell } from "@/layouts/AppShell";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectItem,
  SelectPopup,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { StatusDot, StatusPill, avatarInitials, goalNeedsYou, statusLabel } from "./lib";

export type GoalsView = "triage" | "board" | "table";
type GoalsMode = "active" | "history" | "archived";
type GoalStatusFilter = "all" | ComponentsGoal["status"];

const VIEWS: GoalsView[] = ["triage", "board", "table"];
const VIEW_LABEL: Record<GoalsView, MessageKey> = {
  triage: "goals.viewTriage",
  board: "goals.viewBoard",
  table: "goals.viewTable",
};
const VIEW_ICON: Record<GoalsView, typeof Inbox> = {
  triage: Inbox,
  board: Columns3,
  table: TableIcon,
};
const TERMINAL_STATUSES: ComponentsGoal["status"][] = ["done", "failed", "cancelled"];
const ACTIVE_STATUSES: ComponentsGoal["status"][] = [
  "blocked",
  "reviewing",
  "running",
  "planning",
  "draft",
];

export function GoalsPage() {
  const { t } = useI18n();
  const { agentId } = useParams({ strict: false }) as { agentId: string };
  const search = useSearch({ strict: false });
  const rawSearch = search as Record<string, unknown>;
  const view = rawSearch.view as string | undefined;
  const modeParam = rawSearch.mode as string | undefined;
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { setHeaderTitle, setHeaderActions } = useAppShell();

  // URL params are the source of truth for mode, status, search, and page so the
  // view is shareable and survives refresh/back-forward.
  const cur: GoalsView = VIEWS.includes(view as GoalsView) ? (view as GoalsView) : "triage";
  const mode: GoalsMode =
    modeParam === "history" ? "history" : modeParam === "archived" ? "archived" : "active";
  const status = ((rawSearch.status as string) || "all") as GoalStatusFilter;
  const query = (rawSearch.q as string) || "";
  const page = Math.max(1, Number(rawSearch.page) || 1);
  const [selected, setSelected] = useState<Set<string>>(() => new Set());
  const [acting, setActing] = useState(false);

  // Mode maps to the server's archived/terminal filters: active = non-terminal &
  // not archived, history = terminal & not archived, archived = archived rows.
  const archived = mode === "archived";
  const terminal = mode === "active" ? false : mode === "history" ? true : undefined;

  const { data: counts } = useQuery(goalCountsOptions(agentId));
  const c = counts ?? { active: 0, history: 0, archived: 0 };
  const { data: pageData, isLoading } = useQuery(
    goalsPageOptions({
      agentId,
      archived,
      terminal,
      status: status === "all" ? undefined : status,
      q: query || undefined,
      page,
    }),
  );
  const goals = pageData?.goals ?? [];
  const total = pageData?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / GOALS_PAGE_SIZE));

  const patch = useCallback(
    (next: Record<string, unknown>, replace = false) =>
      void navigate({
        to: "/agents/$agentId/tasks/goals",
        params: { agentId },
        search: { ...rawSearch, ...next } as Record<string, unknown>,
        replace,
      }),
    [agentId, navigate, rawSearch],
  );
  const setView = useCallback((v: GoalsView) => patch({ view: v }), [patch]);
  // Each mode exposes a different status set (active vs terminal), so a status
  // carried over from the previous mode would filter against values the new
  // mode can never contain — a fake-empty list. Clear it on switch (CR-020).
  const setMode = useCallback(
    (m: GoalsMode) => patch({ mode: m, page: 1, status: undefined }),
    [patch],
  );
  const openGoal = (g: ComponentsGoal) =>
    void navigate({
      to: "/agents/$agentId/tasks/goals/$goalId",
      params: { agentId, goalId: g.id },
    });

  // History archives the selected terminal goals; the archived view restores them.
  // The server already scopes each mode, so every selected row on the page is
  // actionable. Active goals are never bulk-actionable (they are not yet finished).
  const selectedActionable = useMemo(
    () => (mode === "active" ? [] : goals.filter((g) => selected.has(g.id))),
    [goals, selected, mode],
  );

  useEffect(() => setSelected(new Set()), [mode, query, status, page]);
  // Clamp the page after a bulk action (or a filter change) shrinks the result
  // set so the pager never points past the last page (CR-010).
  useEffect(() => {
    // Only clamp once a real total is loaded; clamping while isLoading would
    // bounce a cold deep-link to ?page=N back to page 1 before its data arrives
    // (CR-019).
    if (!isLoading && page > totalPages) patch({ page: totalPages }, true);
  }, [isLoading, page, totalPages, patch]);

  const runBulk = useCallback(async () => {
    const ids = selectedActionable.map((g) => g.id);
    if (!ids.length) return;
    setActing(true);
    try {
      await Promise.all(
        ids.map((goalId) =>
          mode === "archived"
            ? unarchiveGoal({ path: { goalId }, throwOnError: true })
            : deleteGoal({ path: { goalId }, throwOnError: true }),
        ),
      );
      await Promise.all([
        qc.invalidateQueries({ queryKey: ["goals-page"] }),
        qc.invalidateQueries({ queryKey: ["goals-counts"] }),
        qc.invalidateQueries({ queryKey: ["goals"] }),
      ]);
      setSelected(new Set());
    } finally {
      setActing(false);
    }
  }, [mode, qc, selectedActionable]);

  useEffect(() => {
    setHeaderTitle(
      <div className="min-w-0">
        <div className="truncate font-mono text-xs font-semibold text-muted-foreground">
          {t("goals.eyebrow")}
        </div>
        <h1 className="truncate text-[15px] font-semibold tracking-[-0.01em]">
          {t("goals.title")}
        </h1>
      </div>,
    );
    setHeaderActions(
      <div className="flex items-center gap-1">
        <Button
          size="sm"
          variant={mode === "active" ? "secondary" : "outline"}
          aria-pressed={mode === "active"}
          onClick={() => setMode("active")}
        >
          {t("goals.modeActive")}
          <span className="font-mono text-xs text-muted-foreground">{c.active}</span>
        </Button>
        <Button
          size="sm"
          variant={mode === "history" ? "secondary" : "outline"}
          aria-pressed={mode === "history"}
          onClick={() => setMode("history")}
        >
          <History />
          <span className="max-sm:hidden">{t("goals.modeHistory")}</span>
          <span className="font-mono text-xs text-muted-foreground">{c.history}</span>
        </Button>
        <Button
          size="sm"
          variant={mode === "archived" ? "secondary" : "outline"}
          aria-pressed={mode === "archived"}
          onClick={() => setMode("archived")}
        >
          <Archive />
          <span className="max-sm:hidden">{t("goals.modeArchived")}</span>
          <span className="font-mono text-xs text-muted-foreground">{c.archived}</span>
        </Button>
        {VIEWS.map((v) => {
          const Icon = VIEW_ICON[v];
          return (
            <Button
              key={v}
              size="sm"
              variant={cur === v ? "secondary" : "outline"}
              aria-pressed={cur === v}
              onClick={() => setView(v)}
            >
              <Icon />
              <span className="max-sm:hidden">{t(VIEW_LABEL[v])}</span>
            </Button>
          );
        })}
      </div>,
    );
    return () => {
      setHeaderTitle(null);
      setHeaderActions(null);
    };
  }, [
    c.active,
    c.history,
    c.archived,
    cur,
    mode,
    setHeaderActions,
    setHeaderTitle,
    setMode,
    setView,
    t,
  ]);

  return (
    <div className="flex h-full min-h-0 flex-col overflow-hidden bg-background">
      <div className="min-h-0 flex-1 overflow-y-auto">
        {isLoading ? (
          <div className="flex items-center justify-center py-20">
            <div className="size-4 animate-spin rounded-full border-2 border-muted-foreground/30 border-t-muted-foreground" />
          </div>
        ) : counts && c.active + c.history + c.archived === 0 ? (
          <Empty />
        ) : (
          <div className="mx-auto max-w-[1140px] px-6 py-6">
            <Toolbar
              query={query}
              status={status}
              mode={mode}
              total={total}
              selectedActionableCount={selectedActionable.length}
              acting={acting}
              onQueryChange={(value) => patch({ q: value || undefined, page: 1 }, true)}
              onStatusChange={(value) =>
                patch({ status: value === "all" ? undefined : value, page: 1 }, true)
              }
              onBulkAction={runBulk}
            />
            {goals.length === 0 ? (
              <FilteredEmpty />
            ) : (
              <>
                {cur === "triage" && (
                  <Triage
                    goals={goals}
                    onOpen={openGoal}
                    selected={selected}
                    onSelect={setSelected}
                  />
                )}
                {cur === "board" && (
                  <Board
                    goals={goals}
                    onOpen={openGoal}
                    selected={selected}
                    onSelect={setSelected}
                  />
                )}
                {cur === "table" && (
                  <Table
                    goals={goals}
                    onOpen={openGoal}
                    selected={selected}
                    onSelect={setSelected}
                  />
                )}
                <Pager
                  page={page}
                  totalPages={totalPages}
                  total={total}
                  onPage={(p) => patch({ page: p })}
                />
              </>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

function Toolbar({
  query,
  status,
  mode,
  total,
  selectedActionableCount,
  acting,
  onQueryChange,
  onStatusChange,
  onBulkAction,
}: {
  query: string;
  status: GoalStatusFilter;
  mode: GoalsMode;
  total: number;
  selectedActionableCount: number;
  acting: boolean;
  onQueryChange: (value: string) => void;
  onStatusChange: (value: GoalStatusFilter) => void;
  onBulkAction: () => void;
}) {
  const { t } = useI18n();
  const statusOptions =
    mode === "active"
      ? ACTIVE_STATUSES
      : mode === "history"
        ? TERMINAL_STATUSES
        : ([...TERMINAL_STATUSES, "draft"] as ComponentsGoal["status"][]);
  return (
    <div className="mb-5 flex flex-col gap-3 rounded-2xl border border-border bg-card p-3 sm:flex-row sm:items-center">
      <div className="flex min-w-0 flex-1 items-center gap-2">
        <Search className="size-4 shrink-0 text-muted-foreground" />
        <Input
          value={query}
          onChange={(e) => onQueryChange(e.target.value)}
          placeholder={t("goals.searchPlaceholder")}
          type="search"
          size="sm"
        />
      </div>
      <Select value={status} onValueChange={(value) => onStatusChange(value as GoalStatusFilter)}>
        <SelectTrigger size="sm" className="w-full sm:w-44">
          <SelectValue placeholder={t("goals.statusAll")} />
        </SelectTrigger>
        <SelectPopup>
          <SelectItem value="all">{t("goals.statusAll")}</SelectItem>
          {statusOptions.map((s) => (
            <SelectItem key={s} value={s}>
              {statusLabel(t, s)}
            </SelectItem>
          ))}
        </SelectPopup>
      </Select>
      <div className="flex items-center gap-2 sm:ml-auto">
        <span className="font-mono text-xs text-muted-foreground">
          {t("goals.filteredCount", { count: total })}
        </span>
        {mode !== "active" && (
          <Button
            variant="outline"
            size="sm"
            loading={acting}
            disabled={selectedActionableCount === 0}
            onClick={onBulkAction}
          >
            {mode === "archived"
              ? t("goals.restoreSelected", { count: selectedActionableCount })
              : t("goals.archiveSelected", { count: selectedActionableCount })}
          </Button>
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

function FilteredEmpty() {
  const { t } = useI18n();
  return (
    <div className="flex flex-col items-center justify-center rounded-2xl border border-border py-16 text-center">
      <p className="text-sm font-medium text-muted-foreground">{t("goals.noMatches")}</p>
      <p className="mt-1 max-w-sm text-xs text-muted-foreground">{t("goals.noMatchesDesc")}</p>
    </div>
  );
}

function hookText(t: TFunction, g: ComponentsGoal): string | null {
  if (g.status === "blocked") return t("goals.hookBlocked");
  if (g.status === "reviewing") return t("goals.hookReviewing");
  if (g.status === "done")
    return t("goals.hookAchieved", {
      time: formatTime(g.completed_at ?? g.updated_at),
    });
  if (g.status === "failed") return t("goals.hookFailed");
  if (g.status === "cancelled") return t("goals.hookCancelled");
  return null;
}

const byUpdatedDesc = (a: ComponentsGoal, b: ComponentsGoal) =>
  new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime();

interface ViewProps {
  goals: ComponentsGoal[];
  onOpen: (g: ComponentsGoal) => void;
  selected: Set<string>;
  onSelect: (next: Set<string>) => void;
}

function toggleSelected(selected: Set<string>, id: string, onSelect: (next: Set<string>) => void) {
  const next = new Set(selected);
  if (next.has(id)) next.delete(id);
  else next.add(id);
  onSelect(next);
}

function Triage({ goals, onOpen, selected, onSelect }: ViewProps) {
  const { t } = useI18n();
  const needs = goals.filter(goalNeedsYou).sort(byUpdatedDesc);
  const prog = goals
    .filter((g) => g.status === "running" || g.status === "planning" || g.status === "draft")
    .sort(byUpdatedDesc);
  const closed = goals.filter((g) => TERMINAL_STATUSES.includes(g.status)).sort(byUpdatedDesc);

  const Section = ({ label, arr, dim }: { label: string; arr: ComponentsGoal[]; dim?: boolean }) =>
    arr.length ? (
      <section className="mb-7">
        <div className="mb-3 flex items-center gap-2.5">
          <span className="font-mono text-xs font-semibold text-muted-foreground">{label}</span>
          <span className="rounded-full bg-muted px-2 py-0.5 font-mono text-xs text-muted-foreground">
            {arr.length}
          </span>
          <span className="h-px flex-1 bg-border" />
        </div>
        <div className="space-y-2">
          {arr.map((g) => (
            <Row
              key={g.id}
              g={g}
              onOpen={onOpen}
              selected={selected.has(g.id)}
              onSelect={() => toggleSelected(selected, g.id, onSelect)}
              dim={dim}
            />
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
  selected,
  onSelect,
  dim,
}: {
  g: ComponentsGoal;
  onOpen: (g: ComponentsGoal) => void;
  selected: boolean;
  onSelect: () => void;
  dim?: boolean;
}) {
  const { t } = useI18n();
  const hook = hookText(t, g);
  const needs = goalNeedsYou(g);
  return (
    <div
      className={cn(
        "flex w-full items-center gap-3.5 rounded-2xl border bg-card px-4 py-3 text-left transition-shadow hover:shadow-md",
        needs ? "border-primary/30" : "border-border",
        dim && "opacity-65 hover:opacity-100",
      )}
    >
      <Checkbox checked={selected} onCheckedChange={onSelect} aria-label={t("goals.selectGoal")} />
      <button
        type="button"
        onClick={() => onOpen(g)}
        className="flex min-w-0 flex-1 items-center gap-3.5 text-left"
      >
        <StatusPill status={g.status} label={statusLabel(t, g.status)} />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="truncate font-serif text-[15px] font-semibold">{g.title}</span>
            {g.status === "done" && (
              <span className="rounded-md border border-chart-3/25 bg-chart-3/10 px-1.5 py-0.5 font-mono text-xs font-medium text-chart-3">
                {t("goals.achieved")}
              </span>
            )}
            {g.priority === "urgent" && (
              <span className="rounded-md border border-chart-4/25 bg-chart-4/10 px-1.5 py-0.5 font-mono text-xs font-medium text-chart-4">
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
            {hook ?? g.description ?? t("goals.progressHint")}
          </div>
        </div>
        <span className="shrink-0 font-mono text-xs text-muted-foreground">
          {formatTime(g.updated_at)}
        </span>
      </button>
    </div>
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
    match: (g) => TERMINAL_STATUSES.includes(g.status),
  },
];

function Board({ goals, onOpen, selected, onSelect }: ViewProps) {
  const { t } = useI18n();
  return (
    <div className="grid grid-cols-1 items-start gap-3.5 sm:grid-cols-2 lg:grid-cols-4">
      {BOARD_COLS.map((col) => {
        const items = goals.filter(col.match).sort(byUpdatedDesc);
        return (
          <div key={col.labelKey} className="min-h-[180px] rounded-2xl bg-muted p-2.5">
            <div className="flex items-center gap-2 px-1.5 pb-2.5 pt-1">
              <StatusDot status={col.status} />
              <span className="font-mono text-xs font-semibold">{t(col.labelKey)}</span>
              <span className="ml-auto font-mono text-xs text-muted-foreground">
                {items.length}
              </span>
            </div>
            <div className="space-y-2">
              {items.map((g) => (
                <div
                  key={g.id}
                  className="rounded-xl border border-border bg-card px-3 py-2.5 transition-shadow hover:shadow-md"
                >
                  <div className="mb-2 flex items-center gap-2">
                    <Checkbox
                      checked={selected.has(g.id)}
                      onCheckedChange={() => toggleSelected(selected, g.id, onSelect)}
                      aria-label={t("goals.selectGoal")}
                    />
                    <button
                      type="button"
                      onClick={() => onOpen(g)}
                      className="min-w-0 flex-1 text-left"
                    >
                      <div className="truncate font-serif text-[13.5px] font-semibold leading-snug">
                        {g.title}
                      </div>
                    </button>
                  </div>
                  <div className="flex flex-wrap items-center gap-1.5">
                    <StatusPill status={g.status} label={statusLabel(t, g.status)} />
                    {g.status === "done" && (
                      <span className="rounded-md border border-chart-3/25 bg-chart-3/10 px-1.5 py-0.5 font-mono text-xs font-medium text-chart-3">
                        {t("goals.achieved")}
                      </span>
                    )}
                    {g.priority === "urgent" && (
                      <span className="rounded-md border border-chart-4/25 bg-chart-4/10 px-1.5 py-0.5 font-mono text-xs font-medium text-chart-4">
                        urgent
                      </span>
                    )}
                  </div>
                  {hookText(t, g) && (
                    <div className="mt-1.5 text-[11.5px] font-medium text-primary/90">
                      {hookText(t, g)}
                    </div>
                  )}
                </div>
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

function Table({ goals, onOpen, selected, onSelect }: ViewProps) {
  const { t } = useI18n();
  const rows = [...goals].sort(
    (a, b) => TABLE_ORDER.indexOf(a.status) - TABLE_ORDER.indexOf(b.status),
  );
  return (
    <div className="overflow-hidden rounded-2xl border border-border bg-card">
      <table className="w-full border-collapse">
        <thead>
          <tr className="border-b border-border bg-muted/50 text-left font-mono text-[10.5px] text-muted-foreground">
            <th className="w-[42px] px-3.5 py-2.5 font-semibold" />
            <th className="w-[120px] px-3.5 py-2.5 font-semibold">{t("goals.colStatus")}</th>
            <th className="px-3.5 py-2.5 font-semibold">{t("goals.colGoal")}</th>
            <th className="w-[120px] px-3.5 py-2.5 font-semibold">{t("goals.colAttention")}</th>
            <th className="w-[150px] px-3.5 py-2.5 font-semibold">{t("goals.colAgent")}</th>
            <th className="w-[90px] px-3.5 py-2.5 font-semibold">{t("goals.colUpdated")}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((g) => (
            <tr key={g.id} className="border-b border-border last:border-0 hover:bg-accent/40">
              <td className="px-3.5 py-3">
                <Checkbox
                  checked={selected.has(g.id)}
                  onCheckedChange={() => toggleSelected(selected, g.id, onSelect)}
                  aria-label={t("goals.selectGoal")}
                />
              </td>
              <td className="cursor-pointer px-3.5 py-3" onClick={() => onOpen(g)}>
                <StatusPill status={g.status} label={statusLabel(t, g.status)} />
              </td>
              <td className="cursor-pointer px-3.5 py-3" onClick={() => onOpen(g)}>
                <div className="font-medium">{g.title}</div>
                {g.description && (
                  <div className="mt-0.5 truncate text-[11.5px] text-muted-foreground">
                    {g.description}
                  </div>
                )}
              </td>
              <td className="px-3.5 py-3">
                {goalNeedsYou(g) ? (
                  <span className="rounded-md border border-primary/25 bg-primary/10 px-2 py-0.5 font-mono text-xs font-medium text-primary">
                    {g.status === "blocked" ? t("goals.actUnblock") : t("goals.actReview")}
                  </span>
                ) : g.status === "done" ? (
                  <span className="rounded-md border border-chart-3/25 bg-chart-3/10 px-2 py-0.5 font-mono text-xs font-medium text-chart-3">
                    {t("goals.achieved")}
                  </span>
                ) : g.priority === "urgent" ? (
                  <span className="rounded-md border border-chart-4/25 bg-chart-4/10 px-2 py-0.5 font-mono text-xs font-medium text-chart-4">
                    urgent
                  </span>
                ) : (
                  <span className="font-mono text-xs text-muted-foreground">—</span>
                )}
              </td>
              <td className="px-3.5 py-3">
                {g.agent_id ? (
                  <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
                    <span className="grid size-[18px] place-items-center rounded-full bg-accent text-xs font-semibold text-accent-foreground">
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

function Pager({
  page,
  totalPages,
  total,
  onPage,
}: {
  page: number;
  totalPages: number;
  total: number;
  onPage: (page: number) => void;
}) {
  const { t } = useI18n();
  if (totalPages <= 1) return null;
  return (
    <div className="mt-5 flex items-center justify-between gap-3">
      <span className="font-mono text-xs text-muted-foreground">
        {t("goals.pageStatus", { page, totalPages, total })}
      </span>
      <div className="flex items-center gap-2">
        <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => onPage(page - 1)}>
          {t("common.back")}
        </Button>
        <Button
          variant="outline"
          size="sm"
          disabled={page >= totalPages}
          onClick={() => onPage(page + 1)}
        >
          {t("common.next")}
        </Button>
      </div>
    </div>
  );
}
