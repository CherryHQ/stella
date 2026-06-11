import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { ChevronRight } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";

export interface TimelineRun {
  id: string;
  status: string;
  startedAt?: string;
  duration?: string;
  error?: string;
  sessionId?: string;
}

function statusTone(status: string): string {
  if (status === "success" || status === "completed" || status === "done") return "text-chart-3";
  if (status === "failed" || status === "error" || status === "timed_out")
    return "text-destructive";
  if (status === "running") return "text-chart-4";
  return "text-muted-foreground";
}

/** Run history: session link visible on every row, error output expandable inline. */
export function RunsTimeline({ runs, agentId }: { runs: TimelineRun[]; agentId: string }) {
  const { t } = useI18n();
  const [open, setOpen] = useState<string | null>(null);

  if (!runs.length) {
    return <p className="text-sm text-muted-foreground">{t("hub.noRuns")}</p>;
  }

  return (
    <div className="overflow-hidden rounded-xl border border-border">
      {runs.map((run) => {
        const isOpen = open === run.id;
        const hasError = !!run.error;
        return (
          <div key={run.id} className="border-b border-border last:border-b-0">
            <div
              role={hasError ? "button" : undefined}
              tabIndex={hasError ? 0 : undefined}
              onClick={hasError ? () => setOpen(isOpen ? null : run.id) : undefined}
              onKeyDown={
                hasError ? (e) => e.key === "Enter" && setOpen(isOpen ? null : run.id) : undefined
              }
              className={cn(
                "flex w-full items-center gap-3 px-3.5 py-2.5 text-left",
                hasError && "cursor-pointer hover:bg-muted/50",
              )}
            >
              <span
                className={cn(
                  "w-16 shrink-0 font-mono text-[11px] font-medium",
                  statusTone(run.status),
                  run.status === "running" && "animate-pulse",
                )}
              >
                {run.status}
              </span>
              <span className="w-24 shrink-0 text-[12.5px]">
                {run.startedAt ? formatTime(run.startedAt) : "—"}
              </span>
              <span className="w-16 shrink-0 font-mono text-[11px] text-muted-foreground">
                {run.duration || "—"}
              </span>
              <span
                className={cn(
                  "min-w-0 flex-1 truncate text-xs",
                  hasError ? "text-destructive" : "text-muted-foreground",
                )}
              >
                {run.error || ""}
              </span>
              {run.sessionId && (
                <Link
                  to="/agents/$agentId/sessions/$sessionId"
                  params={{ agentId, sessionId: run.sessionId }}
                  onClick={(e) => e.stopPropagation()}
                  className="shrink-0 text-xs font-medium text-primary hover:underline"
                >
                  {t("hub.openSession")}
                </Link>
              )}
              {hasError && (
                <ChevronRight
                  className={cn(
                    "size-3.5 shrink-0 text-muted-foreground transition-transform",
                    isOpen && "rotate-90",
                  )}
                />
              )}
            </div>
            {isOpen && hasError && (
              <div className="px-3.5 pb-3.5 sm:pl-[90px]">
                <div className="whitespace-pre-wrap rounded-lg border border-destructive/25 bg-destructive/[0.06] px-3 py-2.5 font-mono text-[11.5px] leading-relaxed text-destructive">
                  {run.error}
                </div>
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}
