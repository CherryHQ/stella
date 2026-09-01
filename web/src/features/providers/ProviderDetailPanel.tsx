import { useCallback, useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { deleteProvider, fetchProviderModels, updateProvider } from "@/lib/api-client/sdk.gen";
import {
  modelCatalogModelsOptions,
  modelCatalogProvidersOptions,
  providerModelsOptions,
  providersQueryOptions,
} from "@/lib/queries/providers";
import type { Provider, ProviderModel, ProviderType } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import {
  Select,
  SelectItem,
  SelectPopup,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useI18n } from "@/lib/i18n";
import { useToast } from "@/hooks/use-toast";
import {
  DetailPanel,
  DetailPanelHeader,
  FormSectionTitle,
} from "@/features/settings/SettingsDetailPanel";
import { ConfirmDialog } from "@/features/settings/ConfirmDialog";
import { ProviderModelEditor } from "./ProviderModelEditor";
import { ProviderSearchCombobox } from "./ProviderSearchCombobox";
import { type ProviderOverrides, withoutCatalogMatches } from "./provider-model-view";
import { parseProviderJSON, providerJSONValue } from "./provider-helpers";

interface ProviderDetailPanelProps {
  provider: Provider;
  providerTypes: ProviderType[];
  providerDefaults: Record<string, { base_url: string; name: string }>;
  onDeleted: () => void;
}

export function ProviderDetailPanel({
  provider: initialProvider,
  providerTypes,
  providerDefaults,
  onDeleted,
}: ProviderDetailPanelProps) {
  const { t } = useI18n();
  const { showToast } = useToast();
  const queryClient = useQueryClient();

  const [provider, setProvider] = useState<Provider>(initialProvider);
  const [showAdvancedJSON, setShowAdvancedJSON] = useState(false);
  const [showImportJSON, setShowImportJSON] = useState(false);
  const [providerJSON, setProviderJSON] = useState(() =>
    JSON.stringify(
      { ...providerJSONValue(initialProvider), api_key: initialProvider.api_key ? "••••" : "" },
      null,
      2,
    ),
  );
  const [confirmDeleteOpen, setConfirmDeleteOpen] = useState(false);

  const modelsQuery = useQuery(providerModelsOptions(initialProvider.id));
  const models = modelsQuery.data ?? [];
  const providerTypeChanged =
    provider.catalog_id !== initialProvider.catalog_id || provider.type !== initialProvider.type;
  const { data: catalogProviders = [] } = useQuery(modelCatalogProvidersOptions);
  const catalogModelsQuery = useQuery(modelCatalogModelsOptions);

  // `providerRef` mirrors the state so a queued save reads the version the
  // previous one returned rather than the version captured at click time.
  const providerRef = useRef(initialProvider);
  const saveChain = useRef<Promise<void>>(Promise.resolve());

  useEffect(() => {
    providerRef.current = initialProvider;
    setProvider(initialProvider);
    setProviderJSON(
      JSON.stringify(
        { ...providerJSONValue(initialProvider), api_key: initialProvider.api_key ? "••••" : "" },
        null,
        2,
      ),
    );
    setShowAdvancedJSON(false);
  }, [initialProvider]);

  const syncJSON = useCallback((p: Provider) => {
    const exported = { ...providerJSONValue(p), api_key: p.api_key ? "••••" : "" };
    setProviderJSON(JSON.stringify(exported, null, 2));
  }, []);

  const applyProvider = useCallback(
    (next: Provider) => {
      providerRef.current = next;
      setProvider(next);
      syncJSON(next);
    },
    [syncJSON],
  );

  const updateField = (field: keyof Provider, value: Provider[keyof Provider]) => {
    applyProvider({ ...provider, [field]: value });
  };
  // SAFETY: the native Base UI input emits its DOM change event; target.value is the text field's value.
  const onNameChange = (e: React.ChangeEvent<HTMLInputElement>) =>
    updateField("name", e.target.value);
  // SAFETY: as above, for the API-key field.
  const onApiKeyChange = (e: React.ChangeEvent<HTMLInputElement>) =>
    updateField("api_key", e.target.value);
  // SAFETY: as above, for the base-URL field.
  const onBaseUrlChange = (e: React.ChangeEvent<HTMLInputElement>) =>
    updateField("base_url", e.target.value);

  const saveMutation = useMutation({
    mutationFn: async (p: Provider) => {
      if (!p.version) throw new Error(t("providers.conflict"));
      const { data } = await updateProvider({
        path: { id: p.id },
        body: {
          type: p.type,
          name: p.name,
          enabled: p.enabled,
          api_key: p.api_key,
          base_url: p.base_url,
          models: p.models,
          catalog_id: p.catalog_id ?? "",
          model_policy: p.model_policy,
          expected_version: p.version,
        },
        throwOnError: true,
      });
      // SAFETY: updateProvider is generated from the Provider response schema and returns the saved row.
      return data as Provider;
    },
    onSuccess: (saved, submitted) => {
      void queryClient.invalidateQueries({ queryKey: providersQueryOptions.queryKey });
      void queryClient.invalidateQueries({ queryKey: ["provider-models", initialProvider.id] });
      if (saved) {
        const current = providerRef.current;
        applyProvider(current === submitted ? saved : { ...current, version: saved.version });
      }
      showToast(t("providers.updated"));
    },
    onError: (e) => {
      // SAFETY: SDK errors expose an optional response.status at runtime.
      const status = (e as { response?: { status?: number } })?.response?.status;
      showToast(
        status === 409 ? t("providers.conflict") : e instanceof Error ? e.message : String(e),
        "error",
      );
      if (status === 409)
        void queryClient.invalidateQueries({ queryKey: providersQueryOptions.queryKey });
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async ({ id, version }: { id: string; version: string }) => {
      await deleteProvider({
        path: { id },
        query: { expected_version: version },
        throwOnError: true,
      });
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: providersQueryOptions.queryKey });
      showToast(t("providers.deleted"));
      onDeleted();
    },
    onError: (e) => {
      // SAFETY: SDK errors expose an optional response.status at runtime.
      const status = (e as { response?: { status?: number } })?.response?.status;
      showToast(
        status === 409 ? t("providers.conflict") : e instanceof Error ? e.message : String(e),
        "error",
      );
      if (status === 409) {
        void queryClient.invalidateQueries({ queryKey: providersQueryOptions.queryKey });
      }
    },
  });

  const fetchModelsMutation = useMutation({
    mutationFn: async () => {
      const { data } = await fetchProviderModels({
        path: { id: provider.id },
        body: { api_key: provider.api_key, base_url: provider.base_url },
        throwOnError: true,
      });
      // SAFETY: refreshProviderModels returns the provider's model list under data.models.
      return (data?.models ?? []) as ProviderModel[];
    },
    onSuccess: (list) => {
      void queryClient.invalidateQueries({ queryKey: ["provider-models", initialProvider.id] });
      showToast(t("providers.modelsAvailable", { count: String(list.length) }));
    },
    onError: (e) => showToast(e instanceof Error ? e.message : String(e), "error"),
  });

  const handleSave = () => {
    try {
      const parsed = parseProviderJSON(providerJSON, provider);
      applyProvider(parsed);
      saveMutation.mutate(parsed);
    } catch (e) {
      showToast(String(e instanceof Error ? e.message : e), "error");
    }
  };

  const handleApplyJSON = () => {
    try {
      const parsed = parseProviderJSON(providerJSON, provider);
      setProvider(parsed);
      setProviderJSON(JSON.stringify(providerJSONValue(parsed), null, 2));
      showToast(t("providers.jsonApplied"));
    } catch (e) {
      showToast(String(e instanceof Error ? e.message : e), "error");
    }
  };

  /**
   * Model overrides save the moment they change: they are single-field,
   * individually reversible edits, and deferring them to the panel's Save
   * button would leave the effective-model list disagreeing with the row the
   * operator just edited.
   *
   * The map moves locally first so the UI is immediate, and the request joins a
   * queue instead of racing — two edits a few milliseconds apart would
   * otherwise send the same `expected_version` and make the second one 409
   * against the operator's own first edit. A rejected save puts the previous
   * map back rather than leaving a phantom override on screen.
   */
  const commitOverrides = (nextModels: ProviderOverrides) => {
    applyProvider({ ...providerRef.current, models: nextModels });
    saveChain.current = saveChain.current.then(async () => {
      try {
        // Send the accumulated state, not the snapshot taken at click time: an
        // edit queued behind this one is already folded into the ref, so one
        // request covers both and the next reads the version this one returns.
        await saveMutation.mutateAsync(providerRef.current);
      } catch {
        // After a rejection the server is the truth. Resync rather than leave a
        // phantom override sitting in the list.
        void queryClient.invalidateQueries({ queryKey: providersQueryOptions.queryKey });
      }
    });
  };

  return (
    <DetailPanel
      onSave={handleSave}
      onDelete={() => setConfirmDeleteOpen(true)}
      saveLabel={t("common.save")}
      deleteLabel={t("common.delete")}
      isSaving={saveMutation.isPending}
    >
      <DetailPanelHeader
        title={provider.name || provider.id}
        subtitle={
          <div className="flex flex-wrap items-center gap-1.5">
            {provider.catalog_id && (
              <Badge variant="outline" size="sm">
                {catalogProviders.find((candidate) => candidate.id === provider.catalog_id)?.name ??
                  provider.catalog_id}
              </Badge>
            )}
            <Badge variant="secondary" size="sm">
              {t("providers.apiType")}: {provider.type}
            </Badge>
          </div>
        }
        action={
          <div className="flex items-center gap-2">
            <Switch
              checked={provider.enabled}
              onCheckedChange={(checked) => updateField("enabled", checked)}
            />
            <span className="text-sm">{t("providers.enabled")}</span>
          </div>
        }
      />

      <div className="space-y-4">
        <FormSectionTitle>{t("providers.connection")}</FormSectionTitle>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div>
            <label className="mb-1 block text-xs font-medium">{t("providers.providerType")}</label>
            <ProviderSearchCombobox
              value={provider.catalog_id || "none"}
              options={[
                {
                  value: "none",
                  label: t("providers.providerTypeCustom"),
                  description: t("providers.providerTypeCustomHint"),
                },
                ...catalogProviders.map((catalog) => ({
                  value: catalog.id,
                  label: catalog.name,
                  description: `${catalog.id} · ${t("providers.modelCount", { count: String(catalog.model_count ?? 0) })}`,
                  disabled: !catalog.supported,
                })),
              ]}
              placeholder={t("providers.searchProviderTypes")}
              emptyText={t("providers.noProviderTypesMatch")}
              ariaLabel={t("providers.providerType")}
              onChange={(value) => {
                const catalogID = value === "none" ? "" : value;
                const catalog = catalogProviders.find((candidate) => candidate.id === catalogID);
                const next = {
                  ...providerRef.current,
                  catalog_id: catalogID,
                  models: withoutCatalogMatches(providerRef.current.models),
                };
                if (catalog) {
                  next.type = catalog.api_type;
                  next.base_url = catalog.base_url;
                }
                applyProvider(next);
              }}
            />
            <p className="mt-1 text-xs text-muted-foreground">{t("providers.providerTypeHint")}</p>
          </div>
          <div>
            <label className="text-xs font-medium mb-1 block">{t("providers.name")}</label>
            <Input
              type="text"
              value={provider.name}
              placeholder={provider.id}
              onChange={onNameChange}
              nativeInput
            />
          </div>
          <div>
            <label className="text-xs font-medium mb-1 block">{t("providers.apiKey")}</label>
            <Input
              type="password"
              value={provider.api_key}
              placeholder="sk-..."
              onChange={onApiKeyChange}
              nativeInput
              className="font-mono"
            />
          </div>
          <div>
            <label className="text-xs font-medium mb-1 block">{t("providers.baseUrl")}</label>
            <Input
              type="text"
              value={provider.base_url}
              placeholder={providerDefaults[provider.type]?.base_url || ""}
              onChange={onBaseUrlChange}
              nativeInput
              className="font-mono"
            />
          </div>
          <div>
            <label className="mb-1 block text-xs font-medium">{t("providers.apiType")}</label>
            <ProviderSearchCombobox
              value={provider.type}
              options={providerTypes.map((providerType) => ({
                value: providerType.id,
                label: providerType.name,
                description: providerType.id,
              }))}
              placeholder={t("providers.searchApiTypes")}
              emptyText={t("providers.noApiTypesMatch")}
              ariaLabel={t("providers.apiType")}
              disabled={!!provider.catalog_id}
              onChange={(value) => updateField("type", value)}
            />
            <p className="mt-1 text-xs text-muted-foreground">
              {provider.catalog_id ? t("providers.apiTypeDerivedHint") : t("providers.apiTypeHint")}
            </p>
          </div>
          <div>
            <label className="mb-1 block text-xs font-medium">{t("providers.modelPolicy")}</label>
            <Select
              value={provider.model_policy ?? "allow_all"}
              onValueChange={(value) => {
                if (value === "allow_all" || value === "allowlist") {
                  updateField("model_policy", value);
                }
              }}
            >
              <SelectTrigger>
                <SelectValue>
                  {(value) =>
                    value === "allowlist" ? t("providers.allowlist") : t("providers.allowAll")
                  }
                </SelectValue>
              </SelectTrigger>
              <SelectPopup>
                <SelectItem value="allow_all">{t("providers.allowAll")}</SelectItem>
                <SelectItem value="allowlist">{t("providers.allowlist")}</SelectItem>
              </SelectPopup>
            </Select>
          </div>
        </div>
      </div>

      {providerTypeChanged ? (
        <div className="rounded-lg border border-border bg-muted/40 px-4 py-3 text-sm text-muted-foreground">
          {t("providers.saveProviderTypeBeforeModels")}
        </div>
      ) : (
        <ProviderModelEditor
          models={models}
          overrides={provider.models ?? {}}
          providerCatalogID={provider.catalog_id ?? undefined}
          catalogModels={catalogModelsQuery.data ?? []}
          isLoading={modelsQuery.isPending || catalogModelsQuery.isPending}
          isError={modelsQuery.isError || catalogModelsQuery.isError}
          saving={saveMutation.isPending}
          onRetry={() => {
            void modelsQuery.refetch();
            void catalogModelsQuery.refetch();
          }}
          onCommit={commitOverrides}
          onFetchModels={async () => {
            await fetchModelsMutation.mutateAsync();
          }}
          showToast={showToast}
        />
      )}

      <div className="border-t border-border pt-4 space-y-3">
        <button
          type="button"
          onClick={() => {
            syncJSON(provider);
            setShowAdvancedJSON((v) => !v);
          }}
          className="text-xs font-mono text-muted-foreground hover:text-foreground cursor-pointer"
        >
          {showAdvancedJSON ? t("providers.hideAdvancedJson") : t("providers.showAdvancedJson")}
        </button>
        {showAdvancedJSON && (
          <div className="space-y-2">
            <label className="text-xs font-medium mb-1 block">
              {t("providers.providerJsonExport")}
            </label>
            <Textarea value={providerJSON} readOnly rows={12} className="font-mono text-xs" />
            <Button onClick={() => setShowImportJSON((v) => !v)} variant="ghost" size="xs">
              {showImportJSON ? t("providers.hideImportJson") : t("providers.importJson")}
            </Button>
            {showImportJSON && (
              <div className="space-y-2">
                <Textarea
                  value={providerJSON}
                  onChange={(e) => setProviderJSON(e.target.value)}
                  rows={12}
                  className="font-mono text-xs"
                />
                <div className="flex justify-end">
                  <Button onClick={handleApplyJSON} variant="ghost" size="xs">
                    {t("providers.applyJson")}
                  </Button>
                </div>
              </div>
            )}
          </div>
        )}
      </div>

      <ConfirmDialog
        open={confirmDeleteOpen}
        onOpenChange={setConfirmDeleteOpen}
        title={t("providers.deleteConfirm")}
        message={t("providers.deleteConfirmDesc", { name: provider.name || provider.id })}
        onConfirm={() => {
          if (!provider.version) {
            showToast(t("providers.conflict"), "error");
            return;
          }
          deleteMutation.mutate({ id: provider.id, version: provider.version });
        }}
      />
    </DetailPanel>
  );
}
