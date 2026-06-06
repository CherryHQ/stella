import { useCallback, useEffect, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { goalsOptions } from "@/lib/queries/goals";
import { agentSchedulerJobsOptions } from "@/lib/queries/agents";
import { standaloneTasksOptions } from "./queries";
import { useI18n } from "@/lib/i18n";
import { useAppShell } from "@/layouts/AppShell";
import { ListPanel } from "./ListPanel";
import { DetailPanel } from "./DetailPanel";
import type { AutomationItem } from "./types";
import { classifyAll, parseItemKey } from "./types";
import type { SchedulerJob } from "@/lib/types";

const NEW_SCHEDULE_KEY = "new-schedule";

export function AutomationsPage() {
  const { t } = useI18n();
  const { agentId } = useParams({ from: "/_app/agents/$agentId/automations/" });
  const search = useSearch({ from: "/_app/agents/$agentId/automations/" });
  const navigate = useNavigate();
  const { setHeaderTitle, setHeaderActions } = useAppShell();

  const selectedKey = (search as Record<string, unknown>).item as string | undefined;
  const searchText = ((search as Record<string, unknown>).q as string) || "";
  const isNewSchedule = selectedKey === NEW_SCHEDULE_KEY;

  const { data: goals = [] } = useQuery(goalsOptions(agentId));
  const { data: jobs = [] } = useQuery(agentSchedulerJobsOptions(agentId));
  const { data: tasks = [] } = useQuery(standaloneTasksOptions(agentId));

  // Build unified item list
  const allItems: AutomationItem[] = useMemo(() => {
    const items: AutomationItem[] = [];
    for (const g of goals) items.push({ kind: "goal", id: g.id, data: g });
    for (const j of jobs as SchedulerJob[]) items.push({ kind: "schedule", id: j.id, data: j });
    for (const t of tasks) items.push({ kind: "task", id: t.id, data: t });
    return items;
  }, [goals, jobs, tasks]);

  // Filter by search
  const filtered = useMemo(() => {
    if (!searchText) return allItems;
    const q = searchText.toLowerCase();
    return allItems.filter((item) => {
      const name = item.kind === "schedule" ? item.data.name : item.data.title;
      const desc =
        item.kind === "schedule"
          ? item.data.description || item.data.message || ""
          : item.data.description || "";
      return name.toLowerCase().includes(q) || desc.toLowerCase().includes(q);
    });
  }, [allItems, searchText]);

  // Classify into sections
  const sections = useMemo(() => classifyAll(filtered), [filtered]);

  // Resolve selected item
  const selectedItem = useMemo(() => {
    if (!selectedKey || isNewSchedule) return null;
    const parsed = parseItemKey(selectedKey);
    if (!parsed) return null;
    return allItems.find((item) => item.kind === parsed.kind && item.id === parsed.id) ?? null;
  }, [selectedKey, isNewSchedule, allItems]);

  // Navigation helpers
  const setSelectedKey = useCallback(
    (key: string) =>
      void navigate({
        to: "/agents/$agentId/automations",
        params: { agentId },
        search: (prev: Record<string, unknown>) => ({ ...prev, item: key }),
        replace: true,
      }),
    [agentId, navigate],
  );

  const setSearch = useCallback(
    (q: string) =>
      void navigate({
        to: "/agents/$agentId/automations",
        params: { agentId },
        search: (prev: Record<string, unknown>) => ({
          ...prev,
          q: q || undefined,
        }),
        replace: true,
      }),
    [agentId, navigate],
  );

  const handleNew = useCallback(() => setSelectedKey(NEW_SCHEDULE_KEY), [setSelectedKey]);

  const handleScheduleCreated = useCallback(
    (jobId: string) => setSelectedKey(`schedule:${jobId}`),
    [setSelectedKey],
  );

  const handleScheduleDeleted = useCallback(
    () =>
      void navigate({
        to: "/agents/$agentId/automations",
        params: { agentId },
        search: (prev: Record<string, unknown>) => {
          const { item: _, ...rest } = prev;
          return rest;
        },
        replace: true,
      }),
    [agentId, navigate],
  );

  // Header
  useEffect(() => {
    const totalCount = allItems.length;
    setHeaderTitle(
      <div className="min-w-0">
        <div className="truncate font-mono text-[10px] font-semibold uppercase tracking-[0.14em] text-muted-foreground/70">
          {t("hub.title")}
        </div>
        <h1 className="truncate text-[15px] font-semibold tracking-[-0.01em]">{t("hub.title")}</h1>
      </div>,
    );
    setHeaderActions(
      <span className="font-mono text-xs text-muted-foreground">{totalCount} items</span>,
    );
    return () => {
      setHeaderTitle(null);
      setHeaderActions(null);
    };
  }, [allItems.length, setHeaderTitle, setHeaderActions, t]);

  return (
    <div className="flex h-full min-h-0 overflow-hidden bg-background">
      <ListPanel
        sections={sections}
        selectedKey={selectedKey}
        searchText={searchText}
        onSearch={setSearch}
        onSelect={setSelectedKey}
        onNew={handleNew}
      />
      <DetailPanel
        item={selectedItem}
        agentId={agentId}
        isNewSchedule={isNewSchedule}
        onScheduleCreated={handleScheduleCreated}
        onScheduleDeleted={handleScheduleDeleted}
      />
    </div>
  );
}
