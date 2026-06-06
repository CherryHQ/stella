import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { StatusDot, statusLabel } from "./lib";
import type { AutomationItem, Section } from "./types";
import { itemKey, itemName, itemUpdatedAt, jobScheduleText } from "./types";
import type { MessageKey } from "@/lib/i18n/messages";

const SECTIONS: { key: Section; labelKey: MessageKey; dotClass: string }[] = [
  {
    key: "needs-you",
    labelKey: "hub.secNeedsYou",
    dotClass: "bg-violet-500 shadow-[0_0_6px_rgba(139,92,246,0.5)]",
  },
  {
    key: "active",
    labelKey: "hub.secActive",
    dotClass: "bg-emerald-500 shadow-[0_0_6px_rgba(52,211,153,0.4)]",
  },
  { key: "schedules", labelKey: "hub.secSchedules", dotClass: "bg-blue-500" },
  { key: "closed", labelKey: "hub.secClosed", dotClass: "bg-muted-foreground" },
];

interface ListPanelProps {
  sections: Record<Section, AutomationItem[]>;
  selectedKey: string | undefined;
  searchText: string;
  onSearch: (q: string) => void;
  onSelect: (key: string) => void;
  onNew: () => void;
}

export function ListPanel({
  sections,
  selectedKey,
  searchText,
  onSearch,
  onSelect,
  onNew,
}: ListPanelProps) {
  const { t } = useI18n();

  return (
    <div className="flex w-[300px] min-w-[300px] shrink-0 flex-col overflow-hidden border-r border-border bg-background">
      {/* Header with search */}
      <div className="px-3.5 pt-3.5 pb-2.5">
        <h1 className="mb-2.5 font-serif text-base font-semibold tracking-tight">
          {t("hub.title")}
        </h1>
        <input
          type="search"
          value={searchText}
          onChange={(e) => onSearch(e.target.value)}
          placeholder={t("hub.searchPlaceholder")}
          className="w-full rounded-lg border border-input bg-card px-2.5 py-[7px] text-xs text-foreground outline-none transition-colors placeholder:text-muted-foreground focus:border-primary/40"
        />
      </div>

      {/* Scrollable list */}
      <div className="flex-1 overflow-y-auto">
        {SECTIONS.map(({ key, labelKey, dotClass }) => {
          const items = sections[key];
          if (!items.length) return null;
          return (
            <div key={key}>
              <div className="flex items-center gap-2 px-3.5 pt-3 pb-1.5">
                <span className={cn("inline-block size-1.5 rounded-full", dotClass)} />
                <span className="font-mono text-[10px] font-semibold uppercase tracking-[0.1em] text-muted-foreground">
                  {t(labelKey)}
                </span>
                <span className="ml-auto rounded-md bg-muted px-1.5 py-px font-mono text-[10px] text-muted-foreground">
                  {items.length}
                </span>
              </div>
              {items.map((item) => (
                <ListItem
                  key={itemKey(item)}
                  item={item}
                  selected={selectedKey === itemKey(item)}
                  onSelect={onSelect}
                />
              ))}
            </div>
          );
        })}

        {Object.values(sections).every((s) => s.length === 0) && (
          <div className="flex flex-col items-center justify-center py-16 text-center">
            <p className="text-sm font-medium text-foreground/70">{t("hub.emptyAll")}</p>
            <p className="mt-1 max-w-[200px] text-xs text-muted-foreground">
              {t("hub.emptyAllDesc")}
            </p>
          </div>
        )}
      </div>

      {/* Footer */}
      <div className="border-t border-border px-3.5 py-2.5">
        <button
          type="button"
          onClick={onNew}
          className="flex w-full items-center justify-center gap-1.5 rounded-lg border border-dashed border-border px-2 py-[7px] text-xs font-medium text-muted-foreground transition-colors hover:border-primary/40 hover:text-primary"
        >
          {t("hub.newAutomation")}
        </button>
      </div>
    </div>
  );
}

function ListItem({
  item,
  selected,
  onSelect,
}: {
  item: AutomationItem;
  selected: boolean;
  onSelect: (key: string) => void;
}) {
  const { t } = useI18n();
  const key = itemKey(item);
  const name = itemName(item);
  const time = itemUpdatedAt(item);
  const isClosed = item.kind !== "schedule" && ["done", "cancelled"].includes(item.data.status);
  const isDisabled = item.kind === "schedule" && !item.data.enabled;

  const needsAttention =
    (item.kind === "goal" &&
      (item.data.status === "reviewing" || item.data.status === "blocked")) ||
    (item.kind === "task" &&
      (item.data.status === "failed" ||
        item.data.status === "blocked" ||
        item.data.status === "reviewing"));

  const statusForDot =
    item.kind === "schedule" ? (item.data.enabled ? "running" : "draft") : item.data.status;

  const subtitle = buildSubtitle(t, item);

  return (
    <button
      type="button"
      onClick={() => onSelect(key)}
      className={cn(
        "flex w-full items-center gap-2.5 border-l-2 px-3.5 py-2.5 text-left transition-colors",
        selected ? "border-l-primary bg-primary/[0.06]" : "border-l-transparent hover:bg-muted/40",
        (isClosed || isDisabled) && !selected && "opacity-50 hover:opacity-75",
      )}
    >
      <StatusDot status={statusForDot} />
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13px] font-medium leading-snug">{name}</div>
        <div className="mt-0.5 flex items-center gap-1 truncate text-[11px] text-muted-foreground">
          <TypeChip kind={item.kind} />
          <span className="truncate">{subtitle}</span>
        </div>
      </div>
      <div className="flex shrink-0 flex-col items-end gap-1">
        <span className="font-mono text-[10px] text-muted-foreground">
          {time ? formatTime(time) : "—"}
        </span>
        {needsAttention && <span className="size-[6px] rounded-full bg-destructive" />}
      </div>
    </button>
  );
}

function TypeChip({ kind }: { kind: AutomationItem["kind"] }) {
  const { t } = useI18n();
  const config = {
    goal: { label: t("hub.chipGoal"), cls: "bg-violet-500/10 text-violet-500/70" },
    schedule: { label: t("hub.chipSchedule"), cls: "bg-blue-500/10 text-blue-500/70" },
    task: { label: t("hub.chipTask"), cls: "bg-emerald-500/10 text-emerald-500/70" },
  }[kind];

  return (
    <span className={cn("rounded px-1 py-px font-mono text-[9px] font-semibold", config.cls)}>
      {config.label}
    </span>
  );
}

function buildSubtitle(t: ReturnType<typeof useI18n>["t"], item: AutomationItem): string {
  if (item.kind === "goal") {
    return statusLabel(t, item.data.status);
  }
  if (item.kind === "schedule") {
    const sched = jobScheduleText(item.data);
    const last = item.data.last_error
      ? "last: error"
      : item.data.last_run_at
        ? "last: success"
        : "";
    return [sched, last].filter(Boolean).join(" · ");
  }
  // task
  const parts: string[] = [statusLabel(t, item.data.status)];
  if (item.data.retry_count > 0) {
    parts.push(`${item.data.retry_count}/${item.data.max_retries}`);
  }
  return parts.join(" · ");
}
