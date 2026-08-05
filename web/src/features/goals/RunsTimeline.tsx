import { useState } from "react";
import { ChevronRight } from "lucide-react";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";

export interface TimelineRun {
  id: string;
  status: string;
  startedAt?: string;
  duration?: string;
  error?: string;
  output?: string;
}

/**
 * A run status is a verdict, so it takes the status tokens, not the chart ones.
 *
 * This used to plot: success on `chart-3` and running on `chart-4`. Two things
 * were wrong with that. The hue said "warning" about a run that is merely still
 * going — nothing is wrong with it — while the goal lifecycle right next door
 * already reads in-progress as blue. And the chart tokens are tuned to be
 * *plotted*: as 12px text on the page they measured 2.53:1 and 2.19:1.
 *
 * Failure takes `destructive-foreground`, not `destructive`. The bare token is
 * the solid fill CossUI puts white on; its `-foreground` half is the one meant
 * to be read on the page, which is what a status word is.
 */
function statusTone(status: string): string {
  if (status === "success" || status === "completed" || status === "done") return "text-success";
  if (status === "failed" || status === "error" || status === "timed_out")
    return "text-destructive-foreground";
  if (status === "running") return "text-info";
  return "text-muted-foreground";
}

/** Run history with output/error expandable inline. */
export function RunsTimeline({ runs }: { runs: TimelineRun[] }) {
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
        const expandable = hasError || !!run.output;
        return (
          <div key={run.id} className="border-b border-border last:border-b-0">
            <div
              role={expandable ? "button" : undefined}
              tabIndex={expandable ? 0 : undefined}
              onClick={expandable ? () => setOpen(isOpen ? null : run.id) : undefined}
              onKeyDown={
                expandable ? (e) => e.key === "Enter" && setOpen(isOpen ? null : run.id) : undefined
              }
              className={cn(
                "flex w-full items-center gap-3 px-3.5 py-2.5 text-left",
                expandable && "cursor-pointer hover:bg-muted/50",
              )}
            >
              <span
                className={cn(
                  "w-16 shrink-0 font-mono text-xs font-medium",
                  statusTone(run.status),
                  run.status === "running" && "animate-pulse",
                )}
              >
                {run.status}
              </span>
              <span className="w-24 shrink-0 text-[12.5px]">
                {run.startedAt ? formatTime(run.startedAt) : "—"}
              </span>
              <span className="w-16 shrink-0 font-mono text-xs text-muted-foreground">
                {run.duration || "—"}
              </span>
              <span
                className={cn(
                  "min-w-0 flex-1 truncate text-xs",
                  hasError ? "text-destructive-foreground" : "text-muted-foreground",
                )}
              >
                {run.error || run.output || ""}
              </span>
              {expandable && (
                <ChevronRight
                  className={cn(
                    "size-3.5 shrink-0 text-muted-foreground transition-transform",
                    isOpen && "rotate-90",
                  )}
                />
              )}
            </div>
            {isOpen && expandable && (
              <div className="space-y-2 px-3.5 pb-3.5 sm:pl-[90px]">
                {run.error && (
                  <div className="whitespace-pre-wrap rounded-lg border border-destructive/25 bg-destructive/[0.06] px-3 py-2.5 font-mono text-[11.5px] leading-relaxed text-destructive-foreground">
                    {run.error}
                  </div>
                )}
                {run.output && (
                  <div className="rounded-lg border border-border bg-muted/40 px-3 py-2.5">
                    <MarkdownPreview
                      content={run.output}
                      className="text-xs [&_ol]:pl-5 [&_ul]:pl-5"
                    />
                  </div>
                )}
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}
