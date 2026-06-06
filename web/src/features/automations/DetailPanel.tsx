import { useI18n } from "@/lib/i18n";
import type { AutomationItem } from "./types";
import { GoalDetail } from "./detail/GoalDetail";
import { ScheduleDetail } from "./detail/ScheduleDetail";
import { TaskDetail } from "./detail/TaskDetail";

interface DetailPanelProps {
  item: AutomationItem | null;
  agentId: string;
  isNewSchedule: boolean;
  onScheduleCreated: (jobId: string) => void;
  onScheduleDeleted: () => void;
}

export function DetailPanel({
  item,
  agentId,
  isNewSchedule,
  onScheduleCreated,
  onScheduleDeleted,
}: DetailPanelProps) {
  const { t } = useI18n();

  if (isNewSchedule) {
    return (
      <div className="flex-1 overflow-y-auto">
        <ScheduleDetail job={null} agentId={agentId} mode="create" onCreated={onScheduleCreated} />
      </div>
    );
  }

  if (!item) {
    return (
      <div className="flex flex-1 flex-col items-center justify-center gap-1.5">
        <p className="text-sm font-medium text-foreground/70">{t("hub.emptySelect")}</p>
        <p className="text-xs text-muted-foreground">{t("hub.emptySelectDesc")}</p>
      </div>
    );
  }

  return (
    <div className="flex-1 overflow-y-auto">
      {item.kind === "goal" && <GoalDetail goal={item.data} />}
      {item.kind === "schedule" && (
        <ScheduleDetail
          job={item.data}
          agentId={agentId}
          mode={item.data.owner_kind === "user" ? "edit" : "readonly"}
          onDeleted={onScheduleDeleted}
        />
      )}
      {item.kind === "task" && <TaskDetail task={item.data} />}
    </div>
  );
}
