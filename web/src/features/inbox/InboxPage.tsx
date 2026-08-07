import { useMemo } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { AlertCircle, CheckCircle2, CircleAlert, ExternalLink } from "lucide-react";
import { ConversationSidebar } from "@/features/sessions/ConversationSidebar";
import { AppShell } from "@/layouts/AppShell";
import { useI18n } from "@/lib/i18n";
import { agentsQueryOptions } from "@/lib/queries/agents";
import { inboxInfiniteQueryOptions } from "@/lib/queries/inbox";
import type { InboxItem } from "@/lib/api-client/types.gen";
import { Button } from "@/components/ui/button";
import { ErrorState } from "@/components/RouteFallback";

const kindLabels = {
  blocked: "inbox.kind.blocked",
  review: "inbox.kind.review",
  failed: "inbox.kind.failed",
} as const;

const sourceLabels = {
  goal: "inbox.source.goal",
  scheduler_run: "inbox.source.scheduler_run",
} as const;

export function InboxPage() {
  const { t } = useI18n();
  const inboxQuery = useInfiniteQuery(inboxInfiniteQueryOptions());
  // Items carry an agent id only. The agent list is already loaded for the
  // sidebar, so naming the owner of each item costs nothing and no API field —
  // and without it a cross-agent list cannot say which agent is waiting.
  const { data: agents = [] } = useQuery(agentsQueryOptions);
  const agentNames = useMemo(() => new Map(agents.map((a) => [a.id, a.name])), [agents]);
  const isLoading = inboxQuery.isLoading;
  const items = useMemo(
    () => inboxQuery.data?.pages.flatMap((page) => page.items ?? []) ?? [],
    [inboxQuery.data],
  );
  const byKind = useMemo(
    () => ({
      blocked: items.filter((item) => item.kind === "blocked").length,
      review: items.filter((item) => item.kind === "review").length,
      failed: items.filter((item) => item.kind === "failed").length,
    }),
    [items],
  );

  return (
    <AppShell
      sidebar={<ConversationSidebar />}
      title={
        <div className="min-w-0">
          <h1 className="truncate text-[15px] font-semibold">{t("inbox.title")}</h1>
          <p className="mt-0.5 truncate text-xs text-muted-foreground">{t("inbox.subtitle")}</p>
        </div>
      }
    >
      <div className="flex min-h-0 flex-1 flex-col overflow-hidden bg-background">
        <div className="border-b border-border/60 px-5 py-3">
          <div className="flex flex-wrap items-center gap-2 text-xs">
            <Metric label={t("inbox.blocked")} value={byKind.blocked} />
            <Metric label={t("inbox.review")} value={byKind.review} />
            <Metric label={t("inbox.failed")} value={byKind.failed} />
          </div>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">
          {isLoading ? (
            <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
          ) : inboxQuery.isError ? (
            // A failed fetch must never reach the branch below: a green check
            // over "Nothing needs attention." is the most dangerous thing this
            // page can say when the server is down.
            <ErrorState
              title={t("route.error.title")}
              description={t("route.loadFailed")}
              onRetry={() => void inboxQuery.refetch()}
            />
          ) : items.length === 0 ? (
            <div className="flex h-full items-center justify-center">
              <div className="text-center">
                <CheckCircle2 className="mx-auto mb-3 size-8 text-success" />
                <p className="text-sm font-medium">{t("inbox.empty")}</p>
              </div>
            </div>
          ) : (
            <div className="mx-auto w-full max-w-4xl">
              <div className="divide-y divide-border/70 border-y border-border/70">
                {items.map((item) => (
                  <InboxRow
                    key={item.id}
                    item={item}
                    agentName={item.agent_id ? (agentNames.get(item.agent_id) ?? "") : ""}
                  />
                ))}
              </div>
              {inboxQuery.hasNextPage && (
                <Button
                  variant="ghost"
                  size="sm"
                  className="mt-3 w-full"
                  disabled={inboxQuery.isFetchingNextPage}
                  onClick={() => void inboxQuery.fetchNextPage()}
                >
                  {t("sessions.sidebar.loadMore")}
                </Button>
              )}
            </div>
          )}
        </div>
      </div>
    </AppShell>
  );
}

function Metric({ label, value }: { label: string; value: number }) {
  return (
    <span className="inline-flex h-7 items-center gap-2 rounded-md border border-border/70 px-2.5 text-muted-foreground">
      {label}
      <span className="font-mono text-foreground">{value}</span>
    </span>
  );
}

function InboxRow({ item, agentName }: { item: InboxItem; agentName: string }) {
  const { t } = useI18n();
  const icon =
    item.kind === "failed" ? (
      <CircleAlert className="size-4 text-destructive-foreground" />
    ) : (
      <AlertCircle className="size-4 text-primary" />
    );

  return (
    <div className="flex min-h-[70px] items-center gap-3 py-3">
      <div className="grid size-8 shrink-0 place-items-center rounded-md border border-border bg-muted/40">
        {icon}
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-2">
          <p className="truncate text-sm font-medium">{item.title}</p>
          <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-xs uppercase text-muted-foreground">
            {t(kindLabels[item.kind])}
          </span>
        </div>
        <p className="mt-1 line-clamp-1 text-xs text-muted-foreground">
          {agentName ? `${agentName} · ` : ""}
          {item.detail || t(sourceLabels[item.source_type])}
        </p>
      </div>
      <Button
        variant="ghost"
        size="xs"
        className="h-8 gap-1.5 rounded-md"
        render={<Link to={item.target_path} />}
      >
        <ExternalLink className="size-3.5" />
        {t("inbox.open")}
      </Button>
    </div>
  );
}
