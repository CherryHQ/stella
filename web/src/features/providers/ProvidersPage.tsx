import { useMemo } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { providersQueryOptions, providerTypesQueryOptions } from "@/lib/queries/providers";
import { useI18n } from "@/lib/i18n";
import { useToast, ToastContainer } from "@/hooks/use-toast";
import { SettingsDetailLayout } from "@/features/settings/SettingsDetailLayout";
import { SettingsEmptyState } from "@/features/settings/SettingsEmptyState";
import { ProviderListPanel } from "./ProviderListPanel";
import { ProviderDetailPanel } from "./ProviderDetailPanel";
import { NewProviderForm } from "./NewProviderForm";

export function ProvidersPage() {
  const { t } = useI18n();
  const { toasts } = useToast();
  const navigate = useNavigate();
  const params = useParams({ strict: false }) as { providerId?: string };
  const providerId = params.providerId;

  const { data: providers = [] } = useQuery(providersQueryOptions);
  const { data: providerTypes = [] } = useQuery(providerTypesQueryOptions);

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

  const selectedProvider =
    providerId && providerId !== "new"
      ? normalizedProviders.find((p) => p.id === providerId)
      : undefined;

  const isCreating = providerId === "new";
  const existingIds = useMemo(
    () => new Set(normalizedProviders.map((p) => p.id)),
    [normalizedProviders],
  );

  const providerModelCounts = useMemo(() => {
    const counts: Record<string, { length: number }> = {};
    for (const p of normalizedProviders) {
      counts[p.id] = { length: Object.keys(p.models || {}).length };
    }
    return counts;
  }, [normalizedProviders]);

  let detail: React.ReactNode = undefined;
  if (isCreating) {
    detail = (
      <NewProviderForm
        providerTypes={sortedTypes}
        providerDefaults={providerDefaults}
        existingIds={existingIds}
        onCreated={(id) =>
          void navigate({ to: "/settings/providers/$providerId", params: { providerId: id } })
        }
        onCancel={() => void navigate({ to: "/settings/providers" })}
      />
    );
  } else if (selectedProvider) {
    detail = (
      <ProviderDetailPanel
        key={selectedProvider.id}
        provider={selectedProvider}
        providerTypes={sortedTypes}
        providerDefaults={providerDefaults}
        onDeleted={() => void navigate({ to: "/settings/providers" })}
      />
    );
  }

  return (
    <>
      <SettingsDetailLayout
        list={
          <ProviderListPanel
            providers={normalizedProviders}
            providerTypes={sortedTypes}
            providerModels={providerModelCounts}
            selectedId={providerId}
            onSelect={(id) =>
              void navigate({ to: "/settings/providers/$providerId", params: { providerId: id } })
            }
            onNew={() =>
              void navigate({
                to: "/settings/providers/$providerId",
                params: { providerId: "new" },
              })
            }
          />
        }
        detail={detail}
        emptyState={
          <SettingsEmptyState
            message={t("providers.noProviders")}
            description={t("providers.noProvidersDesc")}
          />
        }
        onBack={() => void navigate({ to: "/settings/providers" })}
      />
      <ToastContainer messages={toasts} />
    </>
  );
}
