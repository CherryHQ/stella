import { ChevronRight } from "lucide-react";
import type { CatalogModel, ProviderModelOverride } from "@/lib/api-client/types.gen";
import type { ProviderModel } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Checkbox } from "@/components/ui/checkbox";
import { Collapsible, CollapsiblePanel, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Switch } from "@/components/ui/switch";
import { useI18n } from "@/lib/i18n";
import { ProviderModelDetail } from "./ProviderModelDetail";
import {
  SOURCE_LABEL_KEYS,
  type CostKey,
  type OverrideKey,
  type OverrideValues,
  effectiveCost,
  effectiveValue,
  formatPriceSummary,
  formatTokenLimit,
  overrideCount,
  selectedCatalogModel,
} from "./provider-model-view";

interface ProviderModelRowProps {
  model: ProviderModel;
  override: ProviderModelOverride | undefined;
  catalogModels: CatalogModel[];
  selected: boolean;
  expanded: boolean;
  disabled: boolean;
  onSelectedChange: (selected: boolean) => void;
  onExpandedChange: (expanded: boolean) => void;
  onFieldChange: <K extends OverrideKey>(key: K, value: OverrideValues[K] | undefined) => void;
  onCatalogMatchChange: (catalogModel: string | undefined) => void;
  onCostChange: (key: CostKey, value: number | undefined) => void;
  onClearOverrides: () => void;
  onDelete: () => void;
  onInvalid: (message: string) => void;
}

export function ProviderModelRow({
  model,
  override,
  catalogModels,
  selected,
  expanded,
  disabled,
  onSelectedChange,
  onExpandedChange,
  onFieldChange,
  onCatalogMatchChange,
  onCostChange,
  onClearOverrides,
  onDelete,
  onInvalid,
}: ProviderModelRowProps) {
  const { t } = useI18n();
  const selectedCatalog = selectedCatalogModel(model, override, catalogModels);
  const viewedModel =
    selectedCatalog === model.catalog ? model : { ...model, catalog: selectedCatalog };
  const enabled = effectiveValue(viewedModel, override, "enabled") ?? model.enabled;
  const name = effectiveValue(viewedModel, override, "name") || "";
  const overrides = overrideCount(override);
  const price = formatPriceSummary(effectiveCost(viewedModel, override), t("providers.free"));
  const contextWindow = effectiveValue(viewedModel, override, "contextWindow");

  // The collapsed row answers the three questions a list is scanned for: which
  // model, is it on, what does it cost. Everything else waits for the panel.
  const meta = [
    name && name !== model.id ? name : "",
    contextWindow ? t("providers.contextShort", { value: formatTokenLimit(contextWindow) }) : "",
    price,
  ].filter(Boolean);

  return (
    <Collapsible open={expanded} onOpenChange={onExpandedChange}>
      <div
        className={`flex items-center gap-2 px-2 py-1.5 transition-colors duration-150 ${expanded ? "bg-muted/50" : "hover:bg-muted/50"}`}
      >
        <Checkbox
          checked={selected}
          aria-label={t("providers.selectModel", { model: model.id })}
          onCheckedChange={(checked) => onSelectedChange(checked === true)}
        />
        <CollapsibleTrigger className="flex min-w-0 flex-1 items-center gap-2 rounded-md px-1 py-1 text-left outline-none focus-visible:ring-[3px] focus-visible:ring-ring">
          <ChevronRight
            className={`size-4 shrink-0 text-muted-foreground transition-transform duration-150 ${expanded ? "rotate-90" : ""}`}
          />
          <span
            className={`min-w-0 flex-1 truncate font-mono text-sm ${enabled ? "text-foreground" : "text-muted-foreground"}`}
          >
            {model.id}
          </span>
          {meta.length > 0 && (
            <span className="hidden shrink-0 text-xs text-muted-foreground md:inline">
              {meta.join(" · ")}
            </span>
          )}
          <Badge variant="outline" size="sm">
            {t(SOURCE_LABEL_KEYS[model.source])}
          </Badge>
          {overrides > 0 && (
            <Badge variant="info" size="sm">
              {t("providers.overrideCount", { count: String(overrides) })}
            </Badge>
          )}
        </CollapsibleTrigger>
        <Switch
          checked={enabled}
          aria-label={t("providers.enableModel", { model: model.id })}
          onCheckedChange={(checked) => onFieldChange("enabled", checked)}
        />
      </div>
      <CollapsiblePanel>
        {expanded && (
          <ProviderModelDetail
            model={model}
            override={override}
            catalogModels={catalogModels}
            summary={meta}
            disabled={disabled}
            onFieldChange={onFieldChange}
            onCatalogMatchChange={onCatalogMatchChange}
            onCostChange={onCostChange}
            onClearOverrides={onClearOverrides}
            onDelete={model.source === "custom" ? onDelete : undefined}
            onInvalid={onInvalid}
          />
        )}
      </CollapsiblePanel>
    </Collapsible>
  );
}
