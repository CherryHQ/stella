import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import type { TFunction } from "i18next";
import { Archive, Columns3, History, Inbox, Search, Table as TableIcon } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
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
import { AgentChip } from "@/features/goals/DetailShell";
import {
  blockReasonLabel,
  type DisplayStatus,
  goalNeedsYou,
  displayStatus,
  priorityLabel,
  StatusDot,
  StatusPill,
  statusLabel,
} from "@/features/goals/lib";
import { useAppShell } from "@/layouts/AppShell";
import { deleteGoal, unarchiveGoal } from "@/lib/api-client";
import type { ComponentsGoal } from "@/lib/api-client/types.gen";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/messages";
import { GOALS_PAGE_SIZE, goalCountsOptions, goalsPageOptions } from "@/lib/queries/goals";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";

export type GoalsView = "triage" | "board" | "table";
type GoalsMode = "active" | "history" | "archived";
type StatusFilter = "all" | DisplayStatus;

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

// Terminal lifecycles close a goal out of the active set.
const TERMINAL_LIFECYCLES = new Set(["done"]);
const isTerminal = (d: ComponentsGoal) => TERMINAL_LIFECYCLES.has(d.lifecycle);

// The status filter is a DisplayStatus; map it back to the lifecycle the server
// scopes on. review/blocked both live under lifecycle=blocked (split by
// block_reason), which the page query can't express — so those filter
// client-side against the loaded page (the server still returns the full
// blocked set, just unsplit).
const FILTER_TO_LIFECYCLE: Partial<Record<DisplayStatus, string>> = {
  draft: "draft",
  pending: "pending",
  active: "active",
  accepted: "done",
  failed: "done",
  cancelled: "done",
};

const ACTIVE_FILTERS: DisplayStatus[] = ["draft", "pending", "active", "review", "blocked"];
const TERMINAL_FILTERS: DisplayStatus[] = ["accepted", "failed", "cancelled"];

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
  const status = ((rawSearch.status as string) || "all") as StatusFilter;
  const query = (rawSearch.q as string) || "";
  const workflowId = (rawSearch.workflow_id as string) || "";
  const page = Math.max(1, Number(rawSearch.page) || 1);
  const [selected, setSelected] = useState<Set<string>>(() => new Set());
  const [acting, setActing] = useState(false);

  // Mode maps to the server's archived/terminal filters: active = non-terminal &
  // not archived, history = terminal & not archived, archived = archived rows.
  const archived = mode === "archived";
  const terminal = workflowId
    ? undefined
    : mode === "active"
      ? false
      : mode === "history"
        ? true
        : undefined;

  const { data: counts } = useQuery(goalCountsOptions(agentId));
  const c = counts ?? { active: 0, history: 0, archived: 0 };
  const { data: pageData, isLoading } = useQuery(
    goalsPageOptions({
      agentId,
      archived,
      terminal,
      lifecycle: status === "all" ? undefined : FILTER_TO_LIFECYCLE[status],
      workflowId: workflowId || undefined,
      q: query || undefined,
      page,
    }),
  );
  // review/blocked share a lifecycle, so they need a client-side split after the
  // server returns the blocked page.
  const rows = useMemo(() => {
    const all = pageData?.goals ?? [];
    if (status === "review") return all.filter((d) => displayStatus(d) === "review");
    if (status === "blocked") return all.filter((d) => displayStatus(d) === "blocked");
    return all;
  }, [pageData, status]);
  const total = pageData?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / GOALS_PAGE_SIZE));

  const patch = useCallback(
    (next: Record<string, unknown>, replace = false) =>
      void navigate({
        to: "/agents/$agentId/goals/all",
        params: { agentId },
        search: { ...rawSearch, ...next } as Record<string, unknown>,
        replace,
      }),
    [agentId, navigate, rawSearch],
  );
  const setView = useCallback((v: GoalsView) => patch({ view: v }), [patch]);
  // Each mode exposes a different status set (active vs terminal), so a status
  // carried over from the previous mode would filter against values the new
  // mode can never contain — a fake-empty list. Clear it on switch.
  const setMode = useCallback(
    (m: GoalsMode) => patch({ mode: m, page: 1, status: undefined }),
    [patch],
  );
  const openGoal = (d: ComponentsGoal) =>
    void navigate({
      to: "/agents/$agentId/goals/$goalId",
      params: { agentId, goalId: d.id },
    });

  // History archives the selected terminal goals; the archived view
  // restores them. The server already scopes each mode, so every selected row on
  // the page is actionable. Active goals are never bulk-actionable.
  const selectedActionable = useMemo(
    () => (mode === "active" ? [] : rows.filter((d) => selected.has(d.id))),
    [rows, selected, mode],
  );

  useEffect(() => setSelected(new Set()), [mode, query, status, page]);
  // Clamp the page after a bulk action (or a filter change) shrinks the result
  // set so the pager never points past the last page. Only clamp once a real
  // total is loaded; clamping while isLoading would bounce a cold deep-link to
  // ?page=N back to page 1 before its data arrives.
  useEffect(() => {
    if (!isLoading && page > totalPages) patch({ page: totalPages }, true);
  }, [isLoading, page, totalPages, patch]);

  const runBulk = useCallback(async () => {
    const ids = selectedActionable.map((d) => d.id);
    if (!ids.length) return;
    setActing(true);
    try {
      await Promise.all(
        ids.map((id) =>
          mode === "archived"
            ? unarchiveGoal({ path: { id }, throwOnError: true })
            : deleteGoal({ path: { id }, throwOnError: true }),
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
      <h1 className="truncate text-[15px] font-semibold tracking-[-0.01em]">
        {workflowId ? t("workflows.runsTitle") : t("goals.title")}
      </h1>,
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
    workflowId,
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
            {rows.length === 0 ? (
              <FilteredEmpty />
            ) : (
              <>
                {cur === "triage" && (
                  <Triage
                    rows={rows}
                    onOpen={openGoal}
                    selected={selected}
                    onSelect={setSelected}
                  />
                )}
                {cur === "board" && (
                  <Board rows={rows} onOpen={openGoal} selected={selected} onSelect={setSelected} />
                )}
                {cur === "table" && (
                  <Table rows={rows} onOpen={openGoal} selected={selected} onSelect={setSelected} />
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
  status: StatusFilter;
  mode: GoalsMode;
  total: number;
  selectedActionableCount: number;
  acting: boolean;
  onQueryChange: (value: string) => void;
  onStatusChange: (value: StatusFilter) => void;
  onBulkAction: () => void;
}) {
  const { t } = useI18n();
  const statusOptions = mode === "active" ? ACTIVE_FILTERS : TERMINAL_FILTERS;
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
      <Select value={status} onValueChange={(value) => onStatusChange(value as StatusFilter)}>
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
              ? t("goals.restoreSelected", {
                  count: selectedActionableCount,
                })
              : t("goals.archiveSelected", {
                  count: selectedActionableCount,
                })}
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

// One-line attention hook a goal's lifecycle/block_reason earns it.
function hookText(t: TFunction, d: ComponentsGoal): string | null {
  if (d.lifecycle === "blocked") {
    if (d.block_reason === "needs_verdict") return t("goals.hookNeedsVerdict");
    if (d.block_reason === "needs_plan_approval") return t("goals.hookNeedsPlanApproval");
    if (d.block_reason === "budget_exhausted") return t("goals.hookBudget");
    if (d.block_reason === "planning_invalid") return t("goals.hookPlanningInvalid");
    if (d.block_reason === "env_unavailable") return t("goals.hookEnvironment");
    if (d.block_reason === "contract_conflict") return t("goals.hookContract");
    return blockReasonLabel(t, d);
  }
  if (d.lifecycle === "done" && d.done_reason === "accepted")
    return t("goals.acceptedAt", {
      time: formatTime(d.accepted_at ?? d.updated_at),
    });
  if (d.lifecycle === "done" && d.done_reason === "cancelled") return t("goals.hookCancelled");
  if (d.lifecycle === "done") return t("goals.hookFailed");
  return null;
}

const byUpdatedDesc = (a: ComponentsGoal, b: ComponentsGoal) =>
  new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime();

interface ViewProps {
  rows: ComponentsGoal[];
  onOpen: (d: ComponentsGoal) => void;
  selected: Set<string>;
  onSelect: (next: Set<string>) => void;
}

function toggleSelected(selected: Set<string>, id: string, onSelect: (next: Set<string>) => void) {
  const next = new Set(selected);
  if (next.has(id)) next.delete(id);
  else next.add(id);
  onSelect(next);
}

function Triage({ rows, onOpen, selected, onSelect }: ViewProps) {
  const { t } = useI18n();
  const needs = rows.filter(goalNeedsYou).sort(byUpdatedDesc);
  const prog = rows.filter((d) => !goalNeedsYou(d) && !isTerminal(d)).sort(byUpdatedDesc);
  const closed = rows.filter(isTerminal).sort(byUpdatedDesc);

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
          {arr.map((d) => (
            <Row
              key={d.id}
              d={d}
              onOpen={onOpen}
              selected={selected.has(d.id)}
              onSelect={() => toggleSelected(selected, d.id, onSelect)}
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
  d,
  onOpen,
  selected,
  onSelect,
  dim,
}: {
  d: ComponentsGoal;
  onOpen: (d: ComponentsGoal) => void;
  selected: boolean;
  onSelect: () => void;
  dim?: boolean;
}) {
  const { t } = useI18n();
  const hook = hookText(t, d);
  const needs = goalNeedsYou(d);
  const s = displayStatus(d);
  return (
    <div
      className={cn(
        "flex w-full items-center gap-3.5 rounded-2xl border bg-card px-4 py-3 text-left transition-shadow hover:shadow-md",
        needs ? "border-primary/30" : "border-border",
        dim && "opacity-65 hover:opacity-100",
      )}
    >
      <Checkbox checked={selected} onCheckedChange={onSelect} aria-label={t("goals.selectItem")} />
      <button
        type="button"
        onClick={() => onOpen(d)}
        className="flex min-w-0 flex-1 items-center gap-3.5 text-left"
      >
        <StatusPill status={s} label={statusLabel(t, s)} />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="truncate font-serif text-[15px] font-semibold">{d.title}</span>
            {d.priority === "urgent" && (
              <span className="rounded-md border border-chart-4/25 bg-chart-4/10 px-1.5 py-0.5 font-mono text-xs font-medium text-chart-4">
                {priorityLabel(t, d.priority)}
              </span>
            )}
          </div>
          <div
            className={cn(
              "mt-1 truncate text-[12.5px]",
              hook ? "font-medium text-primary/90" : "text-muted-foreground",
            )}
          >
            {hook ?? d.intent ?? t("goals.progressHint")}
          </div>
        </div>
        <span className="shrink-0 font-mono text-xs text-muted-foreground">
          {formatTime(d.updated_at)}
        </span>
      </button>
    </div>
  );
}

const BOARD_COLS: {
  labelKey: MessageKey;
  status: DisplayStatus;
  match: (d: ComponentsGoal) => boolean;
}[] = [
  {
    labelKey: "goals.colPlanning",
    status: "draft",
    match: (d) => d.lifecycle === "draft" || d.lifecycle === "pending",
  },
  {
    labelKey: "goals.colRunning",
    status: "active",
    match: (d) => d.lifecycle === "active" || (d.lifecycle === "blocked" && !goalNeedsYou(d)),
  },
  {
    labelKey: "goals.colNeedsYou",
    status: "review",
    match: goalNeedsYou,
  },
  {
    labelKey: "goals.colClosed",
    status: "accepted",
    match: isTerminal,
  },
];

function Board({ rows, onOpen, selected, onSelect }: ViewProps) {
  const { t } = useI18n();
  return (
    <div className="grid grid-cols-1 items-start gap-3.5 sm:grid-cols-2 lg:grid-cols-4">
      {BOARD_COLS.map((col) => {
        const items = rows.filter(col.match).sort(byUpdatedDesc);
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
              {items.map((d) => {
                const s = displayStatus(d);
                return (
                  <div
                    key={d.id}
                    className="rounded-xl border border-border bg-card px-3 py-2.5 transition-shadow hover:shadow-md"
                  >
                    <div className="mb-2 flex items-center gap-2">
                      <Checkbox
                        checked={selected.has(d.id)}
                        onCheckedChange={() => toggleSelected(selected, d.id, onSelect)}
                        aria-label={t("goals.selectItem")}
                      />
                      <button
                        type="button"
                        onClick={() => onOpen(d)}
                        className="min-w-0 flex-1 text-left"
                      >
                        <div className="truncate font-serif text-[13.5px] font-semibold leading-snug">
                          {d.title}
                        </div>
                      </button>
                    </div>
                    <div className="flex flex-wrap items-center gap-1.5">
                      <StatusPill status={s} label={statusLabel(t, s)} />
                      {d.priority === "urgent" && (
                        <span className="rounded-md border border-chart-4/25 bg-chart-4/10 px-1.5 py-0.5 font-mono text-xs font-medium text-chart-4">
                          {priorityLabel(t, d.priority)}
                        </span>
                      )}
                    </div>
                    {hookText(t, d) && (
                      <div className="mt-1.5 text-[11.5px] font-medium text-primary/90">
                        {hookText(t, d)}
                      </div>
                    )}
                  </div>
                );
              })}
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

// Sort priority for the table: attention-worthy rows float, terminal sinks.
const TABLE_ORDER: DisplayStatus[] = [
  "review",
  "blocked",
  "active",
  "pending",
  "draft",
  "failed",
  "accepted",
  "cancelled",
];

function Table({ rows, onOpen, selected, onSelect }: ViewProps) {
  const { t } = useI18n();
  const sorted = [...rows].sort(
    (a, b) => TABLE_ORDER.indexOf(displayStatus(a)) - TABLE_ORDER.indexOf(displayStatus(b)),
  );
  return (
    <div className="overflow-hidden rounded-2xl border border-border bg-card">
      <table className="w-full border-collapse">
        <thead>
          <tr className="border-b border-border bg-muted/50 text-left font-mono text-[10.5px] text-muted-foreground">
            <th className="w-[42px] px-3.5 py-2.5 font-semibold" />
            <th className="w-[120px] px-3.5 py-2.5 font-semibold">{t("goals.colStatus")}</th>
            <th className="px-3.5 py-2.5 font-semibold">{t("goals.colTitle")}</th>
            <th className="w-[160px] px-3.5 py-2.5 font-semibold">{t("goals.colAttention")}</th>
            <th className="w-[150px] px-3.5 py-2.5 font-semibold">{t("goals.colAgent")}</th>
            <th className="w-[90px] px-3.5 py-2.5 font-semibold">{t("goals.colUpdated")}</th>
          </tr>
        </thead>
        <tbody>
          {sorted.map((d) => {
            const s = displayStatus(d);
            return (
              <tr key={d.id} className="border-b border-border last:border-0 hover:bg-accent/40">
                <td className="px-3.5 py-3">
                  <Checkbox
                    checked={selected.has(d.id)}
                    onCheckedChange={() => toggleSelected(selected, d.id, onSelect)}
                    aria-label={t("goals.selectItem")}
                  />
                </td>
                <td className="cursor-pointer px-3.5 py-3" onClick={() => onOpen(d)}>
                  <StatusPill status={s} label={statusLabel(t, s)} />
                </td>
                <td className="cursor-pointer px-3.5 py-3" onClick={() => onOpen(d)}>
                  <div className="font-medium">{d.title}</div>
                  {d.intent && (
                    <div className="mt-0.5 truncate text-[11.5px] text-muted-foreground">
                      {d.intent}
                    </div>
                  )}
                </td>
                <td className="px-3.5 py-3">
                  {goalNeedsYou(d) ? (
                    <span className="rounded-md border border-primary/25 bg-primary/10 px-2 py-0.5 font-mono text-xs font-medium text-primary">
                      {blockReasonLabel(t, d)}
                    </span>
                  ) : d.lifecycle === "done" && d.done_reason === "accepted" ? (
                    <span className="rounded-md border border-chart-3/25 bg-chart-3/10 px-2 py-0.5 font-mono text-xs font-medium text-chart-3">
                      {t("goals.hookAccepted")}
                    </span>
                  ) : d.priority === "urgent" ? (
                    <span className="rounded-md border border-chart-4/25 bg-chart-4/10 px-2 py-0.5 font-mono text-xs font-medium text-chart-4">
                      {priorityLabel(t, d.priority)}
                    </span>
                  ) : (
                    <span className="font-mono text-xs text-muted-foreground">—</span>
                  )}
                </td>
                <td className="px-3.5 py-3">
                  {d.agent_id ? (
                    <span className="text-xs text-muted-foreground">
                      <AgentChip agentId={d.agent_id} />
                    </span>
                  ) : (
                    <span className="font-mono text-xs text-muted-foreground">—</span>
                  )}
                </td>
                <td className="px-3.5 py-3">
                  <span className="font-mono text-[11.5px] text-muted-foreground">
                    {formatTime(d.updated_at)}
                  </span>
                </td>
              </tr>
            );
          })}
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
