import { useMemo } from "react";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { providersQueryOptions, providerTypesQueryOptions } from "@/lib/queries/providers";
import { useI18n } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
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
      <SettingsGridPage
        title={t("providers.title")}
        action={
          <Button
            render={<Link to="/settings/providers/$providerId" params={{ providerId: "new" }} />}
            variant="outline"
            size="sm"
          >
            <Plus className="size-4" />
            {t("providers.new")}
          </Button>
        }
      >
        {groups.map((group) => (
          <SettingsCardSection key={group.type} title={group.label} count={group.providers.length}>
            {group.providers.map((p) => {
              const modelCount = Object.keys(p.models || {}).length;
              return (
                <SettingsCard
                  key={p.id}
                  icon={<Boxes className="size-4" />}
                  title={p.name || p.id}
                  active={providerId === p.id}
                  to="/settings/providers/$providerId"
                  params={{ providerId: p.id }}
                  footer={
                    <>
                      <span
                        className={`size-1.5 shrink-0 rounded-full ${
                          p.enabled ? "bg-chart-3" : "bg-muted-foreground"
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
        ))}
      </SettingsGridPage>

      <SettingsDetailSheet
        open={sheetOpen}
        onClose={() => void navigate({ to: "/settings/providers" })}
      >
        {detail}
      </SettingsDetailSheet>
    </>
  );
}
