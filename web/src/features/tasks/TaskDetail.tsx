import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { formatTime } from "@/lib/time";
import type { ComponentsAgentTask, ComponentsAgentTaskEvent } from "@/lib/api-client/types.gen";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { useI18n } from "@/lib/i18n";

function taskStatusVariant(status: string): "success" | "error" | "warning" | "outline" {
  if (status === "done") return "success";
  if (status === "failed" || status === "review_requested") return "error";
  if (status === "running" || status === "blocked") return "warning";
  return "outline";
}

function riskVariant(risk: string): "success" | "warning" | "error" | "outline" {
  if (risk === "low") return "success";
  if (risk === "medium") return "warning";
  if (risk === "high") return "error";
  return "outline";
}

function eventTypeVariant(type: string): "success" | "error" | "warning" | "outline" | "info" {
  if (type === "done") return "success";
  if (type === "failed" || type === "cancelled") return "error";
  if (type === "blocked" || type === "review_requested") return "warning";
  if (type === "started" || type === "progress") return "info";
  return "outline";
}

interface Props {
  task: ComponentsAgentTask;
  onAction: (updatedTask: ComponentsAgentTask) => void;
  onToast: (msg: string, kind?: "success" | "error") => void;
}

export function TaskDetail({ task, onAction, onToast }: Props) {
  const { t } = useI18n();
  const [events, setEvents] = useState<ComponentsAgentTaskEvent[]>([]);
  const [respondText, setRespondText] = useState("");
  const [actioning, setActioning] = useState(false);

  const loadEvents = useCallback(async () => {
    try {
      const list = await api<{ items: ComponentsAgentTaskEvent[] }>(
        "GET",
        "/api/tasks/" + task.id + "/events",
      );
      setEvents(list.items || []);
    } catch (e) {
      console.error(e);
    }
  }, [task.id]);

  useEffect(() => {
    void loadEvents();
  }, [loadEvents]);

  const doAction = useCallback(
    async (type: "approve" | "reject" | "respond" | "cancel", message?: string) => {
      setActioning(true);
      try {
        const updated = await api<ComponentsAgentTask>(
          "POST",
          "/api/tasks/" + task.id + "/action",
          { type, message },
        );
        onAction(updated);
        if (type === "approve") onToast(t("tasks.approved"));
        else if (type === "reject") onToast(t("tasks.rejected"));
        else if (type === "respond") {
          setRespondText("");
          onToast(t("tasks.responded"));
        } else if (type === "cancel") onToast(t("tasks.cancelled"));
        void loadEvents();
      } catch (e) {
        onToast(e instanceof Error ? e.message : "Request failed", "error");
      } finally {
        setActioning(false);
      }
    },
    [task.id, onAction, onToast, t, loadEvents],
  );

  return (
    <div>
      {/* Header */}
      <div className="flex items-start justify-between gap-4 mb-6">
        <h2 className="font-serif text-xl tracking-tight">{task.title}</h2>
        <div className="flex items-center gap-2 shrink-0">
          <Badge size="sm" variant={taskStatusVariant(task.status)}>
            {task.status}
          </Badge>
          <Badge size="sm" variant={task.priority === "urgent" ? "error" : "outline"}>
            {task.priority}
          </Badge>
        </div>
      </div>

      {/* Description */}
      {task.description && <p className="text-sm text-muted-foreground mb-4">{task.description}</p>}

      {/* Context metadata */}
      {task.context && Object.keys(task.context).length > 0 && (
        <div className="mb-6">
          <h3 className="text-[10px] font-mono uppercase tracking-wider text-muted-foreground mb-2">
            Context
          </h3>
          <div className="space-y-1 text-xs">
            {Object.entries(task.context).map(([k, v]) => (
              <div key={k} className="flex gap-2">
                <span className="font-mono text-muted-foreground shrink-0">{k}:</span>
                <span className="text-foreground break-all">{JSON.stringify(v)}</span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Review request */}
      {task.status === "review_requested" && task.review_request && (
        <div className="mb-6 rounded-lg border border-border p-4 space-y-3">
          <div className="flex items-center justify-between">
            <h3 className="text-sm font-semibold">Review Request</h3>
            {task.review_request.risk && (
              <Badge size="sm" variant={riskVariant(task.review_request.risk)}>
                risk: {task.review_request.risk}
              </Badge>
            )}
          </div>
          {task.review_request.question && (
            <p className="text-sm">{task.review_request.question}</p>
          )}
          {task.review_request.options && task.review_request.options.length > 0 && (
            <ul className="space-y-1">
              {task.review_request.options.map((opt) => (
                <li key={opt} className="text-sm flex items-center gap-2">
                  <span className="size-1.5 rounded-full bg-muted-foreground/50" />
                  {opt}
                </li>
              ))}
            </ul>
          )}
          {task.review_request.recommendation && (
            <p className="text-xs text-muted-foreground">
              Recommendation: {task.review_request.recommendation}
            </p>
          )}
          {task.review_request.details && (
            <p className="text-xs text-muted-foreground">{task.review_request.details}</p>
          )}
          <div className="flex gap-2 pt-1">
            <Button size="sm" loading={actioning} onClick={() => doAction("approve")}>
              {t("tasks.approve")}
            </Button>
            <Button
              variant="outline"
              size="sm"
              loading={actioning}
              onClick={() => doAction("reject")}
            >
              {t("tasks.reject")}
            </Button>
          </div>
        </div>
      )}

      {/* Respond form when blocked */}
      {task.status === "blocked" && (
        <div className="mb-6 rounded-lg border border-border p-4 space-y-3">
          <h3 className="text-sm font-semibold">Task is blocked — provide a response</h3>
          <Textarea
            value={respondText}
            onChange={(e) => setRespondText(e.target.value)}
            placeholder="Enter your response…"
          />
          <Button
            size="sm"
            loading={actioning}
            disabled={!respondText.trim()}
            onClick={() => doAction("respond", respondText)}
          >
            {t("common.submit")}
          </Button>
        </div>
      )}

      {/* Cancel button for active tasks */}
      {(task.status === "pending" ||
        task.status === "running" ||
        task.status === "blocked" ||
        task.status === "review_requested") && (
        <div className="mb-6">
          <Button
            variant="ghost"
            size="sm"
            className="text-destructive hover:text-destructive"
            loading={actioning}
            onClick={() => doAction("cancel")}
          >
            {t("tasks.cancelTask")}
          </Button>
        </div>
      )}

      {/* Event timeline */}
      <div className="pt-4 border-t border-border">
        <h3 className="text-[13px] font-semibold mb-3">Event Timeline</h3>
        {events.length === 0 ? (
          <p className="text-xs text-muted-foreground">No events yet.</p>
        ) : (
          <div className="space-y-0">
            {events.map((ev) => (
              <div
                key={ev.id}
                className="flex items-start gap-3 text-xs py-2 border-b border-border/50"
              >
                <Badge size="sm" variant={eventTypeVariant(ev.event_type)}>
                  {ev.event_type}
                </Badge>
                <span className="font-mono text-[11px] text-muted-foreground shrink-0">
                  {formatTime(ev.created_at)}
                </span>
                {ev.detail && Object.keys(ev.detail).length > 0 && (
                  <span className="text-muted-foreground truncate max-w-xs">
                    {JSON.stringify(ev.detail)}
                  </span>
                )}
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
