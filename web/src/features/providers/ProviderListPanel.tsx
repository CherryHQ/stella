import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { useI18n } from "@/lib/i18n";
import {
  SettingsListHeader,
  SettingsListItem,
  SettingsListBody,
} from "@/features/settings/SettingsListPanel";
import type { Provider, ProviderType } from "@/lib/types";
import { Plus } from "lucide-react";

interface ProviderListPanelProps {
  providers: Provider[];
  providerTypes: ProviderType[];
  providerModels: Record<string, { length: number }>;
  selectedId?: string;
  onSelect: (id: string) => void;
  onNew: () => void;
}

export function ProviderListPanel({
  providers,
  providerTypes,
  providerModels,
  selectedId,
  onSelect,
  onNew,
}: ProviderListPanelProps) {
  const { t } = useI18n();

  const sorted = [...providers].sort(
    (a, b) => (a.name || a.id).localeCompare(b.name || b.id) || a.id.localeCompare(b.id),
  );

  const grouped = sorted.reduce<Record<string, Provider[]>>((acc, p) => {
    const type = p.type || "other";
    if (!acc[type]) acc[type] = [];
    acc[type].push(p);
    return acc;
  }, {});

  const groups = Object.entries(grouped)
    .map(([type, items]) => {
      const meta = providerTypes.find((pt) => pt.id === type);
      return { type, label: meta?.name || type, providers: items };
    })
    .sort((a, b) => a.label.localeCompare(b.label));

  return (
    <>
      <SettingsListHeader
        title={t("providers.title")}
        action={
          <Button onClick={onNew} variant="ghost" size="icon-sm">
            <Plus className="size-4" />
          </Button>
        }
      />
      <SettingsListBody>
        {groups.map((group) => (
          <div key={group.type} className="space-y-0.5">
            <div className="flex items-center gap-2 px-3 py-1.5">
              <span className="text-xs font-medium text-muted-foreground">{group.label}</span>
              <Badge variant="secondary" size="sm">
                {group.providers.length}
              </Badge>
            </div>
            {group.providers.map((p) => {
              const modelCount =
                (providerModels[p.id] as { length: number } | undefined)?.length ?? 0;
              return (
                <SettingsListItem
                  key={p.id}
                  active={selectedId === p.id}
                  onClick={() => onSelect(p.id)}
                >
                  <div className="flex items-center gap-2">
                    <span
                      className={`shrink-0 size-1.5 rounded-full ${p.enabled ? "bg-green-500" : "bg-muted-foreground"}`}
                    />
                    <span className="text-sm truncate">{p.name || p.id}</span>
                  </div>
                  <span className="text-xs text-muted-foreground">
                    {t("providers.modelsConfigured", { count: String(modelCount) })}
                  </span>
                </SettingsListItem>
              );
            })}
          </div>
        ))}
      </SettingsListBody>
    </>
  );
}
