import { Blocks, RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/messages";

/**
 * The marketplace list every install sheet shares: skeleton while loading, an
 * empty/error state, the rendered rows, and the infinite-scroll sentinel that
 * loads the next page as it nears the sheet's scroll container. The rAF
 * follow-up covers the case where an appended page still leaves the list
 * shorter than the viewport, which never re-triggers the observer.
 *
 * Rows are rendered through `renderItem` so each feature keeps its own card.
 */
export function MarketGrid<T>({
  isLoading,
  isError,
  isFetchingNextPage,
  isFetchNextPageError,
  hasNextPage,
  rows,
  sentinelRef,
  renderItem,
  onRetry,
  emptyIcon,
  emptyTitleKey,
  emptyDescriptionKey,
}: {
  isLoading: boolean;
  isError: boolean;
  isFetchingNextPage: boolean;
  isFetchNextPageError: boolean;
  hasNextPage: boolean;
  rows: T[];
  sentinelRef: React.RefObject<HTMLDivElement | null>;
  renderItem: (row: T) => React.ReactNode;
  onRetry: () => void;
  emptyIcon?: React.ReactNode;
  emptyTitleKey: MessageKey;
  emptyDescriptionKey: MessageKey;
}) {
  const { t } = useI18n();
  if (isLoading) {
    return (
      <div className="grid grid-cols-1 gap-3">
        {Array.from({ length: 4 }).map((_, i) => (
          <div key={i} className="space-y-3 rounded-lg border p-4">
            <div className="flex items-center gap-3">
              <Skeleton className="size-9 rounded-lg" />
              <Skeleton className="h-4 w-32" />
            </div>
            <Skeleton className="h-3 w-full" />
            <Skeleton className="h-3 w-4/5" />
          </div>
        ))}
      </div>
    );
  }
  if (isError || rows.length === 0) {
    return (
      <Empty>
        <EmptyHeader>
          <EmptyMedia variant="icon">{emptyIcon ?? <Blocks />}</EmptyMedia>
          <EmptyTitle>{t(emptyTitleKey)}</EmptyTitle>
          <EmptyDescription>
            {isError ? t("sessions.discover.loadError") : t(emptyDescriptionKey)}
          </EmptyDescription>
        </EmptyHeader>
        {isError && (
          <Button variant="outline" onClick={onRetry}>
            <RefreshCw />
            {t("common.retry")}
          </Button>
        )}
      </Empty>
    );
  }
  return (
    <>
      {/* Single column on purpose: the grid lives in a 560px sheet, but Tailwind
          breakpoints key off the viewport, so responsive columns would misfire. */}
      <div className="grid grid-cols-1 gap-3">{rows.map((row) => renderItem(row))}</div>
      <div ref={sentinelRef} className="flex min-h-12 items-center justify-center py-3">
        {isFetchingNextPage && <Spinner />}
        {isFetchNextPageError && (
          <Button variant="outline" size="sm" onClick={onRetry}>
            <RefreshCw />
            {t("common.retry")}
          </Button>
        )}
        {!hasNextPage && !isFetchNextPageError && (
          <span className="text-xs text-muted-foreground">
            {t("sessions.skillsList.allLoaded")}
          </span>
        )}
      </div>
    </>
  );
}
