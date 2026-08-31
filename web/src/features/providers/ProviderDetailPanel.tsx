import { useCallback, useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { deleteProvider, fetchProviderModels, updateProvider } from "@/lib/api-client/sdk.gen";
import { providerModelsOptions, providersQueryOptions } from "@/lib/queries/providers";
import type {
  CustomModelForm,
  ModelConfig,
  Provider,
  ProviderModel,
  ProviderType,
} from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { useI18n } from "@/lib/i18n";
import { useToast } from "@/hooks/use-toast";
import {
  DetailPanel,
  DetailPanelHeader,
  FormSectionTitle,
} from "@/features/settings/SettingsDetailPanel";
import { ConfirmDialog } from "@/features/settings/ConfirmDialog";
import { ProviderModelEditor } from "./ProviderModelEditor";
import { providerJSONValue, parseProviderJSON, modelConfigFromForm } from "./provider-helpers";

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

  const { data: models = [] } = useQuery(providerModelsOptions(initialProvider.id));

  useEffect(() => {
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

  const updateField = (field: keyof Provider, value: Provider[keyof Provider]) => {
    const next = { ...provider, [field]: value };
    setProvider(next);
    syncJSON(next);
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
      const { data } = await updateProvider({
        path: { id: p.id },
        body: {
          type: p.type,
          name: p.name,
          enabled: p.enabled,
          api_key: p.api_key,
          base_url: p.base_url,
          models: p.models,
          catalog_id: p.catalog_id,
          model_policy: p.model_policy,
          expected_version: p.version,
        },
        throwOnError: true,
      });
      // SAFETY: updateProvider is generated from the Provider response schema and returns the saved row.
      return data as Provider;
    },
    onSuccess: (saved) => {
      void queryClient.invalidateQueries({ queryKey: providersQueryOptions.queryKey });
      void queryClient.invalidateQueries({ queryKey: ["provider-models", initialProvider.id] });
      if (saved) {
        setProvider(saved);
        syncJSON(saved);
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
    mutationFn: async (id: string) => {
      await deleteProvider({ path: { id }, throwOnError: true });
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: providersQueryOptions.queryKey });
      showToast(t("providers.deleted"));
      onDeleted();
    },
    onError: (e) => showToast(e instanceof Error ? e.message : String(e), "error"),
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
      setProvider(parsed);
      syncJSON(parsed);
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

  const handleToggleModel = async (model: ProviderModel, enabled: boolean) => {
    const nextModels = { ...provider.models };
    const current = nextModels[model.id] || {
      id: model.id,
      name: model.name || model.id,
      enabled: model.enabled,
      reasoning: false,
      input: [],
      output: [],
    };
    current.enabled = enabled;
    nextModels[model.id] = current;
    const next = { ...provider, models: nextModels };
    setProvider(next);
    syncJSON(next);
    await saveMutation.mutateAsync(next);
  };

  const handleAddCustomModel = (form: CustomModelForm) => {
    const modelID = form.id.trim();
    if (!modelID) return;
    const nextModels = { ...provider.models };
    nextModels[modelID] = modelConfigFromForm({ ...form, id: modelID });
    const next = { ...provider, models: nextModels };
    setProvider(next);
    syncJSON(next);
  };

  const handleEditCustomModel = (originalId: string, form: CustomModelForm) => {
    const modelID = form.id.trim();
    if (!modelID) return;
    const nextModels = { ...provider.models };
    if (originalId !== modelID) delete nextModels[originalId];
    nextModels[modelID] = modelConfigFromForm({ ...form, id: modelID });
    const next = { ...provider, models: nextModels };
    setProvider(next);
    syncJSON(next);
  };

  const handleRemoveCustomModel = (modelID: string) => {
    const nextModels = { ...provider.models };
    delete nextModels[modelID];
    const next = { ...provider, models: nextModels };
    setProvider(next);
    syncJSON(next);
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
          <Badge variant="outline" size="sm">
            {provider.type}
          </Badge>
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
            <label className="text-xs font-medium mb-1 block">{t("providers.type")}</label>
            <select
              value={provider.type}
              onChange={(e) => updateField("type", e.target.value)}
              className="h-9 w-full rounded-lg border border-input bg-background px-3 text-sm outline-none sm:h-8"
            >
              {providerTypes.map((pt) => (
                <option key={pt.id} value={pt.id}>
                  {pt.name} ({pt.id})
                </option>
              ))}
            </select>
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
            <label className="text-xs font-medium mb-1 block">{t("providers.catalogId")}</label>
            <Input
              type="text"
              value={provider.catalog_id ?? ""}
              placeholder={t("providers.catalogIdPlaceholder")}
              onChange={(e) => updateField("catalog_id", e.target.value || null)}
              nativeInput
              className="font-mono"
            />
          </div>
          <div>
            <label className="text-xs font-medium mb-1 block">{t("providers.modelPolicy")}</label>
            <select
              value={provider.model_policy ?? "allow_all"}
              // SAFETY: the select options are the complete Provider model-policy enum.
              onChange={(e) =>
                updateField("model_policy", e.target.value as Provider["model_policy"])
              }
              className="h-9 w-full rounded-lg border border-input bg-background px-3 text-sm outline-none sm:h-8"
            >
              <option value="allow_all">{t("providers.allowAll")}</option>
              <option value="allowlist">{t("providers.allowlist")}</option>
            </select>
          </div>
        </div>
      </div>

      <ProviderModelEditor
        models={models}
        // SAFETY: provider.models is a per-model config map stored on the provider record.
        providerModels={(provider.models || {}) as Record<string, ModelConfig>}
        onToggleModel={handleToggleModel}
        onAddCustomModel={handleAddCustomModel}
        onEditCustomModel={handleEditCustomModel}
        onRemoveCustomModel={handleRemoveCustomModel}
        onFetchModels={async () => {
          await fetchModelsMutation.mutateAsync();
        }}
        showToast={showToast}
      />

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
        onConfirm={() => deleteMutation.mutate(provider.id)}
      />
    </DetailPanel>
  );
}
