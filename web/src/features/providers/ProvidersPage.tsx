import { useMemo } from "react";
import { useNavigate, useParams, useRouterState } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { Server } from "lucide-react";
import { providersQueryOptions, providerTypesQueryOptions } from "@/lib/queries/providers";
import { useI18n } from "@/lib/i18n";
import { Spinner } from "@/components/ui/spinner";
import { ErrorState } from "@/components/RouteFallback";
import { SettingsDetailLayout } from "@/features/settings/SettingsDetailLayout";
import { SettingsEmptyState } from "@/features/settings/SettingsEmptyState";
import { ProviderDetailPanel } from "./ProviderDetailPanel";
import { NewProviderForm } from "./NewProviderForm";
import { ProviderListPanel } from "./ProviderListPanel";

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

  const providersQuery = useQuery(providersQueryOptions);
  const typesQuery = useQuery(providerTypesQueryOptions);
  const providers = providersQuery.data ?? [];
  const providerTypes = typesQuery.data ?? [];
  const isPending = providersQuery.isPending || typesQuery.isPending;
  const isError = providersQuery.isError || typesQuery.isError;

  const providerDefaults = useMemo(() => {
    const defaults: Record<string, { base_url: string; name: string }> = {};
    for (const providerType of providerTypes) {
      defaults[providerType.id] = {
        base_url: providerType.default_url,
        name: providerType.name,
      };
    }
    return defaults;
  }, [providerTypes]);

  const sortedTypes = useMemo(
    () => [...providerTypes].sort((a, b) => (a.name || a.id).localeCompare(b.name || b.id)),
    [providerTypes],
  );
  const normalizedProviders = useMemo(
    () =>
      providers.map((provider) => ({
        ...provider,
        type: provider.type || provider.id,
        enabled: provider.enabled !== false,
        models: provider.models || {},
      })),
    [providers],
  );
  const selectedProvider =
    providerId && providerId !== "new"
      ? normalizedProviders.find((provider) => provider.id === providerId)
      : undefined;
  const existingIds = useMemo(
    () => new Set(normalizedProviders.map((provider) => provider.id)),
    [normalizedProviders],
  );

  let detail: React.ReactNode = undefined;
  if (providerId === "new") {
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

  const list = isPending ? (
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
    <ProviderListPanel
      providers={normalizedProviders}
      selectedID={providerId === "new" ? undefined : providerId}
      onSelect={(id) => void navigate({ to: detailRoute, params: { providerId: id } })}
      onCreate={() => void navigate({ to: detailRoute, params: { providerId: "new" } })}
    />
  );

  return (
    <SettingsDetailLayout
      list={list}
      detail={detail}
      emptyState={
        <SettingsEmptyState
          icon={<Server className="size-6" />}
          message={
            normalizedProviders.length === 0 ? t("providers.empty") : t("providers.selectProvider")
          }
          description={t("providers.selectProviderDesc")}
        />
      }
      onBack={() => void navigate({ to: listRoute })}
    />
  );
}
