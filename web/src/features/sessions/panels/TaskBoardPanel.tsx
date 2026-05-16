import { useCallback, useEffect, useState } from "react";
import type { ComponentsAgentTask } from "@/lib/api-client/types.gen";
import { api } from "@/lib/api";
import { cn } from "@/lib/utils";
import { formatTime } from "@/lib/time";
import { useI18n } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { SessionConversation } from "@/features/sessions/SessionConversation";
import { ResizableSidePanel } from "./ResizableSidePanel";

interface Props {
  agentId: string;
  selectedTaskId?: string;
  onSelectTask?: (taskId: string | null) => void;
  onOpenTaskSession: (sessionId: string) => void;
}

type Lane = "attention" | "running" | "pending" | "done" | "failed";

const LANES: { key: Lane; label: string; color: string }[] = [
  { key: "attention", label: "Needs Attention", color: "border-t-amber-400" },
  { key: "running", label: "Running", color: "border-t-blue-400" },
  { key: "pending", label: "Pending", color: "border-t-muted-foreground/30" },
  { key: "done", label: "Done", color: "border-t-emerald-400" },
  { key: "failed", label: "Failed", color: "border-t-destructive" },
];

function taskLane(task: ComponentsAgentTask): Lane {
  if (task.status === "review_requested" || task.status === "blocked") return "attention";
  if (task.status === "running") return "running";
  if (task.status === "pending") return "pending";
  if (task.status === "done" || task.status === "cancelled") return "done";
  if (task.status === "failed") return "failed";
  return "pending";
}

export function TaskBoardPanel({
  agentId,
  selectedTaskId,
  onSelectTask,
  onOpenTaskSession,
}: Props) {
  const { t } = useI18n();
  const [tasks, setTasks] = useState<ComponentsAgentTask[]>([]);
  const [loading, setLoading] = useState(true);
  const [tab, setTab] = useState<"board" | "timeline">("board");
  const [creating, setCreating] = useState(false);
  const [newTitle, setNewTitle] = useState("");

  const selectedTask = selectedTaskId ? (tasks.find((t) => t.id === selectedTaskId) ?? null) : null;

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await api<{ items: ComponentsAgentTask[] }>("GET", "/api/tasks");
      setTasks((res.items ?? []).filter((t) => !agentId || t.agent_id === agentId));
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  }, [agentId]);

  useEffect(() => {
    void load();
  }, [load]);

  const createTask = useCallback(async () => {
    if (!newTitle.trim()) return;
    try {
      await api("POST", "/api/tasks", {
        title: newTitle.trim(),
        agent_id: agentId,
        priority: "routine",
      });
      setNewTitle("");
      setCreating(false);
      void load();
    } catch (e) {
      console.error(e);
    }
  }, [newTitle, agentId, load]);

  const selectTask = useCallback(
    (task: ComponentsAgentTask) => {
      onSelectTask?.(task.id ?? null);
    },
    [onSelectTask],
  );

  const laneData = LANES.map((lane) => ({
    ...lane,
    tasks: tasks.filter((t) => taskLane(t) === lane.key),
  }));

  const sortedForTimeline = [...tasks].sort(
    (a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime(),
  );

  return (
    <div className="flex-1 min-w-0 flex overflow-hidden">
      {/* Main content */}
      <div className="flex-1 min-w-0 flex flex-col overflow-hidden">
        {/* Header */}
        <div className="flex-shrink-0 h-12 px-5 border-b border-border/60 bg-background flex items-center gap-3">
          <h2 className="text-[15px] font-medium tracking-tight">{t("sessions.sidebar.tasks")}</h2>
          <div className="flex items-center gap-1 ml-4">
            <button
              onClick={() => setTab("board")}
              className={cn(
                "px-3 py-1 rounded-lg text-xs font-medium transition-colors duration-150",
                tab === "board"
                  ? "bg-muted text-foreground"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              Board
            </button>
            <button
              onClick={() => setTab("timeline")}
              className={cn(
                "px-3 py-1 rounded-lg text-xs font-medium transition-colors duration-150",
                tab === "timeline"
                  ? "bg-muted text-foreground"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              Timeline
            </button>
          </div>
          <div className="ml-auto">
            {!creating ? (
              <Button
                size="sm"
                onClick={() => setCreating(true)}
                className="rounded-xl text-xs gap-1.5"
              >
                <svg
                  className="w-3 h-3"
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="2.5"
                >
                  <path d="M12 5v14M5 12h14" />
                </svg>
                New Task
              </Button>
            ) : (
              <div className="flex items-center gap-2">
                <input
                  autoFocus
                  value={newTitle}
                  onChange={(e) => setNewTitle(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") void createTask();
                    if (e.key === "Escape") setCreating(false);
                  }}
                  placeholder="Task title..."
                  className="text-xs px-3 py-1.5 rounded-lg border border-border bg-background focus:border-primary/50 focus:outline-none w-48"
                />
                <Button size="sm" onClick={() => void createTask()} className="rounded-xl text-xs">
                  Add
                </Button>
              </div>
            )}
          </div>
        </div>

        {/* Body */}
        {loading ? (
          <div className="flex-1 flex items-center justify-center">
            <div className="w-4 h-4 border-2 border-muted-foreground/30 border-t-muted-foreground rounded-full animate-spin" />
          </div>
        ) : tab === "board" ? (
          <div className="flex-1 overflow-x-auto p-4">
            <div className="flex gap-3 h-full min-w-[700px]">
              {laneData.map((lane) => (
                <div key={lane.key} className="flex-1 min-w-[140px] flex flex-col">
                  <div
                    className={cn(
                      "text-[10px] font-mono font-medium uppercase tracking-wider text-muted-foreground/70 pb-2 mb-2 border-t-2",
                      lane.color,
                    )}
                  >
                    {lane.label}
                    <span className="ml-1.5 text-muted-foreground/40">{lane.tasks.length}</span>
                  </div>
                  <div className="flex-1 flex flex-col gap-2 overflow-y-auto">
                    {lane.tasks.map((task) => (
                      <TaskCard
                        key={task.id}
                        task={task}
                        selected={selectedTask?.id === task.id}
                        onOpen={() => selectTask(task)}
                      />
                    ))}
                  </div>
                </div>
              ))}
            </div>
          </div>
        ) : (
          <div className="flex-1 overflow-y-auto p-4">
            <div className="max-w-2xl space-y-2">
              {sortedForTimeline.map((task) => (
                <div
                  key={task.id}
                  onClick={() => selectTask(task)}
                  className={cn(
                    "flex items-center gap-3 px-3 py-2.5 rounded-lg border bg-card hover:shadow-sm transition-all duration-150 cursor-pointer",
                    selectedTask?.id === task.id
                      ? "border-primary/40 shadow-sm"
                      : "border-border/60",
                  )}
                >
                  <StatusDot status={task.status} />
                  <div className="flex-1 min-w-0">
                    <p className="text-[13px] font-medium truncate">{task.title}</p>
                    {task.description && (
                      <p className="text-[11px] text-muted-foreground/60 truncate mt-0.5">
                        {task.description}
                      </p>
                    )}
                  </div>
                  <span className="text-[10px] font-mono text-muted-foreground/50 shrink-0">
                    {formatTime(task.updated_at)}
                  </span>
                  <span
                    className={cn(
                      "text-[9px] font-mono px-1.5 py-0.5 rounded-full shrink-0",
                      task.status === "done" && "bg-emerald-500/10 text-emerald-600",
                      task.status === "running" && "bg-blue-500/10 text-blue-600",
                      task.status === "pending" && "bg-muted text-muted-foreground",
                      task.status === "failed" && "bg-destructive/10 text-destructive",
                      task.status === "blocked" && "bg-amber-500/10 text-amber-600",
                      task.status === "review_requested" && "bg-amber-500/10 text-amber-600",
                    )}
                  >
                    {task.status}
                  </span>
                </div>
              ))}
              {sortedForTimeline.length === 0 && (
                <p className="text-center text-sm text-muted-foreground/50 py-12 font-mono">
                  No tasks yet
                </p>
              )}
            </div>
          </div>
        )}
      </div>

      {/* Task detail side panel */}
      {selectedTask && (
        <TaskDetailPanel
          task={selectedTask}
          onClose={() => onSelectTask?.(null)}
          onOpenSession={() => {
            if (selectedTask.session_id) onOpenTaskSession(selectedTask.session_id);
          }}
        />
      )}
    </div>
  );
}

function TaskDetailPanel({
  task,
  onClose,
  onOpenSession,
}: {
  task: ComponentsAgentTask;
  onClose: () => void;
  onOpenSession: () => void;
}) {
  return (
    <ResizableSidePanel>
      {/* Detail header */}
      <div className="flex-shrink-0 h-12 px-4 border-b border-border/60 flex items-center gap-2">
        <span
          className={cn(
            "text-[9px] font-mono px-2 py-0.5 rounded-full",
            task.status === "done" && "bg-emerald-500/10 text-emerald-600",
            task.status === "running" && "bg-blue-500/10 text-blue-600",
            task.status === "pending" && "bg-muted text-muted-foreground",
            task.status === "failed" && "bg-destructive/10 text-destructive",
            task.status === "blocked" && "bg-amber-500/10 text-amber-600",
            task.status === "review_requested" && "bg-amber-500/10 text-amber-600",
          )}
        >
          {task.status}
        </span>
        <h3 className="flex-1 text-[13px] font-medium truncate">{task.title}</h3>
        {task.session_id && (
          <Button
            size="xs"
            variant="ghost"
            onClick={onOpenSession}
            className="text-[10px] text-muted-foreground"
            title="Open full session"
          >
            Open
          </Button>
        )}
        <button
          onClick={onClose}
          className="text-muted-foreground/50 hover:text-foreground text-sm cursor-pointer"
        >
          ×
        </button>
      </div>

      {/* Task meta */}
      <div className="flex-shrink-0 px-4 py-3 border-b border-border/60 space-y-2">
        {task.description && (
          <p className="text-[12px] text-muted-foreground/80 leading-relaxed">{task.description}</p>
        )}
        <dl className="grid grid-cols-[72px_1fr] gap-x-2 gap-y-1.5 text-[11px]">
          <dt className="font-mono text-muted-foreground/50">Priority</dt>
          <dd
            className={cn(
              task.priority === "urgent" ? "text-destructive font-medium" : "text-foreground/70",
            )}
          >
            {task.priority}
          </dd>
          <dt className="font-mono text-muted-foreground/50">Updated</dt>
          <dd className="text-foreground/70">{formatTime(task.updated_at)}</dd>
          <dt className="font-mono text-muted-foreground/50">Created</dt>
          <dd className="text-foreground/70">{formatTime(task.created_at)}</dd>
          {task.session_id && (
            <>
              <dt className="font-mono text-muted-foreground/50">Session</dt>
              <dd className="text-foreground/70 truncate font-mono text-[10px]">
                {task.session_id}
              </dd>
            </>
          )}
        </dl>
      </div>

      {/* Conversation */}
      <div className="flex-1 min-h-0 flex flex-col overflow-hidden">
        {task.session_id ? (
          <SessionConversation
            sessionId={task.session_id}
            placeholder="Ask about this task..."
            className="h-full"
            bodyClassName="min-h-0 flex-1"
          />
        ) : (
          <div className="flex-1 flex items-center justify-center px-4">
            <p className="text-[11px] text-muted-foreground/50 font-mono text-center">
              No conversation session for this task
            </p>
          </div>
        )}
      </div>
    </ResizableSidePanel>
  );
}

function TaskCard({
  task,
  selected,
  onOpen,
}: {
  task: ComponentsAgentTask;
  selected: boolean;
  onOpen: () => void;
}) {
  return (
    <div
      onClick={onOpen}
      className={cn(
        "rounded-lg border bg-card p-3 transition-all duration-150 hover:shadow-sm cursor-pointer",
        selected ? "border-primary/40 shadow-sm" : "border-border/60",
      )}
    >
      <p className="text-[12px] font-medium leading-snug">{task.title}</p>
      {task.description && (
        <p className="text-[11px] text-muted-foreground/60 mt-1 line-clamp-2">{task.description}</p>
      )}
      <div className="flex items-center gap-2 mt-2">
        <StatusDot status={task.status} />
        <span className="text-[9px] font-mono text-muted-foreground/50">{task.status}</span>
        {task.priority === "urgent" && (
          <span className="text-[9px] font-mono px-1.5 py-0.5 rounded-full bg-destructive/10 text-destructive">
            urgent
          </span>
        )}
        <span className="text-[10px] font-mono text-muted-foreground/40 ml-auto">
          {formatTime(task.updated_at)}
        </span>
      </div>
    </div>
  );
}

function StatusDot({ status }: { status: string }) {
  return (
    <span
      className={cn(
        "w-2 h-2 rounded-full shrink-0",
        status === "done" && "bg-emerald-500",
        status === "running" && "bg-blue-500",
        status === "pending" && "bg-muted-foreground/30",
        status === "failed" && "bg-destructive",
        (status === "blocked" || status === "review_requested") && "bg-amber-500",
        status === "cancelled" && "bg-muted-foreground/20",
      )}
    />
  );
}
