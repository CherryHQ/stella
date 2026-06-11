import { useQuery } from "@tanstack/react-query";
import { Collapsible, CollapsiblePanel, CollapsibleTrigger } from "@/components/ui/collapsible";
import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { changelogQueryOptions } from "@/lib/queries/memories";
import { MemorySection } from "./MemorySection";

interface Props {
  agentId: string;
}

interface ChangelogEntry {
  id: string;
  scope: string;
  action: string;
  source: string;
  memory_version_before?: number | null;
  memory_version_after?: number | null;
  before_text?: string | null;
  after_text?: string | null;
  created_at: string;
}

const scopeStyles: Record<string, string> = {
  soul: "bg-primary/10 text-primary",
  profile: "bg-primary/10 text-primary",
  constraint: "bg-muted text-muted-foreground",
  compaction: "bg-muted text-muted-foreground",
};

const sourceStyles: Record<string, string> = {
  user: "bg-primary/10 text-primary",
  agent: "bg-muted text-muted-foreground",
  reflect: "bg-muted text-muted-foreground",
  system: "bg-muted text-muted-foreground",
};

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
            line.type === "removed" && "bg-destructive/10 text-destructive",
            line.type === "added" && "bg-chart-3/10 text-chart-3",
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

function ChangelogRow({ entry }: { entry: ChangelogEntry }) {
  const hasDetail = Boolean(entry.before_text || entry.after_text);

  const summary = (
    <div className="flex items-center gap-2 text-xs">
      <span
        className={cn(
          "shrink-0 rounded-sm px-1.5 py-0.5 text-xs font-medium",
          scopeStyles[entry.scope] || "bg-muted text-muted-foreground",
        )}
      >
        {entry.scope}
      </span>
      <span className="font-medium">{entry.action}</span>
      <span
        className={cn(
          "shrink-0 rounded-sm px-1.5 py-0.5 text-xs font-medium",
          sourceStyles[entry.source] || "bg-muted text-muted-foreground",
        )}
      >
        {entry.source}
      </span>
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
  const { data: entries = [], isLoading } = useQuery(changelogQueryOptions(agentId));

  return (
    <MemorySection
      title={t("memories.changelog.title")}
      description={t("memories.changelog.description")}
    >
      {isLoading ? (
        <div className="flex items-center justify-center py-6">
          <div className="size-4 animate-spin rounded-full border-2 border-muted-foreground/30 border-t-muted-foreground" />
        </div>
      ) : entries.length === 0 ? (
        <p className="text-sm text-muted-foreground italic">{t("memories.changelog.empty")}</p>
      ) : (
        <div className="space-y-0.5">
          {(entries as ChangelogEntry[]).map((entry) => (
            <ChangelogRow key={entry.id} entry={entry} />
          ))}
        </div>
      )}
    </MemorySection>
  );
}
