import { useEffect, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { createProvider } from "@/lib/api-client/sdk.gen";
import { providersQueryOptions } from "@/lib/queries/providers";
import type { ProviderType } from "@/lib/types";
import { Input } from "@/components/ui/input";
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
  const [type, setType] = useState(providerTypes[0]?.id || "");
  const [id, setId] = useState("");
  const [name, setName] = useState("");
  // SAFETY: the native Base UI input emits its DOM change event; target.value is the field's text.
  const onIdChange = (e: React.ChangeEvent<HTMLInputElement>) => setId(e.target.value);
  // SAFETY: as above for the display-name field.
  const onNameChange = (e: React.ChangeEvent<HTMLInputElement>) => setName(e.target.value);

  useEffect(() => {
    if (providerTypes.length > 0 && !type) {
      setType(providerTypes[0].id);
    }
  }, [providerTypes, type]);

  const mutation = useMutation({
    mutationFn: async ({ type, id, name }: { type: string; id: string; name: string }) => {
      const d = providerDefaults[type] || {};
      await createProvider({
        body: {
          id,
          type,
          name: name || d.name || id,
          enabled: false,
          api_key: "",
          base_url: "",
          models: {},
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
          <label className="text-xs font-medium mb-1 block">{t("providers.type")}</label>
          <select
            value={type}
            onChange={(e) => setType(e.target.value)}
            className="h-9 w-full rounded-lg border border-input bg-background px-3 text-sm outline-none sm:h-8"
          >
            <option value="" disabled>
              {t("providers.selectType")}
            </option>
            {providerTypes.map((pt) => (
              <option key={pt.id} value={pt.id}>
                {pt.name} ({pt.id})
              </option>
            ))}
          </select>
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
