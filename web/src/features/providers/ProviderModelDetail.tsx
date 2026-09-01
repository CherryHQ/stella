import { useEffect, useMemo, useState } from "react";
import { Button } from "@/components/ui/button";
import {
  Combobox,
  ComboboxEmpty,
  ComboboxInput,
  ComboboxItem,
  ComboboxList,
  ComboboxPopup,
} from "@/components/ui/combobox";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectItem,
  SelectPopup,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useI18n } from "@/lib/i18n";
import type { CatalogModel, ProviderModelOverride } from "@/lib/api-client/types.gen";
import type { ProviderModel } from "@/lib/types";
import {
  CAPABILITY_LABEL_KEYS,
  COST_KEYS,
  ORIGIN_LABEL_KEYS,
  RATE_LABEL_KEYS,
  type CostKey,
  type OverrideKey,
  type OverrideValues,
  capabilitiesOf,
  effectiveCost,
  fieldOrigin,
  formatModalities,
  formatRate,
  formatTokenLimit,
  inheritedCost,
  inheritedValue,
  isCostOverridden,
  isOverridden,
  matchingCatalogModels,
  parseModalities,
  parseNumberDraft,
  selectedCatalogModel,
} from "./provider-model-view";

interface ProviderModelDetailProps {
  model: ProviderModel;
  override: ProviderModelOverride | undefined;
  /** Provider-agnostic models created by labs, independent of API hosts. */
  catalogModels: CatalogModel[];
  /** The collapsed row's facts, repeated here because it hides them on mobile. */
  summary: string[];
  disabled: boolean;
  onFieldChange: <K extends OverrideKey>(key: K, value: OverrideValues[K] | undefined) => void;
  onCatalogMatchChange: (catalogModel: string | undefined) => void;
  onCostChange: (key: CostKey, value: number | undefined) => void;
  onClearOverrides: () => void;
  onDelete?: () => void;
  onInvalid: (message: string) => void;
}

/**
 * The model's own settings, written in the same Field + Input grammar as the
 * connection form above it. A field left blank inherits — its placeholder shows
 * what it inherits — and typing in one pins that single field. That is what
 * keeps the provider record sparse: there is no control here that writes a
 * value the operator did not type.
 */
export function ProviderModelDetail({
  model,
  override,
  catalogModels,
  summary,
  disabled,
  onFieldChange,
  onCatalogMatchChange,
  onCostChange,
  onClearOverrides,
  onDelete,
  onInvalid,
}: ProviderModelDetailProps) {
  const { t } = useI18n();
  const selectedCatalog = selectedCatalogModel(model, override, catalogModels);
  const viewedModel =
    selectedCatalog === model.catalog ? model : { ...model, catalog: selectedCatalog };
  const capabilities = capabilitiesOf(viewedModel, override);
  const cost = effectiveCost(viewedModel, override);
  const inherited = inheritedCost(viewedModel);
  const reasoning = isOverridden(override, "reasoning")
    ? override?.reasoning
      ? "on"
      : "off"
    : "inherit";
  const inheritedReasoning = inheritedValue(viewedModel, "reasoning") === true;

  const facts = [
    ...summary,
    viewedModel.catalog?.family,
    ...capabilities.map((capability) => t(CAPABILITY_LABEL_KEYS[capability])),
  ].filter(Boolean);

  return (
    <div className="flex flex-col gap-4 border-t border-border bg-muted/40 px-4 py-4">
      {(viewedModel.catalog?.description || facts.length > 0) && (
        <div className="flex flex-col gap-1">
          {viewedModel.catalog?.description && (
            <p className="max-w-prose text-xs text-muted-foreground">
              {viewedModel.catalog.description}
            </p>
          )}
          {facts.length > 0 && <p className="text-xs text-muted-foreground">{facts.join(" · ")}</p>}
        </div>
      )}

      <CatalogMatchField
        model={model}
        catalogModels={catalogModels}
        value={override?.catalogModel}
        disabled={disabled}
        onChange={onCatalogMatchChange}
      />

      <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
        <TextField
          label={t("providers.displayName")}
          value={override?.name ?? ""}
          placeholder={inheritedValue(viewedModel, "name") ?? model.id}
          inherited={formatInherited(t, viewedModel, "name")}
          overridden={isOverridden(override, "name")}
          disabled={disabled}
          resetText={t("common.reset")}
          resetLabel={t("providers.resetField", { field: t("providers.displayName") })}
          onCommit={(draft) => onFieldChange("name", draft.trim() || undefined)}
        />
        <Field>
          <FieldLabel>{t("providers.reasoning")}</FieldLabel>
          <Select
            value={reasoning}
            onValueChange={(value) => {
              if (value === "inherit") onFieldChange("reasoning", undefined);
              else if (value === "on") onFieldChange("reasoning", true);
              else if (value === "off") onFieldChange("reasoning", false);
            }}
          >
            <SelectTrigger aria-label={t("providers.reasoning")}>
              <SelectValue>
                {(value) =>
                  value === "on"
                    ? t("common.yes")
                    : value === "off"
                      ? t("common.no")
                      : t("providers.inheritValue", {
                          value: inheritedReasoning ? t("common.yes") : t("common.no"),
                        })
                }
              </SelectValue>
            </SelectTrigger>
            <SelectPopup>
              <SelectItem value="inherit">
                {t("providers.inheritValue", {
                  value: inheritedReasoning ? t("common.yes") : t("common.no"),
                })}
              </SelectItem>
              <SelectItem value="on">{t("common.yes")}</SelectItem>
              <SelectItem value="off">{t("common.no")}</SelectItem>
            </SelectPopup>
          </Select>
        </Field>
        <NumberField
          label={t("providers.contextWindow")}
          value={override?.contextWindow ?? undefined}
          placeholder={formatTokenLimit(inheritedValue(viewedModel, "contextWindow"))}
          inherited={formatInherited(t, viewedModel, "contextWindow")}
          overridden={isOverridden(override, "contextWindow")}
          disabled={disabled}
          resetText={t("common.reset")}
          resetLabel={t("providers.resetField", { field: t("providers.contextWindow") })}
          onInvalid={() => onInvalid(t("providers.invalidNumber"))}
          onCommit={(value) => onFieldChange("contextWindow", value)}
        />
        <NumberField
          label={t("providers.maxTokens")}
          value={override?.maxTokens ?? undefined}
          placeholder={formatTokenLimit(inheritedValue(viewedModel, "maxTokens"))}
          inherited={formatInherited(t, viewedModel, "maxTokens")}
          overridden={isOverridden(override, "maxTokens")}
          disabled={disabled}
          resetText={t("common.reset")}
          resetLabel={t("providers.resetField", { field: t("providers.maxTokens") })}
          onInvalid={() => onInvalid(t("providers.invalidNumber"))}
          onCommit={(value) => onFieldChange("maxTokens", value)}
        />
        <TextField
          label={t("providers.inputModalities")}
          value={override?.input?.join(", ") ?? ""}
          placeholder={formatModalities(inheritedValue(viewedModel, "input"))}
          inherited={formatInherited(t, viewedModel, "input")}
          overridden={isOverridden(override, "input")}
          disabled={disabled}
          resetText={t("common.reset")}
          resetLabel={t("providers.resetField", { field: t("providers.inputModalities") })}
          onCommit={(draft) =>
            onFieldChange("input", draft.trim() ? parseModalities(draft) : undefined)
          }
        />
        <TextField
          label={t("providers.outputModalities")}
          value={override?.output?.join(", ") ?? ""}
          placeholder={formatModalities(inheritedValue(viewedModel, "output"))}
          inherited={formatInherited(t, viewedModel, "output")}
          overridden={isOverridden(override, "output")}
          disabled={disabled}
          resetText={t("common.reset")}
          resetLabel={t("providers.resetField", { field: t("providers.outputModalities") })}
          onCommit={(draft) =>
            onFieldChange("output", draft.trim() ? parseModalities(draft) : undefined)
          }
        />
      </div>

      <div className="flex flex-col gap-2">
        <p className="text-xs font-semibold text-muted-foreground">
          {t("providers.pricingPerMillion")}
        </p>
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-4">
          {COST_KEYS.map((key) => (
            <NumberField
              key={key}
              label={t(RATE_LABEL_KEYS[key])}
              value={override?.cost?.[key] ?? undefined}
              placeholder={formatRate(inherited?.[key])}
              inherited={
                isCostOverridden(override, key)
                  ? t("providers.inheritedFrom", {
                      origin: t(
                        ORIGIN_LABEL_KEYS[
                          viewedModel.catalog?.cost?.[key] != null ? "catalog" : "default"
                        ],
                      ),
                      value: formatRate(inherited?.[key]),
                    })
                  : ""
              }
              overridden={isCostOverridden(override, key)}
              disabled={disabled}
              resetText={t("common.reset")}
              resetLabel={t("providers.resetField", { field: t(RATE_LABEL_KEYS[key]) })}
              onInvalid={() => onInvalid(t("providers.invalidNumber"))}
              onCommit={(value) => onCostChange(key, value)}
            />
          ))}
        </div>
        {cost?.tiers && cost.tiers.length > 0 && (
          <p className="text-xs text-muted-foreground">
            {t("providers.tiers")}:{" "}
            {cost.tiers
              .map(
                (tier) =>
                  `${t("providers.tierFrom", { limit: formatTokenLimit(tier.minContext) })} ${formatRate(tier.input ?? cost.input)}/${formatRate(tier.output ?? cost.output)}`,
              )
              .join(" · ")}
          </p>
        )}
      </div>

      <div className="flex items-center justify-end gap-2">
        {onDelete && (
          <Button variant="ghost" size="xs" disabled={disabled} onClick={onDelete}>
            {t("providers.deleteModel")}
          </Button>
        )}
        <Button variant="ghost" size="xs" disabled={disabled} onClick={onClearOverrides}>
          {t("providers.clearOverrides")}
        </Button>
      </div>
    </div>
  );
}

type CatalogMatchOption = {
  value: string;
  label: string;
  description: string;
};

function CatalogMatchField({
  model,
  catalogModels,
  value,
  disabled,
  onChange,
}: {
  model: ProviderModel;
  catalogModels: CatalogModel[];
  value: string | null | undefined;
  disabled: boolean;
  onChange: (catalogModel: string | undefined) => void;
}) {
  const { t } = useI18n();
  const automaticValue = "__automatic__";
  const unmatchedValue = "__unmatched__";
  const automaticLabel = model.catalog
    ? t("providers.catalogMatchAutomaticValue", { model: model.catalog.id })
    : t("providers.catalogMatchAutomatic");
  const unmatchedLabel = t("providers.catalogMatchNone");
  const initialLabel =
    value === ""
      ? unmatchedLabel
      : value
        ? (() => {
            const candidate = catalogModels.find((catalog) => catalog.id === value);
            return candidate?.name
              ? `${candidate.name} (${candidate.id})`
              : (candidate?.id ?? value);
          })()
        : automaticLabel;
  const [catalogSearch, setCatalogSearch] = useState(initialLabel);
  const [catalogOpen, setCatalogOpen] = useState(false);
  const choices = useMemo(
    () => matchingCatalogModels(catalogModels, catalogSearch, value),
    [catalogModels, catalogSearch, value],
  );
  const options: CatalogMatchOption[] = [
    {
      value: automaticValue,
      label: automaticLabel,
      description: t("providers.catalogMatchAutomaticHint"),
    },
    {
      value: unmatchedValue,
      label: unmatchedLabel,
      description: t("providers.catalogMatchNoneHint"),
    },
    ...choices.map((candidate) => ({
      value: candidate.id,
      label: candidate.name ? `${candidate.name} (${candidate.id})` : candidate.id,
      description: [candidate.family, formatTokenLimit(candidate.contextWindow)]
        .filter((part) => part && part !== "—")
        .join(" · "),
    })),
  ];
  const selectedValue =
    value === undefined || value === null ? automaticValue : value || unmatchedValue;
  const selected = options.find((option) => option.value === selectedValue) ?? options[0];

  useEffect(() => {
    if (!catalogOpen) setCatalogSearch(selected.label);
  }, [catalogOpen, selected.label]);

  return (
    <Field>
      <FieldLabel>{t("providers.catalogModelMatch")}</FieldLabel>
      <Combobox
        items={options}
        value={selected}
        inputValue={catalogSearch}
        disabled={disabled || catalogModels.length === 0}
        filter={null}
        itemToStringLabel={(option) => option.label}
        itemToStringValue={(option) => option.value}
        isItemEqualToValue={(item, selectedItem) => item.value === selectedItem.value}
        onOpenChange={(open) => {
          setCatalogOpen(open);
          setCatalogSearch(open ? "" : selected.label);
        }}
        onInputValueChange={(inputValue) => setCatalogSearch(inputValue)}
        onValueChange={(option) => {
          if (!option) return;
          setCatalogSearch(option.label);
          if (option.value === automaticValue) onChange(undefined);
          else if (option.value === unmatchedValue) onChange("");
          else onChange(option.value);
        }}
      >
        <ComboboxInput
          placeholder={t("providers.searchCatalogModels")}
          aria-label={t("providers.catalogModelMatch")}
          showClear={false}
        />
        <ComboboxPopup>
          <ComboboxEmpty>{t("providers.noCatalogModelsMatch")}</ComboboxEmpty>
          <ComboboxList>
            {(option: CatalogMatchOption) => (
              <ComboboxItem key={option.value} value={option}>
                <div className="min-w-0">
                  <div className="truncate font-mono text-xs">{option.label}</div>
                  {option.description && (
                    <div className="truncate text-xs text-muted-foreground">
                      {option.description}
                    </div>
                  )}
                </div>
              </ComboboxItem>
            )}
          </ComboboxList>
        </ComboboxPopup>
      </Combobox>
      <FieldDescription>
        {value === ""
          ? t("providers.catalogMatchNoneHint")
          : value
            ? t("providers.catalogMatchManualHint")
            : model.catalog
              ? t("providers.catalogMatchMatched", { model: model.catalog.id })
              : t("providers.catalogMatchUnmatched")}
      </FieldDescription>
    </Field>
  );
}

/** Names the layer a blank field falls back to, shown only once it is pinned. */
function formatInherited(
  t: ReturnType<typeof useI18n>["t"],
  model: ProviderModel,
  key: OverrideKey,
): string {
  const origin = t(ORIGIN_LABEL_KEYS[fieldOrigin(model, undefined, key)]);
  const text =
    key === "contextWindow" || key === "maxTokens"
      ? formatTokenLimit(inheritedValue(model, key))
      : key === "input" || key === "output"
        ? formatModalities(inheritedValue(model, key))
        : (inheritedValue(model, "name") ?? "—");
  return t("providers.inheritedFrom", { origin, value: text });
}

function ResetLink({
  show,
  text,
  label,
  disabled,
  onReset,
}: {
  show: boolean;
  text: string;
  label: string;
  disabled: boolean;
  onReset: () => void;
}) {
  if (!show) return null;
  return (
    <Button variant="link" size="xs" aria-label={label} disabled={disabled} onClick={onReset}>
      {text}
    </Button>
  );
}

/**
 * A text field whose blank state means "inherit". It commits on blur and Enter
 * rather than per keystroke, so a half-typed value never reaches the provider.
 */
function TextField({
  label,
  value,
  placeholder,
  inherited,
  overridden,
  disabled,
  resetLabel,
  resetText,
  onCommit,
}: {
  label: string;
  value: string;
  placeholder: string;
  inherited: string;
  overridden: boolean;
  disabled: boolean;
  resetLabel: string;
  resetText: string;
  onCommit: (draft: string) => void;
}) {
  const [draft, setDraft] = useState(value);
  useEffect(() => setDraft(value), [value]);

  const commit = () => {
    if (draft.trim() !== value.trim()) onCommit(draft);
  };

  return (
    <Field>
      <div className="flex items-center justify-between gap-2">
        <FieldLabel>{label}</FieldLabel>
        <ResetLink
          show={overridden}
          text={resetText}
          label={resetLabel}
          disabled={disabled}
          onReset={() => onCommit("")}
        />
      </div>
      <Input
        nativeInput
        value={draft}
        placeholder={placeholder}
        aria-label={label}
        onChange={(e: React.ChangeEvent<HTMLInputElement>) => setDraft(e.target.value)}
        onBlur={commit}
        onKeyDown={(e: React.KeyboardEvent<HTMLInputElement>) => {
          if (e.key === "Enter") e.currentTarget.blur();
          if (e.key === "Escape") setDraft(value);
        }}
      />
      {overridden && <FieldDescription>{inherited}</FieldDescription>}
    </Field>
  );
}

function NumberField({
  label,
  value,
  placeholder,
  inherited,
  overridden,
  disabled,
  resetLabel,
  resetText,
  onInvalid,
  onCommit,
}: {
  label: string;
  value: number | undefined;
  placeholder: string;
  inherited: string;
  overridden: boolean;
  disabled: boolean;
  resetLabel: string;
  resetText: string;
  onInvalid: () => void;
  onCommit: (value: number | undefined) => void;
}) {
  const text = value === undefined || value === null ? "" : String(value);
  const [draft, setDraft] = useState(text);
  useEffect(() => setDraft(text), [text]);

  const commit = () => {
    if (draft.trim() === text) return;
    const parsed = parseNumberDraft(draft);
    if (parsed === null) {
      onInvalid();
      setDraft(text);
      return;
    }
    onCommit(parsed);
  };

  return (
    <Field>
      <div className="flex items-center justify-between gap-2">
        <FieldLabel>{label}</FieldLabel>
        <ResetLink
          show={overridden}
          text={resetText}
          label={resetLabel}
          disabled={disabled}
          onReset={() => onCommit(undefined)}
        />
      </div>
      <Input
        nativeInput
        type="number"
        min={0}
        step="any"
        value={draft}
        placeholder={placeholder}
        aria-label={label}
        className="font-mono"
        onChange={(e: React.ChangeEvent<HTMLInputElement>) => setDraft(e.target.value)}
        onBlur={commit}
        onKeyDown={(e: React.KeyboardEvent<HTMLInputElement>) => {
          if (e.key === "Enter") e.currentTarget.blur();
          if (e.key === "Escape") setDraft(text);
        }}
      />
      {overridden && <FieldDescription>{inherited}</FieldDescription>}
    </Field>
  );
}
