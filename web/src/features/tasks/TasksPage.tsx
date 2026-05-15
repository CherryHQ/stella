import { useCallback, useEffect, useState } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { api } from "@/lib/api";
import { formatTime } from "@/lib/time";
import type { ComponentsAgentTask } from "@/lib/api-client/types.gen";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import {
  Dialog,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogPopup,
  DialogTitle,
} from "@/components/ui/dialog";
import { useI18n } from "@/lib/i18n";
import { TaskDetail } from "./TaskDetail";

type MetricFilter = "all" | "running" | "attention" | "pending" | "done" | "failed";

interface TaskForm {
  title: string;
  description: string;
  priority: "routine" | "urgent";
}

const emptyForm = (): TaskForm => ({ title: "", description: "", priority: "routine" });

function statusVariant(status: string): "success" | "error" | "warning" | "outline" {
  if (status === "done") return "success";
  if (status === "failed" || status === "review_requested") return "error";
  if (status === "running" || status === "blocked") return "warning";
  return "outline";
}

const STATUS_ICON: Record<string, string> = {
  pending: "○",
  running: "◉",
  blocked: "⚠",
  review_requested: "⚑",
  done: "✓",
  failed: "✕",
  cancelled: "⊘",
};

interface Toast {
  msg: string;
  kind: "success" | "error";
}

interface ConfirmState {
  msg: string;
  action: () => void;
}

interface TasksPageProps {
  taskId?: string;
}

export function TasksPage({ taskId }: TasksPageProps) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const routeParams = useParams({ strict: false }) as { taskId?: string };
  const routeTaskId = taskId ?? routeParams.taskId;

  const [tasks, setTasks] = useState<ComponentsAgentTask[]>([]);
  const [metricFilter, setMetricFilter] = useState<MetricFilter>("all");
  const [detailTask, setDetailTask] = useState<ComponentsAgentTask | null>(null);
  const [creatingNew, setCreatingNew] = useState(false);
  const [taskForm, setTaskForm] = useState<TaskForm>(emptyForm());
  const [toast, setToast] = useState<Toast | null>(null);
  const [confirm, setConfirm] = useState<ConfirmState | null>(null);
  const [attentionRespond, setAttentionRespond] = useState<Record<string, string>>({});
  const [actioning, setActioning] = useState<string | null>(null);

  const showToast = useCallback((msg: string, kind: "success" | "error" = "success") => {
    setToast({ msg, kind });
    setTimeout(() => setToast(null), 3000);
  }, []);

  const loadTasks = useCallback(async () => {
    try {
      const list = await api<{ items: ComponentsAgentTask[] }>("GET", "/api/tasks");
      setTasks(list.items || []);
    } catch (e) {
      console.error(e);
    }
  }, []);

  useEffect(() => {
    void loadTasks();
  }, [loadTasks]);

  // Auto-refresh every 10s while tasks are active.
  useEffect(() => {
    const id = setInterval(() => void loadTasks(), 10_000);
    return () => clearInterval(id);
  }, [loadTasks]);

  // Load detail task when route has a taskId not already in list.
  useEffect(() => {
    if (!routeTaskId || detailTask?.id === routeTaskId || tasks.some((t) => t.id === routeTaskId))
      return;
    api<ComponentsAgentTask>("GET", "/api/tasks/" + encodeURIComponent(routeTaskId))
      .then(setDetailTask)
      .catch(console.error);
  }, [routeTaskId, detailTask?.id, tasks]);

  const selectedTask = routeTaskId
    ? (tasks.find((t) => t.id === routeTaskId) ??
      (detailTask?.id === routeTaskId ? detailTask : null))
    : null;

  const handleTaskAction = useCallback((updated: ComponentsAgentTask) => {
    setTasks((prev) => prev.map((t) => (t.id === updated.id ? updated : t)));
    setDetailTask((prev) => (prev?.id === updated.id ? updated : prev));
  }, []);

  const doAttentionAction = useCallback(
    async (taskId: string, type: "approve" | "reject" | "respond", message?: string) => {
      setActioning(taskId);
      try {
        const updated = await api<ComponentsAgentTask>("POST", `/api/tasks/${taskId}/action`, {
          type,
          message,
        });
        handleTaskAction(updated);
        setAttentionRespond((prev) => ({ ...prev, [taskId]: "" }));
        if (type === "approve") showToast(t("tasks.approved"));
        else if (type === "reject") showToast(t("tasks.rejected"));
        else showToast(t("tasks.responded"));
      } catch (e) {
        showToast(e instanceof Error ? e.message : "Request failed", "error");
      } finally {
        setActioning(null);
      }
    },
    [handleTaskAction, showToast, t],
  );

  const createTask = useCallback(async () => {
    try {
      const created = await api<ComponentsAgentTask>("POST", "/api/tasks", {
        title: taskForm.title,
        description: taskForm.description || undefined,
        priority: taskForm.priority,
      });
      setCreatingNew(false);
      setTaskForm(emptyForm());
      await navigate({ to: "/tasks/$taskId", params: { taskId: created.id } });
      await loadTasks();
      showToast(t("tasks.created"));
    } catch (e) {
      showToast(e instanceof Error ? e.message : "Request failed", "error");
    }
  }, [taskForm, navigate, loadTasks, showToast, t]);

  const doDeleteTask = useCallback(
    async (id: string) => {
      try {
        await api("DELETE", "/api/tasks/" + id);
        if (routeTaskId === id) await navigate({ to: "/tasks" });
        await loadTasks();
        showToast(t("tasks.deleted"));
      } catch (e) {
        showToast(e instanceof Error ? e.message : "Request failed", "error");
      }
    },
    [routeTaskId, navigate, loadTasks, showToast, t],
  );

  // Derived counts.
  const running = tasks.filter((t) => t.status === "running");
  const attention = tasks.filter((t) => t.status === "blocked" || t.status === "review_requested");
  const pending = tasks.filter((t) => t.status === "pending");
  const done = tasks.filter((t) => t.status === "done");
  const failed = tasks.filter((t) => t.status === "failed" || t.status === "cancelled");

  const filteredTasks = (() => {
    if (metricFilter === "running") return running;
    if (metricFilter === "attention") return attention;
    if (metricFilter === "pending") return pending;
    if (metricFilter === "done") return done;
    if (metricFilter === "failed") return failed;
    return tasks;
  })();

  const setFilter = (f: MetricFilter) => {
    setMetricFilter(f);
    void navigate({ to: "/tasks" });
  };

  const metrics: Array<{
    key: MetricFilter;
    label: string;
    count: number;
    alert?: boolean;
  }> = [
    { key: "all", label: t("tasks.metricTotal"), count: tasks.length },
    { key: "running", label: t("tasks.metricRunning"), count: running.length },
    { key: "attention", label: t("tasks.metricAttention"), count: attention.length, alert: true },
    { key: "pending", label: t("tasks.metricPending"), count: pending.length },
    { key: "done", label: t("tasks.metricDone"), count: done.length },
    { key: "failed", label: t("tasks.metricFailed"), count: failed.length },
  ];

  return (
    <div className="flex flex-col overflow-hidden" style={{ height: "calc(100vh - 3.5rem)" }}>
      {/* ── Header ── */}
      <div className="flex items-center justify-between px-5 py-3 border-b border-border bg-background shrink-0">
        <span className="text-[9px] font-mono uppercase tracking-wider text-muted-foreground">
          {t("tasks.title")}
        </span>
        <Button size="xs" onClick={() => setCreatingNew(true)}>
          + {t("tasks.new")}
        </Button>
      </div>

      {/* ── Metrics strip ── */}
      <div className="flex gap-2 px-5 py-3 border-b border-border bg-background shrink-0 overflow-x-auto">
        {metrics.map((m) => (
          <button
            key={m.key}
            type="button"
            onClick={() => setFilter(m.key)}
            className={[
              "flex flex-col items-start px-4 py-2.5 rounded-lg border transition-all shrink-0 min-w-[90px]",
              metricFilter === m.key
                ? "border-primary bg-primary/5"
                : m.alert && m.count > 0
                  ? "border-destructive/40 bg-destructive/[0.03] hover:bg-destructive/[0.06]"
                  : "border-border hover:bg-muted/40",
            ].join(" ")}
          >
            <span
              className={[
                "text-2xl font-semibold leading-none mb-1",
                metricFilter === m.key
                  ? "text-primary"
                  : m.alert && m.count > 0
                    ? "text-destructive"
                    : "text-foreground",
              ].join(" ")}
            >
              {m.count}
            </span>
            <span
              className={[
                "text-[10px] font-mono leading-none",
                m.alert && m.count > 0 ? "text-destructive/70" : "text-muted-foreground",
              ].join(" ")}
            >
              {m.label}
            </span>
          </button>
        ))}
      </div>

      {/* ── Body ── */}
      <div className="flex flex-1 overflow-hidden">
        {/* Main content */}
        <div className="flex-1 overflow-y-auto">
          {selectedTask ? (
            /* Task detail view */
            <div className="max-w-2xl mx-auto px-8 py-6">
              <div className="flex items-center justify-between mb-6">
                <button
                  type="button"
                  onClick={() => navigate({ to: "/tasks" })}
                  className="text-[11px] font-mono text-muted-foreground hover:text-foreground transition-colors flex items-center gap-1"
                >
                  ← {t("tasks.allTasks")}
                </button>
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
            </div>
          ) : (
            /* Task list */
            <div>
              {filteredTasks.map((task) => (
                <div
                  key={task.id}
                  onClick={() => navigate({ to: "/tasks/$taskId", params: { taskId: task.id } })}
                  className="flex items-center gap-3 px-5 py-3 border-b border-border/60 cursor-pointer hover:bg-muted/40 transition-colors group"
                >
                  <span
                    className={[
                      "text-[13px] shrink-0",
                      task.status === "done"
                        ? "text-success"
                        : task.status === "failed" || task.status === "cancelled"
                          ? "text-muted-foreground"
                          : task.status === "blocked" || task.status === "review_requested"
                            ? "text-destructive"
                            : task.status === "running"
                              ? "text-warning-foreground"
                              : "text-muted-foreground",
                    ].join(" ")}
                  >
                    {STATUS_ICON[task.status] ?? "○"}
                  </span>
                  <span className="flex-1 text-[13px] font-medium truncate group-hover:text-primary transition-colors">
                    {task.title}
                  </span>
                  <div className="flex items-center gap-2 shrink-0">
                    <Badge size="sm" variant={task.priority === "urgent" ? "error" : "outline"}>
                      {task.priority}
                    </Badge>
                    <Badge size="sm" variant={statusVariant(task.status)}>
                      {task.status === "review_requested" ? "review" : task.status}
                    </Badge>
                    <span className="text-[11px] text-muted-foreground w-16 text-right">
                      {formatTime(task.updated_at)}
                    </span>
                  </div>
                </div>
              ))}
              {filteredTasks.length === 0 && (
                <div className="py-16 text-center">
                  <p className="text-sm text-muted-foreground">{t("tasks.noTasks")}</p>
                  <p className="text-xs text-muted-foreground/60 mt-1">{t("tasks.noTasksDesc")}</p>
                </div>
              )}
            </div>
          )}
        </div>

        {/* ── Attention panel ── */}
        <div className="w-[280px] min-w-[280px] shrink-0 border-l border-border bg-background flex flex-col overflow-hidden">
          <div className="px-4 py-3 border-b border-border flex items-center gap-2 shrink-0">
            <span className="text-[9px] font-mono uppercase tracking-wider text-muted-foreground flex-1">
              {t("tasks.needsAttention")}
            </span>
            {attention.length > 0 && (
              <span className="text-[10px] font-mono px-1.5 py-0.5 rounded-full bg-destructive/10 text-destructive">
                {attention.length}
              </span>
            )}
          </div>
          <div className="flex-1 overflow-y-auto">
            {attention.length === 0 ? (
              <div className="py-10 text-center">
                <p className="text-xs text-muted-foreground/60">{t("tasks.allClear")}</p>
              </div>
            ) : (
              attention.map((task) => (
                <div key={task.id} className="px-4 py-3 border-b border-border/50">
                  <button
                    type="button"
                    onClick={() => navigate({ to: "/tasks/$taskId", params: { taskId: task.id } })}
                    className="text-[12.5px] font-medium text-left hover:text-primary transition-colors w-full truncate block mb-2"
                  >
                    {task.title}
                  </button>
                  <Badge
                    size="sm"
                    variant={task.status === "blocked" ? "warning" : "error"}
                    className="mb-3"
                  >
                    {task.status === "review_requested" ? "review" : task.status}
                  </Badge>

                  {task.status === "review_requested" && (
                    <div className="flex gap-1.5">
                      <Button
                        size="xs"
                        loading={actioning === task.id}
                        onClick={() => doAttentionAction(task.id, "approve")}
                      >
                        {t("tasks.approve")}
                      </Button>
                      <Button
                        size="xs"
                        variant="outline"
                        loading={actioning === task.id}
                        onClick={() => doAttentionAction(task.id, "reject")}
                      >
                        {t("tasks.reject")}
                      </Button>
                    </div>
                  )}

                  {task.status === "blocked" && (
                    <div className="space-y-1.5">
                      <Textarea
                        value={attentionRespond[task.id] ?? ""}
                        onChange={(e) =>
                          setAttentionRespond((prev) => ({ ...prev, [task.id]: e.target.value }))
                        }
                        placeholder={t("tasks.respondPlaceholder")}
                        className="text-[12px] min-h-[60px]"
                      />
                      <Button
                        size="xs"
                        loading={actioning === task.id}
                        disabled={!(attentionRespond[task.id] ?? "").trim()}
                        onClick={() =>
                          doAttentionAction(task.id, "respond", attentionRespond[task.id])
                        }
                      >
                        {t("common.submit")}
                      </Button>
                    </div>
                  )}
                </div>
              ))
            )}
          </div>
        </div>
      </div>

      {/* ── Create dialog ── */}
      <Dialog open={creatingNew} onOpenChange={(open) => !open && setCreatingNew(false)}>
        <DialogPopup className="max-w-md">
          <DialogHeader>
            <DialogTitle>{t("tasks.new")}</DialogTitle>
          </DialogHeader>
          <DialogPanel>
            <div className="space-y-4">
              <div className="space-y-1.5">
                <label className="block text-[10px] font-mono text-muted-foreground uppercase tracking-wider">
                  {t("tasks.fieldTitle")}
                </label>
                <Input
                  type="text"
                  value={taskForm.title}
                  onChange={(e) => setTaskForm((f) => ({ ...f, title: e.target.value }))}
                  placeholder={t("tasks.fieldTitlePlaceholder")}
                  nativeInput
                />
              </div>
              <div className="space-y-1.5">
                <label className="block text-[10px] font-mono text-muted-foreground uppercase tracking-wider">
                  {t("tasks.fieldDescription")}
                </label>
                <Textarea
                  value={taskForm.description}
                  onChange={(e) => setTaskForm((f) => ({ ...f, description: e.target.value }))}
                  placeholder={t("tasks.fieldDescriptionPlaceholder")}
                />
              </div>
              <div className="space-y-1.5">
                <label className="block text-[10px] font-mono text-muted-foreground uppercase tracking-wider">
                  {t("tasks.fieldPriority")}
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
                  <option value="routine">{t("tasks.priorityRoutine")}</option>
                  <option value="urgent">{t("tasks.priorityUrgent")}</option>
                </select>
              </div>
            </div>
          </DialogPanel>
          <DialogFooter variant="bare">
            <Button size="sm" disabled={!taskForm.title.trim()} onClick={createTask}>
              {t("common.create")}
            </Button>
            <Button variant="outline" size="sm" onClick={() => setCreatingNew(false)}>
              {t("common.cancel")}
            </Button>
          </DialogFooter>
        </DialogPopup>
      </Dialog>

      {/* ── Confirm dialog ── */}
      <Dialog open={!!confirm} onOpenChange={(open) => !open && setConfirm(null)}>
        <DialogPopup className="max-w-sm">
          <DialogHeader>
            <DialogTitle>{confirm?.msg}</DialogTitle>
          </DialogHeader>
          <DialogFooter variant="bare">
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
          </DialogFooter>
        </DialogPopup>
      </Dialog>

      {/* ── Toast ── */}
      {toast && (
        <div
          className={[
            "fixed bottom-4 right-4 z-50 rounded-lg border px-4 py-3 text-sm shadow-md max-w-sm",
            toast.kind === "error"
              ? "border-destructive/36 bg-destructive/8 text-destructive-foreground"
              : "border-success/36 bg-success/8 text-success-foreground",
          ].join(" ")}
        >
          {toast.msg}
        </div>
      )}
    </div>
  );
}
