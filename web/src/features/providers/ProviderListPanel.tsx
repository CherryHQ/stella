import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Database, Plus, RefreshCw } from "lucide-react";
import { syncModelCatalog } from "@/lib/api-client/sdk.gen";
import type { Provider } from "@/lib/types";
import { modelCatalogProvidersOptions, modelCatalogStatusOptions } from "@/lib/queries/providers";
import { useI18n } from "@/lib/i18n";
import { useToast } from "@/hooks/use-toast";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  SettingsListBody,
  SettingsListHeader,
  SettingsListItem,
} from "@/features/settings/SettingsListPanel";

export function ProviderListPanel({
  providers,
  selectedID,
  onSelect,
  onCreate,
}: {
  providers: Provider[];
  selectedID?: string;
  onSelect: (id: string) => void;
  onCreate: () => void;
}) {
  const { t } = useI18n();
  const { showToast } = useToast();
  const queryClient = useQueryClient();
  const { data: catalogStatus } = useQuery(modelCatalogStatusOptions);
  const { data: catalogProviders = [] } = useQuery(modelCatalogProvidersOptions);
  const syncMutation = useMutation({
    mutationFn: async () => {
      await syncModelCatalog({ throwOnError: true });
    },
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: modelCatalogStatusOptions.queryKey }),
        queryClient.invalidateQueries({ queryKey: modelCatalogProvidersOptions.queryKey }),
        queryClient.invalidateQueries({ queryKey: ["provider-models"] }),
      ]);
      showToast(t("providers.catalogSynced"));
    },
    onError: (error) =>
      showToast(error instanceof Error ? error.message : t("providers.catalogSyncFailed"), "error"),
  });

  const groups = [...providers]
    .sort((a, b) => (a.name || a.id).localeCompare(b.name || b.id))
    .reduce<Map<string, Provider[]>>((result, provider) => {
      const entries = result.get(provider.type) ?? [];
      entries.push(provider);
      result.set(provider.type, entries);
      return result;
    }, new Map());

  return (
    <>
      <SettingsListHeader
        title={t("providers.title")}
        action={
          <Button
            size="icon-xs"
            variant="ghost"
            onClick={onCreate}
            aria-label={t("providers.addProvider")}
          >
            <Plus className="size-4" />
          </Button>
        }
      />
      <div className="border-b border-border p-3">
        <div className="rounded-lg border border-border bg-muted/40 p-3">
          <div className="flex items-center gap-2">
            <Database className="size-4 text-muted-foreground" />
            <span className="text-xs font-semibold">{t("providers.catalog")}</span>
            <Badge variant="outline" size="sm" className="ml-auto">
              {catalogStatus?.source || "embedded"}
            </Badge>
          </div>
          <p className="mt-1 text-xs text-muted-foreground">
            {t("providers.catalogSummary", {
              providers: String(catalogStatus?.provider_count ?? catalogProviders.length),
              models: String(catalogStatus?.model_count ?? 0),
            })}
          </p>
          <Button
            className="mt-2 w-full"
            size="xs"
            variant="outline"
            loading={syncMutation.isPending}
            onClick={() => syncMutation.mutate()}
          >
            <RefreshCw className="size-3.5" />
            {t("providers.syncCatalog")}
          </Button>
        </div>
      </div>
      <SettingsListBody>
        {[...groups.entries()].map(([type, entries]) => (
          <div key={type} className="space-y-1">
            <p className="px-3 pt-2 text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">
              {type}
            </p>
            {entries.map((provider) => (
              <SettingsListItem
                key={provider.id}
                active={provider.id === selectedID}
                onClick={() => onSelect(provider.id)}
              >
                <div className="flex items-center gap-2">
                  <span className="min-w-0 flex-1 truncate text-sm">
                    {provider.name || provider.id}
                  </span>
                  <span
                    className={`size-1.5 rounded-full ${provider.enabled ? "bg-success" : "bg-muted-foreground/40"}`}
                  />
                </div>
                <div className="mt-0.5 flex items-center gap-2 text-[11px] font-normal text-muted-foreground">
                  <span className="truncate font-mono">{provider.id}</span>
                  <span className="ml-auto shrink-0">
                    {t("providers.modelCount", {
                      count: String(
                        provider.total_model_count ?? Object.keys(provider.models ?? {}).length,
                      ),
                    })}
                  </span>
                </div>
              </SettingsListItem>
            ))}
          </div>
        ))}
      </SettingsListBody>
    </>
  );
}
