import { History } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { changelogQueryOptions } from "@/lib/queries/memories";

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
  before_text?: string;
  after_text?: string;
  created_at: string;
}

const scopeColors: Record<string, string> = {
  soul: "bg-violet-500/15 text-violet-600 dark:text-violet-400",
  profile: "bg-blue-500/15 text-blue-600 dark:text-blue-400",
  constraint: "bg-amber-500/15 text-amber-600 dark:text-amber-400",
  compaction: "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400",
};

const sourceColors: Record<string, string> = {
  user: "bg-primary/10 text-primary",
  agent: "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400",
  reflect: "bg-violet-500/15 text-violet-600 dark:text-violet-400",
  system: "bg-muted text-muted-foreground",
};

export function ChangelogSection({ agentId }: Props) {
  const { t } = useI18n();
  const { data: entries = [], isLoading } = useQuery(changelogQueryOptions(agentId));

  return (
    <section className="rounded-2xl border border-border/40 bg-card/45 backdrop-blur-md shadow-2xs overflow-hidden">
      {/* Header */}
      <div className="flex items-start justify-between gap-3 px-6 pt-5 pb-3">
        <div className="flex items-center gap-3 min-w-0">
          <span className="grid size-8 shrink-0 place-items-center rounded-lg bg-muted text-muted-foreground">
            <History className="size-4" />
          </span>
          <div className="min-w-0">
            <h2 className="text-sm font-semibold text-foreground">
              {t("memories.changelog.title")}
            </h2>
            <p className="text-xs text-muted-foreground mt-0.5">
              {t("memories.changelog.description")}
            </p>
          </div>
        </div>
      </div>

      {/* Content */}
      <div className="px-6 pb-5">
        {isLoading ? (
          <div className="flex items-center justify-center py-6">
            <div className="size-4 animate-spin rounded-full border-2 border-muted-foreground/30 border-t-muted-foreground" />
          </div>
        ) : entries.length === 0 ? (
          <p className="text-sm text-muted-foreground italic py-4">
            {t("memories.changelog.empty")}
          </p>
        ) : (
          <div className="space-y-1">
            {(entries as ChangelogEntry[]).map((entry) => (
              <div
                key={entry.id}
                className="flex items-center gap-2 rounded-xl px-3 py-2 text-xs transition-colors hover:bg-muted"
              >
                {/* Scope badge */}
                <span
                  className={cn(
                    "shrink-0 rounded-md px-1.5 py-0.5 text-[10px] font-medium",
                    scopeColors[entry.scope] || "bg-muted text-muted-foreground",
                  )}
                >
                  {entry.scope}
                </span>

                {/* Action */}
                <span className="text-foreground font-medium">{entry.action}</span>

                {/* Source badge */}
                <span
                  className={cn(
                    "shrink-0 rounded-md px-1.5 py-0.5 text-[10px] font-medium",
                    sourceColors[entry.source] || "bg-muted text-muted-foreground",
                  )}
                >
                  {entry.source}
                </span>

                {/* Version */}
                {entry.memory_version_after != null && (
                  <span className="text-muted-foreground font-mono">
                    v{entry.memory_version_after}
                  </span>
                )}

                {/* Timestamp */}
                <span className="ml-auto shrink-0 text-muted-foreground font-mono text-[10px]">
                  {formatTime(entry.created_at)}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
    </section>
  );
}
