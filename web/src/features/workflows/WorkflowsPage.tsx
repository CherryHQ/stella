import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { Search, Workflow as WorkflowIcon } from "lucide-react";
import { useCallback, useMemo } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { SettingsEmptyState } from "@/features/settings/SettingsEmptyState";
import { asFrozenPlan, planStats } from "@/features/workflows/lib";
import type { Workflow } from "@/lib/api-client/types.gen";
import { useI18n } from "@/lib/i18n";
import { WORKFLOWS_PAGE_SIZE, workflowsPageOptions } from "@/lib/queries/workflows";
import { formatTime } from "@/lib/time";

interface WorkflowsSearch {
  q?: string;
  page?: number;
}

export function WorkflowsPage() {
  const { t } = useI18n();
  const { agentId } = useParams({ strict: false }) as { agentId: string };
  const { q, page: pageParam } = useSearch({ strict: false }) as WorkflowsSearch;
  const navigate = useNavigate();
  const page = Math.max(1, pageParam ?? 1);

  const patch = useCallback(
    (next: Partial<WorkflowsSearch>) =>
      void navigate({
        to: "/agents/$agentId/workflows",
        params: { agentId },
        search: (prev: WorkflowsSearch) => ({ ...prev, ...next }),
      }),
    [navigate, agentId],
  );

  const { data, isLoading } = useQuery(workflowsPageOptions({ agentId, q, page }));
  const workflows = data?.workflows ?? [];
  const total = data?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / WORKFLOWS_PAGE_SIZE));

  return (
    <div className="h-full min-h-0 overflow-y-auto bg-background">
      <div className="mx-auto max-w-[800px] px-6 py-7 pb-20 sm:px-8">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h1 className="text-[22px] font-semibold tracking-tight">{t("workflows.title")}</h1>
          <div className="relative">
            <Search className="absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              type="search"
              value={q ?? ""}
              onChange={(e) => patch({ q: e.target.value || undefined, page: undefined })}
              placeholder={t("workflows.searchPlaceholder")}
              className="w-56 pl-8"
              nativeInput
            />
          </div>
        </div>
        <p className="mt-1.5 text-[12.5px] text-muted-foreground">{t("workflows.subtitle")}</p>

        <div className="mt-6">
          {isLoading ? (
            <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
          ) : workflows.length === 0 ? (
            <SettingsEmptyState
              icon={<WorkflowIcon className="size-5" />}
              message={t(q ? "workflows.noMatch" : "workflows.empty")}
              description={q ? undefined : t("workflows.emptyHint")}
            />
          ) : (
            <div className="flex flex-col gap-2.5">
              {workflows.map((w) => (
                <WorkflowRow key={w.id} agentId={agentId} w={w} />
              ))}
            </div>
          )}
        </div>

        {totalPages > 1 && (
          <div className="mt-6 flex items-center justify-between">
            <span className="text-xs text-muted-foreground">
              {t("goals.pageStatus", { page, totalPages, total })}
            </span>
            <div className="flex gap-2">
              <Button
                variant="outline"
                size="sm"
                disabled={page <= 1}
                onClick={() => patch({ page: page - 1 })}
              >
                {t("common.back")}
              </Button>
              <Button
                variant="outline"
                size="sm"
                disabled={page >= totalPages}
                onClick={() => patch({ page: page + 1 })}
              >
                {t("common.next")}
              </Button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

function WorkflowRow({ agentId, w }: { agentId: string; w: Workflow }) {
  const { t } = useI18n();
  const stats = useMemo(() => planStats(asFrozenPlan(w.plan)), [w.plan]);
  return (
    <Link
      to="/agents/$agentId/workflows/$workflowId"
      params={{ agentId, workflowId: w.id }}
      className="rounded-xl border border-border bg-card px-4 py-3 transition-colors hover:bg-muted/40"
    >
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
        <span className="text-[14px] font-medium text-foreground">{w.name}</span>
        <Badge size="sm" variant="secondary">
          {t("workflows.versionLabel", { n: w.version })}
        </Badge>
      </div>
      {w.intent && (
        <p className="mt-1 line-clamp-1 text-[12.5px] text-muted-foreground">{w.intent}</p>
      )}
      <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-xs text-muted-foreground">
        <span>
          {t("workflows.planSummary", {
            nodes: stats.nodes,
            leaves: stats.leaves,
            composites: stats.composites,
          })}
        </span>
        <span>{t("hub.updatedAt", { time: formatTime(w.updated_at) })}</span>
      </div>
    </Link>
  );
}
