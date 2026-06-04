import { cn } from "@/lib/utils";
import type { StoredDigestSummary } from "@/lib/api-client/types.gen";
import type { TFunction } from "../constants";

export function RecallyDigestView({
  t,
  className,
  storedDigests,
  storedDigestsLoading,
  selectedDigestDate,
  onSelectDigest,
}: {
  t: TFunction;
  className?: string;
  storedDigests: StoredDigestSummary[];
  storedDigestsLoading: boolean;
  selectedDigestDate: string | null;
  onSelectDigest: (date: string) => void;
}) {
  return (
    <div
      className={cn(
        "flex min-h-0 flex-col overflow-hidden border-r border-border bg-background",
        className,
      )}
    >
      <div className="shrink-0 border-b border-border bg-background px-4 py-4">
        <h1 className="text-xl font-semibold tracking-tight text-foreground">
          {t("recally.nav.digest")}
        </h1>
      </div>

      <div className="flex-1 overflow-auto">
        {storedDigestsLoading && (
          <p className="px-4 py-4 text-sm text-muted-foreground">{t("common.loading")}</p>
        )}
        {!storedDigestsLoading && storedDigests.length === 0 && (
          <p className="px-4 py-4 text-sm text-muted-foreground">{t("recally.digest.noHistory")}</p>
        )}
        {storedDigests.map((d) => (
          <button
            key={d.id}
            type="button"
            onClick={() => onSelectDigest(d.date)}
            className={cn(
              "w-full border-b border-border px-4 py-3 text-left transition-colors duration-120 hover:bg-muted cursor-pointer",
              selectedDigestDate === d.date && "bg-accent text-accent-foreground",
            )}
          >
            <div className="font-mono text-sm font-medium text-foreground">{d.date}</div>
            <div className="mt-1 text-xs text-muted-foreground">
              {d.saved_yesterday_count} {t("recally.stat.savedYesterday")} ·{" "}
              {d.worth_revisiting_count} {t("recally.stat.worthRevisiting")}
            </div>
          </button>
        ))}
      </div>
    </div>
  );
}
