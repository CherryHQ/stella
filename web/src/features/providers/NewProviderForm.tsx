import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createProvider, probeProvider } from "@/lib/api-client/sdk.gen";
import { modelCatalogProvidersOptions, providersQueryOptions } from "@/lib/queries/providers";
import type { ProviderType } from "@/lib/types";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectItem,
  SelectPopup,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useI18n } from "@/lib/i18n";
import { useToast } from "@/hooks/use-toast";
import { DetailPanel, DetailPanelHeader } from "@/features/settings/SettingsDetailPanel";

interface NewProviderFormProps {
  providerTypes: ProviderType[];
  providerDefaults: Record<string, { base_url: string; name: string }>;
  existingIds: Set<string>;
  onCreated: (id: string) => void;
  onCancel: () => void;
}

export function NewProviderForm({
  providerTypes,
  providerDefaults,
  existingIds,
  onCreated,
  onCancel,
}: NewProviderFormProps) {
  const { t } = useI18n();
  const { showToast } = useToast();
  const queryClient = useQueryClient();
  const { data: catalogProviders = [] } = useQuery(modelCatalogProvidersOptions);
  const [type, setType] = useState(providerTypes[0]?.id || "");
  const [catalogID, setCatalogID] = useState("");
  const [id, setId] = useState("");
  const [name, setName] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  // SAFETY: the native Base UI input emits its DOM change event; target.value is the field's text.
  const onIdChange = (e: React.ChangeEvent<HTMLInputElement>) => setId(e.target.value);
  // SAFETY: as above for the display-name field.
  const onNameChange = (e: React.ChangeEvent<HTMLInputElement>) => setName(e.target.value);

  useEffect(() => {
    if (providerTypes.length > 0 && !type) {
      setType(providerTypes[0].id);
    }
  }, [providerTypes, type]);

  const handleCatalogChange = (value: string | null) => {
    const nextID = value === "none" || value === null ? "" : value;
    setCatalogID(nextID);
    const catalog = catalogProviders.find((provider) => provider.id === nextID);
    if (!catalog) return;
    setType(catalog.api_type);
    setBaseUrl(catalog.base_url);
    setId((current) => current || catalog.id);
    setName((current) => current || catalog.name);
  };

  const mutation = useMutation({
    mutationFn: async ({ type, id, name }: { type: string; id: string; name: string }) => {
      const d = providerDefaults[type] || {};
      const { data: probe } = await probeProvider({
        body: { api_type: type, api_key: apiKey, base_url: baseUrl || d.base_url || "" },
        throwOnError: true,
      });
      const models = Object.fromEntries(
        (probe?.models ?? []).map((model) => [model.id, { enabled: true }]),
      );
      await createProvider({
        body: {
          id,
          type,
          name: name || d.name || id,
          enabled: true,
          api_key: apiKey,
          base_url: baseUrl || d.base_url || "",
          catalog_id: catalogID || undefined,
          model_policy: "allow_all",
          models,
        },
        throwOnError: true,
      });
      return id;
    },
    onSuccess: (createdId) => {
      void queryClient.invalidateQueries({ queryKey: providersQueryOptions.queryKey });
      showToast(t("providers.created"));
      onCreated(createdId);
    },
    onError: (e) => showToast(e instanceof Error ? e.message : String(e), "error"),
  });

  const handleSubmit = () => {
    const trimmedId = id.trim();
    if (!type) {
      showToast(t("providers.typeRequired"), "error");
      return;
    }
    if (!trimmedId) {
      showToast(t("providers.idRequired"), "error");
      return;
    }
    if (existingIds.has(trimmedId)) {
      showToast(t("providers.idExists"), "error");
      return;
    }
    if (!apiKey.trim()) {
      showToast(t("providers.apiKeyRequired"), "error");
      return;
    }
    mutation.mutate({
      type,
      id: trimmedId,
      name: name.trim() || providerDefaults[type]?.name || trimmedId,
    });
  };

  return (
    <DetailPanel
      onSave={handleSubmit}
      onCancel={onCancel}
      saveLabel={t("providers.addProvider")}
      cancelLabel={t("common.cancel")}
      canSave={!!type && !!id.trim()}
      isSaving={mutation.isPending}
    >
      <DetailPanelHeader
        title={t("providers.newProvider")}
        subtitle={<p className="text-sm text-muted-foreground">{t("providers.newProviderDesc")}</p>}
      />
      <div className="space-y-4">
        <div>
          <label className="mb-1 block text-xs font-medium">{t("providers.catalog")}</label>
          <Select value={catalogID || "none"} onValueChange={handleCatalogChange}>
            <SelectTrigger>
              <SelectValue>
                {(value) =>
                  value === "none"
                    ? t("providers.catalogNone")
                    : catalogProviders.find((provider) => provider.id === value)?.name || value
                }
              </SelectValue>
            </SelectTrigger>
            <SelectPopup>
              <SelectItem value="none">{t("providers.catalogNone")}</SelectItem>
              {catalogProviders.map((provider) => (
                <SelectItem key={provider.id} value={provider.id}>
                  {provider.name} · {provider.model_count ?? 0}
                </SelectItem>
              ))}
            </SelectPopup>
          </Select>
          <p className="mt-1 text-xs text-muted-foreground">{t("providers.catalogHint")}</p>
        </div>
        <div>
          <label className="mb-1 block text-xs font-medium">{t("providers.type")}</label>
          <Select value={type || null} onValueChange={(value) => value && setType(value)}>
            <SelectTrigger>
              <SelectValue placeholder={t("providers.selectType")}>
                {(value) => {
                  const providerType = providerTypes.find((candidate) => candidate.id === value);
                  return providerType ? `${providerType.name} (${providerType.id})` : value;
                }}
              </SelectValue>
            </SelectTrigger>
            <SelectPopup>
              {providerTypes.map((providerType) => (
                <SelectItem key={providerType.id} value={providerType.id}>
                  {providerType.name} ({providerType.id})
                </SelectItem>
              ))}
            </SelectPopup>
          </Select>
        </div>
        <div>
          <label className="text-xs font-medium mb-1 block">{t("providers.providerId")}</label>
          <Input
            type="text"
            value={id}
            placeholder="e.g. openrouter"
            onChange={onIdChange}
            nativeInput
            className="font-mono"
          />
        </div>
        <div>
          <label className="text-xs font-medium mb-1 block">{t("providers.apiKey")}</label>
          <Input
            type="password"
            value={apiKey}
            placeholder="sk-..."
            onChange={(e) => setApiKey(e.target.value)}
            nativeInput
            className="font-mono"
          />
        </div>
        <div>
          <label className="text-xs font-medium mb-1 block">{t("providers.baseUrl")}</label>
          <Input
            type="text"
            value={baseUrl}
            placeholder={providerDefaults[type]?.base_url || ""}
            onChange={(e) => setBaseUrl(e.target.value)}
            nativeInput
            className="font-mono"
          />
        </div>
        <div>
          <label className="text-xs font-medium mb-1 block">{t("providers.displayName")}</label>
          <Input
            type="text"
            value={name}
            placeholder={providerDefaults[type]?.name || ""}
            onChange={onNameChange}
            nativeInput
          />
        </div>
      </div>
    </DetailPanel>
  );
}
