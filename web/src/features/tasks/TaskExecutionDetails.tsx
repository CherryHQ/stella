import { Link } from "@tanstack/react-router";
import type {
  Blocker,
  ComponentsDep,
  ComponentsEvent,
  ComponentsReview,
  ComponentsTask,
} from "@/lib/api-client/types.gen";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/time";
import { DetailSection } from "./DetailShell";
import { StatusPill, statusLabel } from "./lib";

function hasValue(value: unknown): boolean {
  if (value == null) return false;
  if (typeof value === "string") return value.trim().length > 0;
  if (Array.isArray(value)) return value.length > 0;
  if (typeof value === "object") return Object.keys(value as Record<string, unknown>).length > 0;
  return true;
}

function stringify(value: unknown): string {
  return typeof value === "string" ? value : JSON.stringify(value, null, 2);
}

function tryParse(value: string): unknown {
  try {
    return JSON.parse(value);
  } catch {
    return value;
  }
}

function extractLinks(value: unknown, label = ""): Array<{ label: string; url: string }> {
  if (typeof value === "string") {
    return /^https?:\/\//i.test(value) ? [{ label: label || value, url: value }] : [];
  }
  if (Array.isArray(value)) {
    return value.flatMap((item, index) =>
      extractLinks(item, label ? `${label}.${index}` : String(index)),
    );
  }
  if (value && typeof value === "object") {
    return Object.entries(value as Record<string, unknown>).flatMap(([key, child]) =>
      extractLinks(child, label ? `${label}.${key}` : key),
    );
  }
  return [];
}

function JsonBlock({ value }: { value: unknown }) {
  return (
    <pre className="max-h-80 overflow-auto rounded-xl border border-border bg-muted/40 px-4 py-3 text-[11.5px] leading-relaxed text-muted-foreground">
      {stringify(value)}
    </pre>
  );
}

export function TaskOutputArtifacts({ task }: { task: ComponentsTask }) {
  const { t } = useI18n();
  const links = [...extractLinks(task.output), ...extractLinks(task.context)];
  if (!hasValue(task.output) && links.length === 0) return null;
  return (
    <DetailSection title={t("hub.outputArtifacts")}>
      <div className="space-y-3">
        {hasValue(task.output) && <JsonBlock value={task.output} />}
        {links.length > 0 && (
          <div className="flex flex-wrap gap-2">
            {links.map((link, index) => (
              <a
                key={`${link.url}-${index}`}
                href={link.url}
                target="_blank"
                rel="noreferrer"
                className="rounded-full border border-border px-3 py-1 text-xs font-medium text-primary hover:underline"
              >
                {link.label}
              </a>
            ))}
          </div>
        )}
      </div>
    </DetailSection>
  );
}

export function TaskDependencySection({
  agentId,
  deps,
}: {
  agentId: string;
  deps: ComponentsDep[];
}) {
  const { t } = useI18n();
  if (deps.length === 0) return null;
  return (
    <DetailSection title={t("hub.dependencies")}>
      <div className="overflow-hidden rounded-xl border border-border">
        {deps.map((dep) => (
          <div
            key={dep.dep_task_id}
            className="flex flex-wrap items-center gap-3 border-b border-border px-3.5 py-2.5 text-[12.5px] last:border-b-0"
          >
            <Link
              to="/agents/$agentId/tasks/$taskId"
              params={{ agentId, taskId: dep.dep_task_id }}
              className="font-mono font-medium text-primary hover:underline"
            >
              {dep.dep_task_id.slice(0, 8)}
            </Link>
            {dep.upstream_status && (
              <StatusPill
                status={dep.upstream_status}
                label={statusLabel(t, dep.upstream_status)}
              />
            )}
            <span className="text-muted-foreground">{dep.dep_kind}</span>
            <span className="text-muted-foreground">{dep.on_failure}</span>
            {dep.waived_at && (
              <span className="text-chart-4">
                {t("hub.waivedAt", { time: formatTime(dep.waived_at) })}
              </span>
            )}
            {dep.waiver_reason && (
              <span className="text-muted-foreground">{dep.waiver_reason}</span>
            )}
          </div>
        ))}
      </div>
    </DetailSection>
  );
}

export function TaskReviewSection({ reviews }: { reviews: ComponentsReview[] }) {
  const { t } = useI18n();
  if (reviews.length === 0) return null;
  return (
    <DetailSection title={t("hub.reviews")}>
      <div className="space-y-2">
        {reviews.map((review) => (
          <div key={review.id} className="rounded-xl border border-border bg-muted/30 px-4 py-3">
            <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
              <span className="font-mono">{review.reviewer_type}</span>
              <span>·</span>
              <span className="font-mono">{review.status.replace(/_/g, " ")}</span>
              <span>·</span>
              <span>{formatTime(review.updated_at)}</span>
            </div>
            {review.summary && <p className="mt-2 text-[13px] text-foreground">{review.summary}</p>}
            {review.feedback && (
              <MarkdownPreview
                content={review.feedback}
                className="mt-2 text-[12.5px] text-muted-foreground [&_ol]:pl-5 [&_ul]:pl-5"
              />
            )}
          </div>
        ))}
      </div>
    </DetailSection>
  );
}

export function TaskEventSection({ events }: { events: ComponentsEvent[] }) {
  const { t } = useI18n();
  if (events.length === 0) return null;
  return (
    <DetailSection title={t("hub.eventsTimeline")}>
      <ol className="relative space-y-4 border-l border-border pl-4">
        {events.map((event) => (
          <li key={event.id} className="relative">
            <span className="absolute -left-[21px] top-1 size-2 rounded-full bg-muted-foreground/40" />
            <div className="text-[12.5px] text-foreground">
              <span className="font-medium">{event.event_type.replace(/_/g, " ")}</span>
              {event.from_status && event.to_status && (
                <span className="text-muted-foreground">
                  {" "}
                  · {event.from_status} → {event.to_status}
                </span>
              )}
            </div>
            <div className="mt-0.5 font-mono text-[10.5px] text-muted-foreground">
              {event.actor_type} · {formatTime(event.created_at)}
            </div>
            {hasValue(event.detail) && (
              <div className="mt-2">
                <JsonBlock value={event.detail} />
              </div>
            )}
          </li>
        ))}
      </ol>
    </DetailSection>
  );
}

export function TaskBlockerDetails({ blocker }: { blocker?: Blocker }) {
  const { t } = useI18n();
  if (!blocker) return null;
  return (
    <DetailSection title={t("hub.blockerDetails")}>
      <div className="rounded-xl border border-chart-4/30 bg-chart-4/[0.06] px-4 py-3.5">
        <div className="flex flex-wrap items-center gap-2 text-xs text-chart-4">
          <span className="font-medium uppercase tracking-wide">
            {blocker.kind.replace(/_/g, " ")}
          </span>
          <span>·</span>
          <span>{blocker.status}</span>
          <span>·</span>
          <span>{formatTime(blocker.created_at)}</span>
        </div>
        {blocker.question && (
          <MarkdownPreview
            content={blocker.question}
            className="mt-2 text-[13px] [&_ol]:pl-5 [&_ul]:pl-5"
          />
        )}
        {blocker.detail && (
          <div className="mt-3">
            <JsonBlock value={tryParse(blocker.detail)} />
          </div>
        )}
        {blocker.resolution && (
          <div className="mt-3 rounded-lg border border-border bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
            {blocker.resolution}
          </div>
        )}
      </div>
    </DetailSection>
  );
}
