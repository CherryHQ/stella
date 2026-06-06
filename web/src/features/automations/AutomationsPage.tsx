import { useCallback, useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useNavigate, useParams } from "@tanstack/react-router";
import { goalsOptions } from "@/lib/queries/goals";
import { agentSchedulerJobsOptions } from "@/lib/queries/agents";
import { standaloneTasksOptions } from "./queries";
import { useI18n } from "@/lib/i18n";
import { useAppShell } from "@/layouts/AppShell";
import { ListPanel } from "./ListPanel";
import { DetailPanel } from "./DetailPanel";
import type { AutomationItem, ItemKind } from "./types";
import { classifyAll, itemKey } from "./types";
import type { SchedulerJob } from "@/lib/types";

interface AutomationsPageProps {
  selectedKind?: ItemKind;
  selectedId?: string;
}

export function AutomationsPage({ selectedKind, selectedId }: AutomationsPageProps = {}) {
  const { t } = useI18n();
  const { agentId } = useParams({ strict: false }) as { agentId: string };
  const navigate = useNavigate();
  const { setHeaderTitle, setHeaderActions } = useAppShell();

  const [searchText, setSearchText] = useState("");
  const [isNewSchedule, setIsNewSchedule] = useState(false);

  const { data: goals = [] } = useQuery(goalsOptions(agentId));
  const { data: jobs = [] } = useQuery(agentSchedulerJobsOptions(agentId));
  const { data: tasks = [] } = useQuery(standaloneTasksOptions(agentId));

  const allItems: AutomationItem[] = useMemo(() => {
    const items: AutomationItem[] = [];
    for (const g of goals) items.push({ kind: "goal", id: g.id, data: g });
    for (const j of jobs as SchedulerJob[]) items.push({ kind: "schedule", id: j.id, data: j });
    for (const t of tasks) items.push({ kind: "task", id: t.id, data: t });
    return items;
  }, [goals, jobs, tasks]);

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

  const sections = useMemo(() => classifyAll(filtered), [filtered]);

  const selectedItem = useMemo(() => {
    if (!selectedKind || !selectedId) return null;
    return allItems.find((item) => item.kind === selectedKind && item.id === selectedId) ?? null;
  }, [selectedKind, selectedId, allItems]);

  const selectedKey = selectedItem ? itemKey(selectedItem) : undefined;

  const pathForItem = useCallback(
    (kind: ItemKind, id: string) => {
      const base = `/agents/${agentId}/automations`;
      switch (kind) {
        case "goal":
          return `${base}/goals/${id}`;
        case "schedule":
          return `${base}/schedules/${id}`;
        case "task":
          return `${base}/tasks/${id}`;
      }
    },
    [agentId],
  );

  const handleSelect = useCallback(
    (key: string) => {
      setIsNewSchedule(false);
      const [kind, ...rest] = key.split(":");
      const id = rest.join(":");
      void navigate({ to: pathForItem(kind as ItemKind, id) });
    },
    [navigate, pathForItem],
  );

  const handleNew = useCallback(() => setIsNewSchedule(true), []);

  const handleScheduleCreated = useCallback(
    (jobId: string) => {
      setIsNewSchedule(false);
      void navigate({ to: pathForItem("schedule", jobId) });
    },
    [navigate, pathForItem],
  );

  const handleScheduleDeleted = useCallback(() => {
    setIsNewSchedule(false);
    void navigate({ to: `/agents/${agentId}/automations` });
  }, [navigate, agentId]);

  useEffect(() => {
    setHeaderTitle(
      <div className="min-w-0">
        <div className="truncate font-mono text-[10px] font-semibold uppercase tracking-[0.14em] text-muted-foreground/70">
          {t("hub.title")}
        </div>
        <h1 className="truncate text-[15px] font-semibold tracking-[-0.01em]">{t("hub.title")}</h1>
      </div>,
    );
    setHeaderActions(
      <span className="font-mono text-xs text-muted-foreground">{allItems.length} items</span>,
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
        onSearch={setSearchText}
        onSelect={handleSelect}
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
