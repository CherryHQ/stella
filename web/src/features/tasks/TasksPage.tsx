import { useCallback, useEffect, useState } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { api } from "@/lib/api";
import { formatTime } from "@/lib/time";
import type { ComponentsAgentTask } from "@/lib/api-client/types.gen";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Dialog, DialogPopup, DialogTitle } from "@/components/ui/dialog";
import { useI18n } from "@/lib/i18n";
import { TaskDetail } from "./TaskDetail";

type TaskStatus =
  | "all"
  | "pending"
  | "running"
  | "blocked"
  | "review_requested"
  | "done"
  | "failed"
  | "cancelled";

interface TaskForm {
  title: string;
  description: string;
  priority: "routine" | "urgent";
}

const emptyForm = (): TaskForm => ({
  title: "",
  description: "",
  priority: "routine",
});

function taskStatusVariant(status: string): "success" | "error" | "warning" | "outline" {
  if (status === "done") return "success";
  if (status === "failed" || status === "review_requested") return "error";
  if (status === "running" || status === "blocked") return "warning";
  return "outline";
}

interface Toast {
  msg: string;
  kind: "success" | "error";
}
interface ConfirmState {
  msg: string;
  action: () => void;
}

const STATUS_TABS: TaskStatus[] = [
  "all",
  "pending",
  "running",
  "blocked",
  "review_requested",
  "done",
  "failed",
  "cancelled",
];

interface TasksPageProps {
  taskId?: string;
}

export function TasksPage({ taskId }: TasksPageProps) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const routeParams = useParams({ strict: false }) as { taskId?: string };
  const routeTaskId = taskId ?? routeParams.taskId;
  const [tasks, setTasks] = useState<ComponentsAgentTask[]>([]);
  const [statusFilter, setStatusFilter] = useState<TaskStatus>("all");
  const [detailTask, setDetailTask] = useState<ComponentsAgentTask | null>(null);
  const [creatingNew, setCreatingNew] = useState(false);
  const [taskForm, setTaskForm] = useState<TaskForm>(emptyForm());
  const [toast, setToast] = useState<Toast | null>(null);
  const [confirm, setConfirm] = useState<ConfirmState | null>(null);

  const showToast = useCallback((msg: string, kind: "success" | "error" = "success") => {
    setToast({ msg, kind });
    setTimeout(() => setToast(null), 3000);
  }, []);

  const loadTasks = useCallback(async (status?: string) => {
    try {
      const qs = status && status !== "all" ? "?status=" + status : "";
      const list = await api<{ items: ComponentsAgentTask[] }>("GET", "/api/tasks" + qs);
      setTasks(list.items || []);
    } catch (e) {
      console.error(e);
    }
  }, []);

  useEffect(() => {
    void loadTasks(statusFilter);
  }, [loadTasks, statusFilter]);

  const createTask = useCallback(async () => {
    try {
      const created = await api<ComponentsAgentTask>("POST", "/api/tasks", {
        title: taskForm.title,
        description: taskForm.description || undefined,
        priority: taskForm.priority,
      });
      setCreatingNew(false);
      setTaskForm(emptyForm());
      setDetailTask(created);
      await navigate({ to: "/tasks/$taskId", params: { taskId: created.id } });
      await loadTasks(statusFilter);
      showToast(t("tasks.created"));
    } catch (e) {
      showToast(e instanceof Error ? e.message : "Request failed", "error");
    }
  }, [taskForm, statusFilter, navigate, loadTasks, showToast, t]);

  const doDeleteTask = useCallback(
    async (id: string) => {
      try {
        await api("DELETE", "/api/tasks/" + id);
        if (routeTaskId === id) {
          setDetailTask(null);
          await navigate({ to: "/tasks" });
        }
        await loadTasks(statusFilter);
        showToast(t("tasks.deleted"));
      } catch (e) {
        showToast(e instanceof Error ? e.message : "Request failed", "error");
      }
    },
    [routeTaskId, statusFilter, navigate, loadTasks, showToast, t],
  );

  const handleTaskAction = useCallback((updated: ComponentsAgentTask) => {
    setTasks((prev) => prev.map((t) => (t.id === updated.id ? updated : t)));
    setDetailTask((prev) => (prev?.id === updated.id ? updated : prev));
  }, []);

  const selectedTask = routeTaskId
    ? (tasks.find((t) => t.id === routeTaskId) ??
      (detailTask?.id === routeTaskId ? detailTask : null))
    : null;

  useEffect(() => {
    if (routeTaskId) setCreatingNew(false);
  }, [routeTaskId]);

  useEffect(() => {
    if (
      !routeTaskId ||
      detailTask?.id === routeTaskId ||
      tasks.some((task) => task.id === routeTaskId)
    ) {
      return;
    }

    api<ComponentsAgentTask>("GET", "/api/tasks/" + encodeURIComponent(routeTaskId))
      .then(setDetailTask)
      .catch((e) => console.error(e));
  }, [routeTaskId, detailTask?.id, tasks]);

  return (
    <div className="flex overflow-hidden" style={{ height: "calc(100vh - 3.5rem)" }}>
      {/* Left panel: task list */}
      <div className="w-[320px] min-w-[320px] shrink-0 border-r border-border bg-background flex flex-col overflow-hidden">
        <div className="px-4 py-3 border-b border-border flex items-center justify-between">
          <span className="text-[9px] font-mono uppercase tracking-wider text-muted-foreground">
            {t("tasks.title")}
          </span>
          <Button
            size="xs"
            onClick={() => {
              setCreatingNew(true);
              setDetailTask(null);
              setTaskForm(emptyForm());
              void navigate({ to: "/tasks" });
            }}
          >
            + {t("tasks.new")}
          </Button>
        </div>

        {/* Status filter tabs */}
        <div className="px-2 py-2 border-b border-border flex flex-wrap gap-1">
          {STATUS_TABS.map((s) => (
            <button
              key={s}
              type="button"
              onClick={() => setStatusFilter(s)}
              className={`text-[10px] font-mono px-2 py-0.5 rounded-full transition-colors ${
                statusFilter === s
                  ? "bg-foreground text-background"
                  : "text-muted-foreground hover:text-foreground hover:bg-muted"
              }`}
            >
              {s === "review_requested" ? "review" : s}
            </button>
          ))}
        </div>

        <div className="flex-1 overflow-y-auto">
          {tasks.map((task) => (
            <div
              key={task.id}
              onClick={() => {
                setCreatingNew(false);
                void navigate({ to: "/tasks/$taskId", params: { taskId: task.id } });
              }}
              className={`px-4 py-3 border-b border-border/50 cursor-pointer transition-colors border-l-2 ${
                routeTaskId === task.id
                  ? "border-l-primary bg-primary/[0.03]"
                  : "border-l-transparent hover:bg-muted/50"
              }`}
            >
              <div className="flex items-center justify-between gap-2 mb-1">
                <span className="text-[13px] font-medium truncate">{task.title}</span>
                <Badge size="sm" variant={taskStatusVariant(task.status)}>
                  {task.status}
                </Badge>
              </div>
              <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
                <Badge size="sm" variant={task.priority === "urgent" ? "error" : "outline"}>
                  {task.priority}
                </Badge>
                <span className="text-muted-foreground/60 ml-auto">
                  {formatTime(task.updated_at)}
                </span>
              </div>
            </div>
          ))}
          {tasks.length === 0 && (
            <div className="py-12 text-center">
              <p className="text-sm text-muted-foreground">{t("tasks.noTasks")}</p>
              <p className="text-xs text-muted-foreground/60 mt-1">{t("tasks.noTasksDesc")}</p>
            </div>
          )}
        </div>
      </div>

      {/* Right panel: detail / create form */}
      <div className="flex-1 overflow-y-auto p-8 px-10">
        {selectedTask ? (
          <>
            <div className="flex items-center justify-between mb-6">
              <div />
              <Button
                variant="ghost"
                size="sm"
                className="text-destructive hover:text-destructive"
                onClick={() =>
                  setConfirm({
                    msg: t("tasks.deleteConfirm"),
                    action: () => doDeleteTask(selectedTask.id),
                  })
                }
              >
                {t("common.delete")}
              </Button>
            </div>
            <TaskDetail task={selectedTask} onAction={handleTaskAction} onToast={showToast} />
          </>
        ) : creatingNew ? (
          <div>
            <h2 className="font-serif text-xl tracking-tight mb-6">{t("tasks.new")}</h2>
            <div className="space-y-4 max-w-lg">
              <div className="space-y-1.5">
                <label className="block text-[10px] font-mono text-muted-foreground uppercase tracking-wider">
                  Title
                </label>
                <Input
                  type="text"
                  value={taskForm.title}
                  onChange={(e) => setTaskForm((f) => ({ ...f, title: e.target.value }))}
                  placeholder="Task title"
                  nativeInput
                />
              </div>
              <div className="space-y-1.5">
                <label className="block text-[10px] font-mono text-muted-foreground uppercase tracking-wider">
                  Description
                </label>
                <Textarea
                  value={taskForm.description}
                  onChange={(e) => setTaskForm((f) => ({ ...f, description: e.target.value }))}
                  placeholder="Describe what the agent should do…"
                />
              </div>
              <div className="space-y-1.5">
                <label className="block text-[10px] font-mono text-muted-foreground uppercase tracking-wider">
                  Priority
                </label>
                <select
                  value={taskForm.priority}
                  onChange={(e) =>
                    setTaskForm((f) => ({
                      ...f,
                      priority: e.target.value as "routine" | "urgent",
                    }))
                  }
                  className="h-8.5 w-full rounded-lg border border-input bg-background px-3 text-sm text-foreground outline-none focus:ring-2 focus:ring-ring"
                >
                  <option value="routine">Routine</option>
                  <option value="urgent">Urgent</option>
                </select>
              </div>
              <div className="flex items-center gap-3 pt-2">
                <Button size="sm" disabled={!taskForm.title.trim()} onClick={createTask}>
                  {t("common.create")}
                </Button>
                <Button variant="outline" size="sm" onClick={() => setCreatingNew(false)}>
                  {t("common.cancel")}
                </Button>
              </div>
            </div>
          </div>
        ) : (
          <div className="flex items-center justify-center h-full text-muted-foreground text-sm">
            Select a task or create a new one
          </div>
        )}
      </div>

      {/* Confirm dialog */}
      <Dialog
        open={!!confirm}
        onOpenChange={(open) => {
          if (!open) setConfirm(null);
        }}
      >
        <DialogPopup className="max-w-sm">
          <DialogTitle>{confirm?.msg}</DialogTitle>
          <div className="flex justify-end gap-2 mt-4">
            <Button variant="outline" size="sm" onClick={() => setConfirm(null)}>
              {t("common.cancel")}
            </Button>
            <Button
              variant="destructive"
              size="sm"
              onClick={() => {
                confirm?.action();
                setConfirm(null);
              }}
            >
              {t("common.delete")}
            </Button>
          </div>
        </DialogPopup>
      </Dialog>

      {/* Toast */}
      {toast && (
        <div
          className={`fixed bottom-4 right-4 z-50 rounded-lg border px-4 py-3 text-sm shadow-md max-w-sm ${
            toast.kind === "error"
              ? "border-destructive/36 bg-destructive/8 text-destructive-foreground"
              : "border-success/36 bg-success/8 text-success-foreground"
          }`}
        >
          {toast.msg}
        </div>
      )}
    </div>
  );
}
