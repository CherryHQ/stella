import type { ReactNode } from "react";
import { Link, useRouter } from "@tanstack/react-router";
import { AlertTriangle } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import { Spinner } from "@/components/ui/spinner";
import { useI18n } from "@/lib/i18n";

/**
 * The one failure surface. Routes mount it through {@link RouteError} /
 * {@link RouteNotFound}; list pages mount it directly on a query's `isError`.
 *
 * It exists because a request that fails and a resource that is empty used to
 * render the same thing, so a server outage read as "you have no data" — the
 * most convincing wrong answer this UI can give.
 */
export function ErrorState({
  title,
  description,
  onRetry,
  action,
}: {
  title: string;
  description?: string;
  onRetry?: () => void;
  action?: ReactNode;
}) {
  const { t } = useI18n();
  return (
    <Empty>
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <AlertTriangle className="text-destructive-foreground" />
        </EmptyMedia>
        <EmptyTitle>{title}</EmptyTitle>
        {description && <EmptyDescription>{description}</EmptyDescription>}
      </EmptyHeader>
      {onRetry && (
        <Button variant="outline" size="sm" onClick={onRetry}>
          {t("common.retry")}
        </Button>
      )}
      {action}
    </Empty>
  );
}

/**
 * Router `errorComponent`. Retry re-runs the failed loaders rather than only
 * clearing the boundary, so a transient outage recovers without a full reload.
 */
export function RouteError({ error, reset }: { error: Error; reset?: () => void }) {
  const { t } = useI18n();
  const router = useRouter();
  const detail = error?.message?.trim();
  return (
    <ErrorState
      title={t("route.error.title")}
      description={detail || t("route.error.desc")}
      onRetry={() => {
        reset?.();
        void router.invalidate();
      }}
    />
  );
}

/** Router `notFoundComponent`. A bad URL gets an explanation, not a silent bounce. */
export function RouteNotFound() {
  const { t } = useI18n();
  return (
    <ErrorState
      title={t("route.notFound.title")}
      description={t("route.notFound.desc")}
      action={
        <Button variant="outline" size="sm" render={<Link to="/agents" />}>
          {t("route.notFound.home")}
        </Button>
      }
    />
  );
}

/** Router `defaultPendingComponent`: blocking loaders and lazy chunks get a pulse. */
export function RoutePending() {
  return (
    <div className="flex h-full min-h-64 w-full items-center justify-center">
      <Spinner className="size-5 text-muted-foreground" />
    </div>
  );
}
