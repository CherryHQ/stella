import { useMemo, useState } from "react";
import { Plus, RefreshCw, Search } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import {
  Select,
  SelectItem,
  SelectPopup,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { ErrorState } from "@/components/RouteFallback";
import { ConfirmDialog } from "@/features/settings/ConfirmDialog";
import { useI18n } from "@/lib/i18n";
import type { ProviderModel } from "@/lib/types";
import { ProviderCustomModelDialog } from "./ProviderCustomModelDialog";
import { ProviderModelRow } from "./ProviderModelRow";
import {
  type CostKey,
  type ModelSource,
  type ModelStatusFilter,
  type OverrideKey,
  type OverrideValues,
  type ProviderOverrides,
  effectiveValue,
  matchesModelFilters,
  overrideOf,
  withCostOverride,
  withEnabledOverrides,
  withFieldOverride,
  withoutModelOverride,
} from "./provider-model-view";

interface ProviderModelEditorProps {
  models: ProviderModel[];
  overrides: ProviderOverrides;
  isLoading: boolean;
  isError: boolean;
  saving: boolean;
  onRetry: () => void;
  onCommit: (next: ProviderOverrides) => void;
  onFetchModels: () => Promise<void>;
  showToast: (text: string, kind?: "success" | "error") => void;
}

export function ProviderModelEditor({
  models,
  overrides,
  isLoading,
  isError,
  saving,
  onRetry,
  onCommit,
  onFetchModels,
  showToast,
}: ProviderModelEditorProps) {
  const { t } = useI18n();
  const [search, setSearch] = useState("");
  const [sourceFilter, setSourceFilter] = useState<ModelSource | "all">("all");
  const [statusFilter, setStatusFilter] = useState<ModelStatusFilter>("all");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [expanded, setExpanded] = useState<string | null>(null);
  const [fetching, setFetching] = useState(false);
  const [customModelOpen, setCustomModelOpen] = useState(false);
  const [deleting, setDeleting] = useState<string | null>(null);

  const visible = useMemo(
    () =>
      models.filter((model) =>
        matchesModelFilters(model, overrideOf(overrides, model.id), {
          search,
          source: sourceFilter,
          status: statusFilter,
        }),
      ),
    [models, overrides, search, sourceFilter, statusFilter],
  );

  const enabledCount = useMemo(
    () =>
      models.filter((model) => effectiveValue(model, overrideOf(overrides, model.id), "enabled"))
        .length,
    [models, overrides],
  );

  const selectedVisible = visible.filter((model) => selected.has(model.id));
  const allVisibleSelected = visible.length > 0 && selectedVisible.length === visible.length;
  const existingIDs = useMemo(() => new Set(models.map((model) => model.id)), [models]);

  const setFieldOverride = <K extends OverrideKey>(
    modelID: string,
    key: K,
    value: OverrideValues[K] | undefined,
  ) => {
    onCommit(withFieldOverride(overrides, modelID, overrideOf(overrides, modelID), key, value));
  };

  const setCostOverride = (modelID: string, key: CostKey, value: number | undefined) => {
    onCommit(withCostOverride(overrides, modelID, overrideOf(overrides, modelID), key, value));
  };

  const bulkSetEnabled = (enabled: boolean) => {
    onCommit(withEnabledOverrides(overrides, selectedVisible, enabled));
    setSelected(new Set());
  };

  // The Select emits its own item value; narrow it back to the filter union
  // here rather than asserting, so an unknown item is ignored instead of stored.
  const selectSource = (value: string | null) => {
    if (value === "all" || value === "catalog" || value === "fetched" || value === "custom") {
      setSourceFilter(value);
    }
  };
  const selectStatus = (value: string | null) => {
    if (value === "all" || value === "enabled" || value === "disabled" || value === "overridden") {
      setStatusFilter(value);
    }
  };

  const handleFetch = async () => {
    setFetching(true);
    try {
      await onFetchModels();
    } finally {
      setFetching(false);
    }
  };

  const toolbar = (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <Input
          value={search}
          onChange={(e: React.ChangeEvent<HTMLInputElement>) => setSearch(e.target.value)}
          placeholder={t("providers.searchModels")}
          aria-label={t("providers.searchModels")}
          nativeInput
          className="min-w-48 flex-1"
        />
        <Select value={sourceFilter} onValueChange={selectSource}>
          <SelectTrigger size="sm" className="w-36" aria-label={t("providers.allSources")}>
            <SelectValue>
              {(value) =>
                value === "catalog"
                  ? t("providers.catalog")
                  : value === "fetched"
                    ? t("providers.fetched")
                    : value === "custom"
                      ? t("providers.custom")
                      : t("providers.allSources")
              }
            </SelectValue>
          </SelectTrigger>
          <SelectPopup>
            <SelectItem value="all">{t("providers.allSources")}</SelectItem>
            <SelectItem value="catalog">{t("providers.catalog")}</SelectItem>
            <SelectItem value="fetched">{t("providers.fetched")}</SelectItem>
            <SelectItem value="custom">{t("providers.custom")}</SelectItem>
          </SelectPopup>
        </Select>
        <Select value={statusFilter} onValueChange={selectStatus}>
          <SelectTrigger size="sm" className="w-36" aria-label={t("providers.allStatuses")}>
            <SelectValue>
              {(value) =>
                value === "enabled"
                  ? t("common.enable")
                  : value === "disabled"
                    ? t("common.disable")
                    : value === "overridden"
                      ? t("providers.overriddenOnly")
                      : t("providers.allStatuses")
              }
            </SelectValue>
          </SelectTrigger>
          <SelectPopup>
            <SelectItem value="all">{t("providers.allStatuses")}</SelectItem>
            <SelectItem value="enabled">{t("common.enable")}</SelectItem>
            <SelectItem value="disabled">{t("common.disable")}</SelectItem>
            <SelectItem value="overridden">{t("providers.overriddenOnly")}</SelectItem>
          </SelectPopup>
        </Select>
      </div>
      {/* The bulk bar only exists once a row is checked; an always-on toolbar of
          disabled buttons is noise on the common path of toggling one model. */}
      {selectedVisible.length > 0 && (
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-xs text-muted-foreground">
            {t("providers.selectedModels", { count: String(selectedVisible.length) })}
          </span>
          <Button
            onClick={() => bulkSetEnabled(true)}
            disabled={saving}
            variant="outline"
            size="xs"
          >
            {t("providers.bulkEnable")}
          </Button>
          <Button
            onClick={() => bulkSetEnabled(false)}
            disabled={saving}
            variant="outline"
            size="xs"
          >
            {t("providers.bulkDisable")}
          </Button>
          <Button
            onClick={() => setSelected(new Set(visible.map((model) => model.id)))}
            disabled={allVisibleSelected}
            variant="ghost"
            size="xs"
          >
            {t("providers.selectAll")}
          </Button>
          <Button onClick={() => setSelected(new Set())} variant="ghost" size="xs">
            {t("common.cancel")}
          </Button>
        </div>
      )}
    </div>
  );

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-baseline gap-2">
          <span className="text-xs font-semibold text-muted-foreground">
            {t("providers.models")}
          </span>
          <span className="font-mono text-xs text-muted-foreground">
            {t("providers.modelsSummary", {
              enabled: String(enabledCount),
              total: String(models.length),
            })}
          </span>
        </div>
        <div className="flex items-center gap-1">
          <Button onClick={() => void handleFetch()} loading={fetching} variant="ghost" size="xs">
            <RefreshCw />
            {fetching ? t("providers.fetching") : t("providers.fetchModels")}
          </Button>
          <Button onClick={() => setCustomModelOpen(true)} variant="ghost" size="xs">
            <Plus />
            {t("providers.addCustomModel")}
          </Button>
        </div>
      </div>

      <p className="text-xs text-muted-foreground">{t("providers.modelsHint")}</p>

      {isError ? (
        <ErrorState
          title={t("route.error.title")}
          description={t("providers.modelsLoadFailed")}
          onRetry={onRetry}
        />
      ) : isLoading ? (
        <div className="flex justify-center py-6">
          <Spinner />
        </div>
      ) : models.length === 0 ? (
        <p className="py-2 text-xs text-muted-foreground">{t("providers.noModels")}</p>
      ) : (
        <>
          {toolbar}
          {visible.length === 0 ? (
            <p className="py-4 text-xs text-muted-foreground">
              <Search className="mr-1 inline size-3.5" />
              {t("providers.noModelsMatch")}
            </p>
          ) : (
            <div className="divide-y divide-border overflow-hidden rounded-lg border border-border">
              {visible.map((model) => (
                <ProviderModelRow
                  key={`${model.id}:${model.source}`}
                  model={model}
                  override={overrideOf(overrides, model.id)}
                  selected={selected.has(model.id)}
                  expanded={expanded === model.id}
                  disabled={saving}
                  onSelectedChange={(next) =>
                    setSelected((current) => {
                      const updated = new Set(current);
                      if (next) updated.add(model.id);
                      else updated.delete(model.id);
                      return updated;
                    })
                  }
                  onExpandedChange={(open) => setExpanded(open ? model.id : null)}
                  onFieldChange={(key, value) => setFieldOverride(model.id, key, value)}
                  onCostChange={(key, value) => setCostOverride(model.id, key, value)}
                  onClearOverrides={() => onCommit(withoutModelOverride(overrides, model.id))}
                  onDelete={() => setDeleting(model.id)}
                  onInvalid={(message) => showToast(message, "error")}
                />
              ))}
            </div>
          )}
        </>
      )}

      <ProviderCustomModelDialog
        open={customModelOpen}
        existingIDs={existingIDs}
        onOpenChange={setCustomModelOpen}
        onSubmit={(modelID) => {
          // A custom model is created as the leanest possible override: only
          // `enabled`. Everything else stays undeclared so the row inherits any
          // catalog entry that later covers this ID, and so no modality list is
          // invented on the operator's behalf.
          onCommit({ ...overrides, [modelID]: { enabled: true } });
          setExpanded(modelID);
        }}
      />

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={t("providers.deleteModel")}
        message={t("providers.deleteModelConfirm", { model: deleting ?? "" })}
        onConfirm={() => {
          if (deleting) onCommit(withoutModelOverride(overrides, deleting));
          setDeleting(null);
        }}
      />
    </div>
  );
}
