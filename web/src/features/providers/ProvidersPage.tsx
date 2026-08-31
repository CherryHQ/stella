import { useMemo } from "react";
import { Link, useNavigate, useParams, useRouterState } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { providersQueryOptions, providerTypesQueryOptions } from "@/lib/queries/providers";
import { useI18n } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { ErrorState } from "@/components/RouteFallback";
import {
  SettingsCard,
  SettingsCardSection,
  SettingsDetailSheet,
  SettingsGridPage,
} from "@/features/settings/SettingsCardGrid";
import { Boxes, Plus } from "lucide-react";
import { ProviderDetailPanel } from "./ProviderDetailPanel";
import { NewProviderForm } from "./NewProviderForm";

export function ProvidersPage() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const isAdminSurface = useRouterState({
    select: (state) => state.location.pathname.startsWith("/admin/"),
  });
  const listRoute = isAdminSurface ? "/admin/ai/providers" : "/settings/providers";
  const detailRoute = isAdminSurface
    ? "/admin/ai/providers/$providerId"
    : "/settings/providers/$providerId";
  // SAFETY: this route may or may not carry a providerId param; read as optional.
  const params = useParams({ strict: false }) as { providerId?: string };
  const providerId = params.providerId;

  // Neither is defaulted to `[]` at the destructure. TanStack Query turns a
  // rejection into `isError` instead of throwing to the router boundary, so a
  // swallowed failure renders "no providers configured" during an outage — and
  // worse, offers a create form whose type list is silently empty.
  const providersQuery = useQuery(providersQueryOptions);
  const typesQuery = useQuery(providerTypesQueryOptions);
  const providers = providersQuery.data ?? [];
  const providerTypes = typesQuery.data ?? [];
  const isPending = providersQuery.isPending || typesQuery.isPending;
  const isError = providersQuery.isError || typesQuery.isError;

  const providerDefaults = useMemo(() => {
    const defaults: Record<string, { base_url: string; name: string }> = {};
    for (const pt of providerTypes) {
      defaults[pt.id] = { base_url: pt.default_url, name: pt.name };
    }
    return defaults;
  }, [providerTypes]);

  const sortedTypes = useMemo(
    () => [...providerTypes].sort((a, b) => (a.name || a.id).localeCompare(b.name || b.id)),
    [providerTypes],
  );

  const normalizedProviders = useMemo(
    () =>
      providers.map((p) => ({
        ...p,
        type: p.type || p.id,
        enabled: p.enabled !== false,
        models: p.models || {},
      })),
    [providers],
  );

  // Group providers by type, labelled from the provider-type registry.
  const groups = useMemo(() => {
    const byType: Record<string, typeof normalizedProviders> = {};
    for (const p of [...normalizedProviders].sort((a, b) =>
      (a.name || a.id).localeCompare(b.name || b.id),
    )) {
      (byType[p.type] ??= []).push(p);
    }
    return Object.entries(byType)
      .map(([type, items]) => ({
        type,
        label: sortedTypes.find((pt) => pt.id === type)?.name || type,
        providers: items,
      }))
      .sort((a, b) => a.label.localeCompare(b.label));
  }, [normalizedProviders, sortedTypes]);

  const selectedProvider =
    providerId && providerId !== "new"
      ? normalizedProviders.find((p) => p.id === providerId)
      : undefined;
  const isCreating = providerId === "new";
  const sheetOpen = isCreating || !!selectedProvider;
  const existingIds = useMemo(
    () => new Set(normalizedProviders.map((p) => p.id)),
    [normalizedProviders],
  );

  let detail: React.ReactNode = undefined;
  if (isCreating) {
    detail = (
      <NewProviderForm
        providerTypes={sortedTypes}
        providerDefaults={providerDefaults}
        existingIds={existingIds}
        onCreated={(id) => void navigate({ to: detailRoute, params: { providerId: id } })}
        onCancel={() => void navigate({ to: listRoute })}
      />
    );
  } else if (selectedProvider) {
    detail = (
      <ProviderDetailPanel
        key={selectedProvider.id}
        provider={selectedProvider}
        providerTypes={sortedTypes}
        providerDefaults={providerDefaults}
        onDeleted={() => void navigate({ to: listRoute })}
      />
    );
  }

  return (
    <>
      <SettingsGridPage
        title={t("providers.title")}
        action={
          <Button
            render={<Link to={detailRoute} params={{ providerId: "new" }} />}
            variant="outline"
            size="sm"
            // The form is built out of the provider-type registry. Without it
            // there is nothing to pick, so offering the button promises a
            // choice the page cannot deliver.
            disabled={isPending || isError}
          >
            <Plus className="size-4" />
            {t("providers.new")}
          </Button>
        }
      >
        {isPending ? (
          <div className="flex justify-center py-8">
            <Spinner />
          </div>
        ) : isError ? (
          <ErrorState
            title={t("route.error.title")}
            description={t("route.loadFailed")}
            onRetry={() => {
              void providersQuery.refetch();
              void typesQuery.refetch();
            }}
          />
        ) : (
          groups.map((group) => (
            <SettingsCardSection
              key={group.type}
              title={group.label}
              count={group.providers.length}
            >
              {group.providers.map((p) => {
                const modelCount = p.total_model_count ?? 0;
                return (
                  <SettingsCard
                    key={p.id}
                    icon={<Boxes className="size-4" />}
                    title={p.name || p.id}
                    active={providerId === p.id}
                    to={detailRoute}
                    params={{ providerId: p.id }}
                    footer={
                      <>
                        <span
                          className={`size-1.5 shrink-0 rounded-full ${
                            p.enabled ? "bg-success" : "bg-muted-foreground"
                          }`}
                        />
                        <span className="text-xs text-muted-foreground">
                          {t("providers.modelsConfigured", { count: String(modelCount) })}
                        </span>
                      </>
                    }
                  />
                );
              })}
            </SettingsCardSection>
          ))
        )}
      </SettingsGridPage>

      <SettingsDetailSheet open={sheetOpen} onClose={() => void navigate({ to: listRoute })}>
        {detail}
      </SettingsDetailSheet>
    </>
  );
}
