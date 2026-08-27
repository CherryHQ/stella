import { useInfiniteQuery } from "@tanstack/react-query";
import type { ComponentsChangelogEntry } from "@/lib/api-client/types.gen";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsiblePanel, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Spinner } from "@/components/ui/spinner";
import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import {
  flattenMemoryChangelogPages,
  memoryChangelogInfiniteQueryOptions,
} from "@/lib/queries/memories";
import { MemorySection } from "./MemorySection";

interface Props {
  agentId: string;
}

const scopeLabels = {
  soul: "memories.changelog.scopeSoul",
  profile: "memories.changelog.scopeProfile",
  knowledge: "memories.changelog.scopeKnowledge",
  constraint: "memories.changelog.scopeConstraint",
} as const;

const actionLabels = {
  create: "memories.changelog.actionCreate",
  edit: "memories.changelog.actionEdit",
  update: "memories.changelog.actionEdit",
  delete: "memories.changelog.actionManualDelete",
  manual_delete: "memories.changelog.actionManualDelete",
  curator_remove: "memories.changelog.actionCuratorRemove",
  restore: "memories.changelog.actionRestore",
} as const;

const sourceLabels = {
  manual: "memories.changelog.sourceManual",
  reflect: "memories.changelog.sourceReflect",
  user: "memories.changelog.sourceUser",
  agent: "memories.changelog.sourceAgent",
  system: "memories.changelog.sourceSystem",
} as const;

interface DiffLine {
  type: "added" | "removed" | "context";
  text: string;
}

function computeDiff(before: string, after: string): DiffLine[] {
  const a = before.split("\n");
  const b = after.split("\n");
  const m = a.length;
  const n = b.length;

  const dp: number[][] = Array.from({ length: m + 1 }, () =>
    Array.from({ length: n + 1 }, () => 0),
  );
  for (let i = 1; i <= m; i++)
    for (let j = 1; j <= n; j++)
      dp[i][j] =
        a[i - 1] === b[j - 1] ? dp[i - 1][j - 1] + 1 : Math.max(dp[i - 1][j], dp[i][j - 1]);

  const result: DiffLine[] = [];
  let i = m;
  let j = n;
  while (i > 0 || j > 0) {
    if (i > 0 && j > 0 && a[i - 1] === b[j - 1]) {
      result.unshift({ type: "context", text: a[i - 1] });
      i--;
      j--;
    } else if (j > 0 && (i === 0 || dp[i][j - 1] >= dp[i - 1][j])) {
      result.unshift({ type: "added", text: b[j - 1] });
      j--;
    } else {
      result.unshift({ type: "removed", text: a[i - 1] });
      i--;
    }
  }
  return result;
}

function DiffView({ before, after }: { before: string; after: string }) {
  const lines = computeDiff(before, after);
  return (
    <div className="overflow-x-auto rounded-md border border-border font-mono text-xs leading-relaxed">
      {lines.map((line, i) => (
        <div
          key={i}
          className={cn(
            "flex gap-2 px-3 py-0.5",
            line.type === "removed" && "bg-destructive/10 text-destructive-foreground",
            line.type === "added" && "bg-success/10 text-success-foreground",
            line.type === "context" && "text-muted-foreground",
          )}
        >
          <span className="w-3 shrink-0 select-none text-right opacity-50">
            {line.type === "removed" ? "−" : line.type === "added" ? "+" : " "}
          </span>
          <span className="whitespace-pre-wrap">{line.text || " "}</span>
        </div>
      ))}
    </div>
  );
}

function ChangelogRow({ entry }: { entry: ComponentsChangelogEntry }) {
  const { t } = useI18n();
  const hasDetail = Boolean(entry.before_text || entry.after_text);
  // SAFETY: each scope/action/source value indexes the corresponding labels map keyed by those literal unions.
  const scopeKey = scopeLabels[entry.scope as keyof typeof scopeLabels];
  // SAFETY: same invariant for the action labels map.
  const actionKey = actionLabels[entry.action as keyof typeof actionLabels];
  // SAFETY: same invariant for the source labels map.
  const sourceKey = sourceLabels[entry.source as keyof typeof sourceLabels];

  const summary = (
    <div className="flex flex-wrap items-center gap-2 text-xs">
      <Badge variant="secondary">{scopeKey ? t(scopeKey) : entry.scope}</Badge>
      <span className="font-medium">{actionKey ? t(actionKey) : entry.action}</span>
      <Badge variant="outline">{sourceKey ? t(sourceKey) : entry.source}</Badge>
      {entry.memory_version_after != null && (
        <span className="text-muted-foreground font-mono">v{entry.memory_version_after}</span>
      )}
      <span className="ml-auto shrink-0 text-muted-foreground font-mono text-xs">
        {formatTime(entry.created_at)}
      </span>
    </div>
  );

  if (!hasDetail) {
    return (
      <div className="rounded-lg px-3 py-2 transition-colors hover:bg-muted/50">{summary}</div>
    );
  }

  return (
    <Collapsible>
      <CollapsibleTrigger className="w-full cursor-pointer rounded-lg px-3 py-2 text-left transition-colors hover:bg-muted/50">
        {summary}
      </CollapsibleTrigger>
      <CollapsiblePanel>
        <div className="px-3 pb-3 pt-1">
          {entry.before_text && entry.after_text ? (
            <DiffView before={entry.before_text} after={entry.after_text} />
          ) : (
            <pre className="whitespace-pre-wrap rounded-md bg-muted/50 p-3 font-mono text-xs leading-relaxed">
              {entry.after_text ?? entry.before_text}
            </pre>
          )}
        </div>
      </CollapsiblePanel>
    </Collapsible>
  );
}

export function ChangelogSection({ agentId }: Props) {
  const { t } = useI18n();
  const query = useInfiniteQuery(memoryChangelogInfiniteQueryOptions(agentId));
  const entries = flattenMemoryChangelogPages(query.data?.pages);

  return (
    <MemorySection
      title={t("memories.changelog.title")}
      description={t("memories.changelog.description")}
    >
      {query.isLoading ? (
        <div className="flex items-center justify-center py-6">
          <Spinner />
        </div>
      ) : query.isError ? (
        <p className="text-sm text-destructive-foreground">{t("memories.changelog.error")}</p>
      ) : entries.length === 0 ? (
        <p className="text-sm text-muted-foreground italic">{t("memories.changelog.empty")}</p>
      ) : (
        <>
          <div className="space-y-0.5">
            {entries.map((entry) => (
              <ChangelogRow key={entry.id} entry={entry} />
            ))}
          </div>
          {query.hasNextPage && (
            <div className="flex justify-center pt-4">
              <Button
                variant="outline"
                size="sm"
                loading={query.isFetchingNextPage}
                onClick={() => void query.fetchNextPage()}
              >
                {t("common.loadMore")}
              </Button>
            </div>
          )}
        </>
      )}
    </MemorySection>
  );
}
