import { useCallback, useEffect, useState } from "react";
import type { ComponentsReadiness, ComponentsTask } from "@/lib/api-client/types.gen";
import { getTaskReadiness } from "@/lib/api-client";
import { fetchAllTasks } from "@/lib/paginated";
import { cn } from "@/lib/utils";
import { formatTime } from "@/lib/time";
import { Button } from "@/components/ui/button";
import { SessionConversation } from "@/features/sessions/SessionConversation";
import { TaskPanel } from "./TaskPanel";

interface Props {
  agentId: string;
  selectedTaskId?: string;
  onSelectTask?: (taskId: string | null) => void;
  onOpenTaskSession: (sessionId: string) => void;
}

type Filter = "all" | "active" | "done";

const FILTERS: { key: Filter; label: string }[] = [
  { key: "all", label: "All" },
  { key: "active", label: "Active" },
  { key: "done", label: "Done" },
];

function matchesFilter(task: ComponentsTask, filter: Filter): boolean {
  if (filter === "all") return true;
  if (filter === "active")
    return (
      task.status === "draft" ||
      task.status === "ready" ||
      task.status === "running" ||
      task.status === "blocked" ||
      task.status === "reviewing"
    );
  return task.status === "done" || task.status === "cancelled" || task.status === "failed";
}

export function TaskBoardPanel({
  agentId,
  selectedTaskId,
  onSelectTask,
  onOpenTaskSession,
}: Props) {
  const [tasks, setTasks] = useState<ComponentsTask[]>([]);
  const [loading, setLoading] = useState(true);
  const [filter, setFilter] = useState<Filter>("all");
  const [showCreate, setShowCreate] = useState(false);

  const selectedTask = selectedTaskId ? (tasks.find((t) => t.id === selectedTaskId) ?? null) : null;

  const hasDetail = selectedTask !== null || showCreate;
  const [mobileView, setMobileView] = useState<"list" | "detail">(hasDetail ? "detail" : "list");

  useEffect(() => {
    setMobileView(hasDetail ? "detail" : "list");
  }, [hasDetail]);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setTasks(await fetchAllTasks(agentId));
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  }, [agentId]);

  useEffect(() => {
    void load();
  }, [load]);

  const filtered = tasks
    .filter((t) => matchesFilter(t, filter))
    .sort((a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime());

  return (
    <div className="flex h-full min-h-0 overflow-hidden bg-background">
      {/* List panel */}
      <div
        className={`${mobileView === "list" ? "flex" : "hidden"} w-full shrink-0 flex-col overflow-hidden border-r border-border bg-card/70 md:flex md:w-[380px]`}
      >
        {/* Header */}
        <div className="flex min-h-12 items-center justify-between gap-3 border-b border-border px-4 py-3">
          <span className="font-mono text-[10px] font-semibold text-muted-foreground">Tasks</span>
          <Button
            onClick={() => {
              onSelectTask?.(null);
              setShowCreate(true);
            }}
            variant="ghost"
            size="xs"
            className="text-primary font-medium"
          >
            + New Task
          </Button>
        </div>

        {/* Segmented filter */}
        <div className="flex gap-1 px-4 py-2.5 border-b border-border">
          {FILTERS.map((f) => (
            <button
              key={f.key}
              onClick={() => setFilter(f.key)}
              className={cn(
                "flex-1 rounded-full px-2.5 py-1 text-xs font-mono transition-colors duration-150",
                filter === f.key
                  ? "bg-foreground text-background font-medium"
                  : "bg-muted text-muted-foreground hover:bg-muted/80 hover:text-foreground",
              )}
            >
              {f.label}
            </button>
          ))}
        </div>

        {/* Task list */}
        <div className="flex-1 overflow-y-auto">
          {loading ? (
            <div className="flex items-center justify-center py-12">
              <div className="w-4 h-4 border-2 border-muted-foreground/30 border-t-muted-foreground rounded-full animate-spin" />
            </div>
          ) : filtered.length === 0 ? (
            <p className="text-center text-sm text-muted-foreground py-12">No tasks</p>
          ) : (
            <div className="space-y-1 p-2">
              {filtered.map((task) => (
                <button
                  key={task.id}
                  type="button"
                  onClick={() => onSelectTask?.(task.id)}
                  className={cn(
                    "w-full rounded-xl px-3 py-2.5 text-left transition-colors",
                    selectedTask?.id === task.id
                      ? "bg-accent text-accent-foreground"
                      : "text-foreground hover:bg-foreground/[0.045] hover:text-foreground",
                  )}
                >
                  <div className="flex items-start gap-2.5">
                    <StatusDot status={task.status} className="mt-1.5 shrink-0" />
                    <div className="min-w-0 flex-1">
                      <div className="flex items-baseline justify-between gap-2">
                        <span className="text-sm font-medium truncate">{task.title}</span>
                        <span className="text-[10px] font-mono text-muted-foreground shrink-0">
                          {formatTime(task.updated_at)}
                        </span>
                      </div>
                      {task.description && (
                        <p className="text-xs text-muted-foreground truncate mt-0.5">
                          {task.description}
                        </p>
                      )}
                    </div>
                  </div>
                </button>
              ))}
            </div>
          )}
        </div>
      </div>

      {/* Detail panel */}
      <div
        className={`${mobileView === "detail" ? "flex" : "hidden"} flex-1 flex-col overflow-hidden bg-background md:flex`}
      >
        {showCreate ? (
          <div className="flex h-full flex-col overflow-hidden">
            <div className="flex shrink-0 items-center justify-between border-b border-border bg-card/85 px-4 py-2">
              <button
                onClick={() => setShowCreate(false)}
                className="flex items-center gap-1 text-sm text-primary hover:text-primary/80 md:hidden"
              >
                <ChevronLeft />
                Tasks
              </button>
              <div className="hidden md:block" />
              <button
                onClick={() => setShowCreate(false)}
                className="hidden md:block text-muted-foreground hover:text-foreground text-lg leading-none"
              >
                ×
              </button>
            </div>
            <TaskPanel
              agentId={agentId}
              onCreated={(task) => {
                setShowCreate(false);
                void load();
                if (task.id) onSelectTask?.(task.id);
              }}
            />
          </div>
        ) : selectedTask ? (
          <TaskDetail
            agentId={agentId}
            task={selectedTask}
            onBack={() => onSelectTask?.(null)}
            onOpenSession={() => {
              if (selectedTask.session_id) onOpenTaskSession(selectedTask.session_id);
            }}
          />
        ) : (
          <div className="flex h-full items-center justify-center">
            <p className="text-sm text-muted-foreground">Select a task to view details.</p>
          </div>
        )}
      </div>
    </div>
  );
}

function TaskDetail({
  agentId,
  task,
  onBack,
  onOpenSession,
}: {
  agentId: string;
  task: ComponentsTask;
  onBack: () => void;
  onOpenSession: () => void;
}) {
  const [readiness, setReadiness] = useState<ComponentsReadiness | null>(null);
  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const { data } = await getTaskReadiness({ path: { taskId: task.id }, throwOnError: true });
        if (!cancelled) setReadiness(data ?? null);
      } catch (e) {
        console.error(e);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [task.id, task.updated_at]);
  return (
    <div className="flex h-full flex-col overflow-hidden">
      {/* Mobile back + Open Session header */}
      <div className="flex shrink-0 items-center justify-between border-b border-border bg-card/85 px-4 py-2">
        <button
          onClick={onBack}
          className="flex items-center gap-1 text-sm text-primary hover:text-primary/80 md:hidden"
        >
          <ChevronLeft />
          Tasks
        </button>
        <div className="hidden md:block" />
        <div className="flex items-center gap-2">
          {task.session_id && (
            <Button
              variant="ghost"
              size="xs"
              onClick={onOpenSession}
              className="text-primary font-medium"
            >
              Open Session
            </Button>
          )}
          <button
            onClick={onBack}
            className="hidden md:block text-muted-foreground hover:text-foreground text-lg leading-none"
          >
            ×
          </button>
        </div>
      </div>

      {/* Task info */}
      <div className="shrink-0 overflow-y-auto border-b border-border px-5 py-4 space-y-3">
        {/* Status + Title */}
        <div>
          <div className="flex items-center gap-1.5 mb-1.5">
            <StatusDot status={task.status} />
            <span className={cn("text-xs capitalize", statusColor(task.status))}>
              {formatStatus(task.status)}
            </span>
          </div>
          <h2 className="font-serif text-xl tracking-tight">{task.title}</h2>
          {task.description && (
            <p className="text-sm text-muted-foreground mt-1 leading-relaxed">{task.description}</p>
          )}
        </div>

        {/* Property rows */}
        <div className="divide-y divide-border">
          <PropertyRow
            label="Priority"
            value={task.priority}
            highlight={task.priority === "urgent"}
          />
          {readiness && <PropertyRow label="Readiness" value={readiness.state} />}
          <PropertyRow label="Updated" value={formatTime(task.updated_at)} />
          <PropertyRow label="Created" value={formatTime(task.created_at)} />
        </div>
      </div>

      {/* Conversation */}
      <div className="flex-1 min-h-0 flex flex-col overflow-hidden">
        {task.session_id ? (
          <SessionConversation
            agentId={agentId}
            sessionId={task.session_id}
            placeholder="Ask about this task..."
            className="h-full"
            bodyClassName="min-h-0 flex-1"
            inline
          />
        ) : (
          <div className="flex-1 flex items-center justify-center px-4">
            <p className="text-sm text-muted-foreground text-center">No conversation session</p>
          </div>
        )}
      </div>
    </div>
  );
}

function PropertyRow({
  label,
  value,
  highlight,
}: {
  label: string;
  value: string;
  highlight?: boolean;
}) {
  return (
    <div className="flex items-center justify-between py-2">
      <span className="text-sm text-muted-foreground">{label}</span>
      <span
        className={cn(
          "text-sm",
          highlight ? "text-destructive font-medium" : "text-muted-foreground",
        )}
      >
        {value}
      </span>
    </div>
  );
}

function StatusDot({ status, className }: { status: string; className?: string }) {
  return (
    <span
      className={cn(
        "w-2 h-2 rounded-full shrink-0",
        status === "done" && "bg-emerald-500",
        status === "running" && "bg-blue-500",
        (status === "draft" || status === "ready") && "bg-muted-foreground/30",
        status === "failed" && "bg-destructive",
        (status === "blocked" || status === "reviewing") && "bg-amber-500",
        status === "cancelled" && "bg-muted-foreground/20",
        className,
      )}
    />
  );
}

function ChevronLeft() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      className="w-4 h-4"
    >
      <path strokeLinecap="round" strokeLinejoin="round" d="M15.75 19.5 8.25 12l7.5-7.5" />
    </svg>
  );
}

function statusColor(status: string): string {
  if (status === "done") return "text-emerald-600";
  if (status === "running") return "text-blue-600";
  if (status === "failed") return "text-destructive";
  if (status === "blocked" || status === "reviewing") return "text-amber-600";
  return "text-muted-foreground";
}

function formatStatus(status: string): string {
  if (status === "reviewing") return "Reviewing";
  return status;
}
