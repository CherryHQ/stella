import { useState } from "react";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { listGoalTimeline } from "@/lib/api-client";
import type { GoalTimelineEvent } from "@/lib/api-client/types.gen";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/messages";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { postGoalTimelineMessage } from "@/features/goals/useGoalTimelineMessage";

const PAGE_SIZE = 50;

type Payload = Record<string, unknown>;

export function GoalTimeline({ goalId, live = false }: { goalId: string; live?: boolean }) {
  const { t } = useI18n();
  const qc = useQueryClient();
  const [text, setText] = useState("");
  const [posting, setPosting] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const timeline = useInfiniteQuery({
    queryKey: ["goal-timeline", goalId],
    enabled: !!goalId,
    // The owner (GoalPage) knows whether the goal can still move; a done
    // goal's timeline is frozen, so polling stops with it.
    refetchInterval: live ? 5_000 : false,
    initialPageParam: "",
    queryFn: async ({ pageParam }) => {
      const { data } = await listGoalTimeline({
        path: { id: goalId },
        query: {
          page_size: PAGE_SIZE,
          page_token: pageParam || undefined,
        },
        throwOnError: true,
      });
      return data;
    },
    getNextPageParam: (last) => last?.next_page_token || undefined,
  });

  const events = timeline.data?.pages.flatMap((page) => page?.events ?? []) ?? [];

  const submit = async () => {
    const trimmed = text.trim();
    if (!trimmed) return;
    setPosting(true);
    setError(null);
    setNotice(null);
    try {
      const reattempt = await postGoalTimelineMessage(qc, goalId, trimmed);
      setText("");
      setNotice(
        reattempt ? t("goals.timelineReattemptAuthorized") : t("goals.timelineMessageSaved"),
      );
    } catch (e) {
      setError(e instanceof Error ? e.message : t("goals.timelinePostFailed"));
    } finally {
      setPosting(false);
    }
  };

  return (
    <div className="space-y-4">
      <div className="rounded-xl border border-border bg-background">
        {timeline.isLoading ? (
          <p className="p-4 text-sm text-muted-foreground">{t("common.loading")}</p>
        ) : events.length === 0 ? (
          <p className="p-4 text-sm text-muted-foreground">{t("goals.timelineEmpty")}</p>
        ) : (
          <ol className="divide-y divide-border">
            {events.map((event) => (
              <TimelineEventRow key={event.id} event={event} />
            ))}
          </ol>
        )}
      </div>

      {timeline.hasNextPage && (
        <Button
          variant="outline"
          size="sm"
          loading={timeline.isFetchingNextPage}
          onClick={() => void timeline.fetchNextPage()}
        >
          {t("goals.timelineLoadMore")}
        </Button>
      )}

      <div className="rounded-xl border border-border bg-card p-3.5">
        <label className="block text-xs font-medium text-muted-foreground">
          {t("goals.timelineMessageLabel")}
        </label>
        <Textarea
          value={text}
          onChange={(e) => setText(e.target.value)}
          rows={3}
          placeholder={t("goals.timelineMessagePlaceholder")}
          className="mt-2 text-sm"
        />
        <div className="mt-2.5 flex flex-wrap items-center gap-2">
          <Button size="sm" loading={posting} disabled={!text.trim()} onClick={() => void submit()}>
            {t("goals.timelineSend")}
          </Button>
          {notice && <span className="text-xs text-chart-3">{notice}</span>}
          {error && <span className="text-xs text-destructive">{error}</span>}
        </div>
      </div>
    </div>
  );
}

function TimelineEventRow({ event }: { event: GoalTimelineEvent }) {
  const { t } = useI18n();
  const payload = event.payload ?? {};
  const meta = eventMeta(event.event_type);
  const human = event.event_type === "human_message";

  return (
    <li className="p-3.5">
      <div className={cn("flex gap-3", human && "justify-end")}>
        {!human && <span className={cn("mt-1.5 size-2 shrink-0 rounded-full", meta.dot)} />}
        <div className={cn("min-w-0", human ? "max-w-[82%]" : "flex-1")}>
          <div
            className={cn(
              "rounded-xl border px-3 py-2.5",
              human ? "border-primary/25 bg-primary/10" : "border-border bg-muted/30",
            )}
          >
            <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
              <span className="font-mono text-[10.5px] font-semibold uppercase tracking-wide text-muted-foreground">
                {t(meta.label)}
              </span>
              <span className="font-mono text-[10.5px] text-muted-foreground">
                {formatTime(event.created_at)}
              </span>
            </div>
            <EventBody type={event.event_type} payload={payload} />
          </div>
        </div>
      </div>
    </li>
  );
}

function EventBody({ type, payload }: { type: GoalTimelineEvent["event_type"]; payload: Payload }) {
  const { t } = useI18n();
  switch (type) {
    case "plan_submitted": {
      const children = array(payload.children);
      const edges = array(payload.edges);
      return (
        <div className="mt-1.5 space-y-1.5 text-[12.5px] text-foreground">
          <p>
            {t("goals.timelinePlanSummary", { children: children.length, edges: edges.length })}
          </p>
          {children.length > 0 && (
            <p className="text-muted-foreground">
              {children
                .slice(0, 4)
                .map((child) => str(child.title) || str(child.key))
                .filter(Boolean)
                .join(" · ")}
              {children.length > 4 ? " …" : ""}
            </p>
          )}
          {edges.length > 0 && (
            <ul className="space-y-0.5 text-muted-foreground">
              {edges.slice(0, 3).map((edge, i) => (
                <li key={i}>
                  {str(edge.from) || str(edge.upstream_key)} →{" "}
                  {str(edge.to) || str(edge.downstream_key)}
                  {str(edge.kind) ? ` · ${str(edge.kind)}` : ""}
                  {str(edge.on_failure) ? ` · ${str(edge.on_failure)}` : ""}
                </li>
              ))}
            </ul>
          )}
        </div>
      );
    }
    case "attempt_started":
      return (
        <p className="mt-1.5 text-[12.5px] text-foreground">
          {t("goals.timelineAttemptStartedSummary", {
            purpose: str(payload.purpose) || "execution",
            attempt: num(payload.attempt_no) ?? "—",
            status: str(payload.status) || "running",
          })}
        </p>
      );
    case "attempt_finished":
      return (
        <div className="mt-1.5 space-y-1 text-[12.5px] text-foreground">
          <p>
            {t("goals.timelineAttemptFinishedSummary", {
              purpose: str(payload.purpose) || "execution",
              attempt: num(payload.attempt_no) ?? "—",
              status: str(payload.status) || "—",
            })}
          </p>
          {str(payload.failure_class) && (
            <p className="font-mono text-[11px] text-muted-foreground">
              {str(payload.failure_class)}
            </p>
          )}
          {(str(payload.reason) || str(payload.evidence_summary)) && (
            <p className="text-muted-foreground">
              {str(payload.reason) || str(payload.evidence_summary)}
            </p>
          )}
        </div>
      );
    case "acceptance_recorded":
      return (
        <div className="mt-1.5 space-y-1 text-[12.5px] text-foreground">
          <p>
            <span className="font-mono text-muted-foreground">
              {str(payload.item_kind) || "item"}
            </span>{" "}
            {str(payload.item_id) || "—"}: {str(payload.result) || "—"}
            {payload.exit_code !== undefined ? ` · exit ${exitCodeText(payload.exit_code)}` : ""}
          </p>
          {(str(payload.reason) || str(payload.detail)) && (
            <p className="text-muted-foreground">{str(payload.reason) || str(payload.detail)}</p>
          )}
        </div>
      );
    case "lifecycle_changed":
      return (
        <div className="mt-1.5 space-y-1 text-[12.5px] text-foreground">
          <p className="font-mono">
            {str(payload.from) || "—"} → {str(payload.to) || "—"}
          </p>
          {str(payload.block_reason) && (
            <p className="text-muted-foreground">{str(payload.block_reason)}</p>
          )}
        </div>
      );
    case "human_message":
      return (
        <div className="mt-1.5 space-y-1 text-[12.5px] text-foreground">
          <p className="whitespace-pre-wrap">{str(payload.text)}</p>
          {bool(payload.reattempt_authorized) && (
            <p className="font-mono text-[11px] text-primary">
              {t("goals.timelineReattemptAuthorized")}
            </p>
          )}
          {str(payload.reattempt_skipped_cause) && (
            <p className="font-mono text-[11px] text-muted-foreground">
              {str(payload.reattempt_skipped_cause)}
            </p>
          )}
        </div>
      );
    default:
      return <JsonSnippet value={payload} />;
  }
}

function JsonSnippet({ value }: { value: Payload }) {
  return (
    <pre className="mt-2 overflow-auto text-[11px] text-muted-foreground">
      {JSON.stringify(value, null, 2)}
    </pre>
  );
}

function eventMeta(type: GoalTimelineEvent["event_type"]): { label: MessageKey; dot: string } {
  switch (type) {
    case "plan_submitted":
      return { label: "goals.timelinePlanSubmitted", dot: "bg-primary" };
    case "attempt_started":
      return { label: "goals.timelineAttemptStarted", dot: "bg-chart-2" };
    case "attempt_finished":
      return { label: "goals.timelineAttemptFinished", dot: "bg-muted-foreground" };
    case "acceptance_recorded":
      return { label: "goals.timelineAcceptanceRecorded", dot: "bg-chart-3" };
    case "lifecycle_changed":
      return { label: "goals.timelineLifecycleChanged", dot: "bg-chart-4" };
    case "human_message":
      return { label: "goals.timelineHumanMessage", dot: "bg-primary" };
  }
}

function str(v: unknown): string {
  return typeof v === "string" ? v : "";
}

function num(v: unknown): number | null {
  return typeof v === "number" && Number.isFinite(v) ? v : null;
}

function bool(v: unknown): boolean {
  return v === true;
}

function exitCodeText(v: unknown): string {
  const n = num(v);
  if (n != null) return String(n);
  return typeof v === "string" ? v : "—";
}

function array(v: unknown): Payload[] {
  return Array.isArray(v)
    ? v.filter(
        (item): item is Payload => !!item && typeof item === "object" && !Array.isArray(item),
      )
    : [];
}
