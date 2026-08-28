import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChevronDownIcon, TriangleAlertIcon } from "lucide-react";
import { Alert, AlertAction, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardDescription, CardHeader, CardPanel, CardTitle } from "@/components/ui/card";
import { Collapsible, CollapsiblePanel, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectGroup,
  SelectGroupLabel,
  SelectItem,
  SelectPopup,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { SettingsPageHeader } from "@/features/settings/SettingsPageHeader";
import { useToast } from "@/hooks/use-toast";
import { updateDefaultModels, updateEmbeddingSettings } from "@/lib/api-client/sdk.gen";
import type { DefaultModels } from "@/lib/api-client/types.gen";
import { useI18n } from "@/lib/i18n";
import { defaultModelsQueryOptions } from "@/lib/queries/default-models";
import { embeddingSettingsQueryOptions } from "@/lib/queries/embedding";
import { modelsQueryOptions } from "@/lib/queries/models";

const EMPTY: DefaultModels = {
  model: "",
  model_thinking: "",
  model_strong: "",
  model_strong_thinking: "",
  model_fast: "",
  model_fast_thinking: "",
  model_vision: "",
  model_embedding: "",
};

const MODEL_KEYS: (keyof DefaultModels)[] = [
  "model",
  "model_thinking",
  "model_strong",
  "model_strong_thinking",
  "model_fast",
  "model_fast_thinking",
  "model_vision",
  "model_embedding",
];

const THINKING_LEVELS = ["", "minimal", "low", "medium", "high", "xhigh"];

type ModelGroup = {
  id: string;
  label: string;
  items: { value: string; label: string }[];
};

function ModelSelect({
  value,
  onChange,
  groups,
  labels,
  placeholder,
  ariaLabel,
}: {
  value: string;
  onChange: (value: string) => void;
  groups: ModelGroup[];
  labels: Map<string, string>;
  placeholder: string;
  ariaLabel?: string;
}) {
  // A model saved before its provider was removed would otherwise vanish from
  // the list and be silently cleared by the next save.
  const stale = value !== "" && !labels.has(value);
  return (
    <Select onValueChange={(next) => onChange(next ?? "")} value={value}>
      <SelectTrigger aria-label={ariaLabel} className="min-w-0">
        <SelectValue>
          {(selected: string) =>
            selected ? (
              (labels.get(selected) ?? selected)
            ) : (
              <span className="text-muted-foreground">{placeholder}</span>
            )
          }
        </SelectValue>
      </SelectTrigger>
      <SelectPopup>
        <SelectItem value="">{placeholder}</SelectItem>
        {groups.map((group) => (
          <SelectGroup key={group.id}>
            <SelectGroupLabel>{group.label}</SelectGroupLabel>
            {group.items.map((item) => (
              <SelectItem key={item.value} value={item.value}>
                {item.label}
              </SelectItem>
            ))}
          </SelectGroup>
        ))}
        {stale && (
          <>
            <SelectSeparator />
            <SelectItem value={value}>{value}</SelectItem>
          </>
        )}
      </SelectPopup>
    </Select>
  );
}

function ThinkingSelect({
  value,
  onChange,
  ariaLabel,
}: {
  value: string;
  onChange: (value: string) => void;
  ariaLabel: string;
}) {
  const { t } = useI18n();
  const label = (level: string) => level || t("agents.form.thinkingDefault");
  return (
    <Select onValueChange={(next) => onChange(next ?? "")} value={value}>
      <SelectTrigger aria-label={ariaLabel}>
        <SelectValue>{(selected: string) => label(selected)}</SelectValue>
      </SelectTrigger>
      <SelectPopup>
        {THINKING_LEVELS.map((level) => (
          <SelectItem key={level || "default"} value={level}>
            {label(level)}
          </SelectItem>
        ))}
      </SelectPopup>
    </Select>
  );
}

/** One agent tier: what it is on the left, the two controls that set it on the right. */
function TierRow({
  title,
  description,
  stale,
  children,
}: {
  title: string;
  description: string;
  stale: boolean;
  children: React.ReactNode;
}) {
  const { t } = useI18n();
  return (
    <Field className="gap-2 sm:flex-row sm:items-start sm:justify-between sm:gap-8">
      <div className="flex flex-col gap-1 sm:pt-2">
        <FieldLabel>
          {title}
          {stale && (
            <Badge size="sm" variant="warning">
              {t("defaultModels.stale")}
            </Badge>
          )}
        </FieldLabel>
        <FieldDescription>{description}</FieldDescription>
      </div>
      <div className="grid w-full grid-cols-1 items-center gap-2 sm:w-[22rem] sm:shrink-0 sm:grid-cols-[minmax(0,1fr)_9rem]">
        {children}
      </div>
    </Field>
  );
}

export function DefaultModelsPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { showToast } = useToast();
  const { data: settings } = useQuery(defaultModelsQueryOptions);
  const { data: embedding } = useQuery(embeddingSettingsQueryOptions);
  const { data: models, isError: modelsFailed } = useQuery(modelsQueryOptions);

  const [draft, setDraft] = useState<DefaultModels>(EMPTY);
  const [lane, setLane] = useState({
    enabled: false,
    dim: "",
    normalize: false,
  });
  const [advanced, setAdvanced] = useState(false);

  // Re-seed the drafts whenever the server snapshots change (initial load, or
  // after a successful save invalidates the queries).
  useEffect(() => {
    if (settings) setDraft(settings);
  }, [settings]);

  useEffect(() => {
    if (!embedding) return;
    setLane({
      enabled: embedding.enabled,
      dim: String(embedding.dim),
      normalize: embedding.normalize,
    });
  }, [embedding]);

  const set = (patch: Partial<DefaultModels>) => setDraft((prev) => ({ ...prev, ...patch }));

  const { groups, labels } = useMemo(() => {
    const byProvider = new Map<string, ModelGroup>();
    const names = new Map<string, string>();
    for (const m of models ?? []) {
      const providerName = m.provider_name || m.provider;
      const group = byProvider.get(m.provider) ?? {
        id: m.provider,
        items: [],
        label: providerName,
      };
      group.items.push({ label: m.model, value: `${m.provider}/${m.model}` });
      byProvider.set(m.provider, group);
      names.set(`${m.provider}/${m.model}`, `${providerName} / ${m.model}`);
    }
    return { groups: [...byProvider.values()], labels: names };
  }, [models]);

  const dirty = useMemo(() => {
    if (!settings || !embedding) return false;
    if (MODEL_KEYS.some((key) => draft[key] !== settings[key])) return true;
    return (
      lane.enabled !== embedding.enabled ||
      Number(lane.dim) !== embedding.dim ||
      lane.normalize !== embedding.normalize
    );
  }, [draft, settings, lane, embedding]);

  const save = useMutation({
    mutationFn: async () => {
      // Two independent writes. Neither rejects the other's state, so a failure
      // here leaves a partial save rather than a broken deployment: the lane's
      // stored flag is intent, and the backend resolves it to off until the
      // model behind it exists.
      await updateDefaultModels({ body: draft, throwOnError: true });
      await updateEmbeddingSettings({
        body: {
          dim: Number(lane.dim) || 0,
          enabled: lane.enabled,
          normalize: lane.normalize,
        },
        throwOnError: true,
      });
    },
    onError: (e) =>
      showToast(e instanceof Error ? e.message : t("defaultModels.saveFailed"), "error"),
    // Two independent writes: the first can land while the second fails, so
    // refetch on either outcome rather than leaving the page diffing against a
    // snapshot the server no longer holds.
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["default-models"] });
      void queryClient.invalidateQueries({ queryKey: ["embedding-settings"] });
    },
    onSuccess: () => showToast(t("defaultModels.saved"), "success"),
  });

  const isStale = (value: string) => value !== "" && !labels.has(value);
  const picker = (
    value: string,
    onChange: (next: string) => void,
    placeholder: string,
    ariaLabel: string,
  ) => (
    <ModelSelect
      ariaLabel={ariaLabel}
      groups={groups}
      labels={labels}
      onChange={onChange}
      placeholder={placeholder}
      value={value}
    />
  );
  const noModels = !modelsFailed && models !== undefined && models.length === 0;
  // The backend stores the switch as intent and resolves the lane to disabled
  // when the model behind it is missing, so say so rather than let the toggle
  // claim the lane is running.
  // Whether the lane is really running is a server fact — it depends on the
  // provider catalog and whether that provider has a key, which the browser only
  // partially sees. Trust `active` while the model field still matches what the
  // server resolved; once it is edited, only "no model at all" is knowable here
  // until the next save.
  const laneInactive =
    lane.enabled &&
    (draft.model_embedding === ""
      ? true
      : draft.model_embedding === settings?.model_embedding && embedding?.active === false);

  return (
    <div className="h-full overflow-y-auto bg-background">
      <div className="mx-auto max-w-3xl p-6 sm:p-8 lg:p-10">
        <SettingsPageHeader
          action={
            <Button
              disabled={!dirty}
              loading={save.isPending}
              onClick={() => save.mutate()}
              size="sm"
            >
              {t("defaultModels.save")}
            </Button>
          }
          description={t("defaultModels.description")}
          title={t("defaultModels.title")}
        />

        <div className="flex flex-col gap-6">
          {noModels && (
            <Alert variant="warning">
              <TriangleAlertIcon />
              <AlertDescription>{t("defaultModels.noModels")}</AlertDescription>
              <AlertAction>
                <Button render={<Link to="/admin/ai/providers" />} size="sm" variant="outline">
                  {t("defaultModels.noModelsAction")}
                </Button>
              </AlertAction>
            </Alert>
          )}

          <Card>
            <CardHeader>
              <CardTitle>{t("defaultModels.agentTitle")}</CardTitle>
              <CardDescription>{t("defaultModels.agentHint")}</CardDescription>
            </CardHeader>
            <CardPanel className="flex flex-col gap-5">
              <TierRow
                description={t("defaultModels.defaultHint")}
                stale={isStale(draft.model)}
                title={t("agents.form.modelDefault")}
              >
                {picker(
                  draft.model,
                  (v) => set({ model: v }),
                  t("defaultModels.unset"),
                  t("agents.form.modelDefault"),
                )}
                <ThinkingSelect
                  ariaLabel={t("agents.form.modelThinking")}
                  onChange={(v) => set({ model_thinking: v })}
                  value={draft.model_thinking}
                />
              </TierRow>

              <Separator />

              <TierRow
                description={t("defaultModels.strongHint")}
                stale={isStale(draft.model_strong)}
                title={t("agents.form.modelStrong")}
              >
                {picker(
                  draft.model_strong,
                  (v) => set({ model_strong: v }),
                  t("agents.form.modelFallback"),
                  t("agents.form.modelStrong"),
                )}
                <ThinkingSelect
                  ariaLabel={t("agents.form.modelStrongThinking")}
                  onChange={(v) => set({ model_strong_thinking: v })}
                  value={draft.model_strong_thinking}
                />
              </TierRow>

              <Separator />

              <TierRow
                description={t("defaultModels.fastHint")}
                stale={isStale(draft.model_fast)}
                title={t("agents.form.modelFast")}
              >
                {picker(
                  draft.model_fast,
                  (v) => set({ model_fast: v }),
                  t("agents.form.modelFallback"),
                  t("agents.form.modelFast"),
                )}
                <ThinkingSelect
                  ariaLabel={t("agents.form.modelFastThinking")}
                  onChange={(v) => set({ model_fast_thinking: v })}
                  value={draft.model_fast_thinking}
                />
              </TierRow>
            </CardPanel>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>{t("defaultModels.capabilitiesTitle")}</CardTitle>
              <CardDescription>{t("defaultModels.auxiliaryHint")}</CardDescription>
            </CardHeader>
            <CardPanel className="flex flex-col gap-5">
              <Field>
                <FieldLabel>
                  {t("defaultModels.vision")}
                  {isStale(draft.model_vision) && (
                    <Badge size="sm" variant="warning">
                      {t("defaultModels.stale")}
                    </Badge>
                  )}
                </FieldLabel>
                <div className="w-full sm:max-w-sm">
                  {picker(
                    draft.model_vision,
                    (v) => set({ model_vision: v }),
                    t("defaultModels.visionUnset"),
                    t("defaultModels.vision"),
                  )}
                </div>
                <FieldDescription>{t("defaultModels.visionHint")}</FieldDescription>
              </Field>

              <Separator />

              <Field>
                <FieldLabel>
                  {t("defaultModels.embedding")}
                  {isStale(draft.model_embedding) && (
                    <Badge size="sm" variant="warning">
                      {t("defaultModels.stale")}
                    </Badge>
                  )}
                </FieldLabel>
                <div className="w-full sm:max-w-sm">
                  {picker(
                    draft.model_embedding,
                    (v) => set({ model_embedding: v }),
                    t("defaultModels.embeddingUnset"),
                    t("defaultModels.embedding"),
                  )}
                </div>
                <FieldDescription>{t("defaultModels.embeddingHint")}</FieldDescription>
              </Field>

              <Field className="gap-2 sm:flex-row sm:items-start sm:justify-between sm:gap-8">
                <div className="flex flex-col gap-1">
                  <FieldLabel className="gap-2">
                    {t("embedding.enableTitle")}
                    {laneInactive && (
                      <Badge size="sm" variant="warning">
                        {t("defaultModels.embeddingInactive")}
                      </Badge>
                    )}
                  </FieldLabel>
                  <FieldDescription>
                    {laneInactive
                      ? t("defaultModels.embeddingNeedsModel")
                      : t("embedding.enableHint")}
                  </FieldDescription>
                </div>
                <Switch
                  aria-label={t("embedding.enableTitle")}
                  checked={lane.enabled}
                  className="sm:mt-1"
                  onCheckedChange={(checked) => setLane((prev) => ({ ...prev, enabled: checked }))}
                />
              </Field>

              <Collapsible onOpenChange={setAdvanced} open={advanced}>
                <CollapsibleTrigger render={<Button size="sm" variant="ghost" />}>
                  {t("defaultModels.advanced")}
                  <ChevronDownIcon
                    className={
                      advanced ? "rotate-180 transition-transform" : "transition-transform"
                    }
                  />
                </CollapsibleTrigger>
                <CollapsiblePanel>
                  <div className="flex flex-col gap-5 px-1 pt-4">
                    <Field>
                      <FieldLabel>{t("embedding.dim")}</FieldLabel>
                      <div className="w-full sm:max-w-40">
                        <Input
                          aria-label={t("embedding.dim")}
                          min={0}
                          nativeInput
                          onChange={(e) =>
                            setLane((prev) => ({
                              ...prev,
                              dim: e.target.value,
                            }))
                          }
                          placeholder="1536"
                          type="number"
                          value={lane.dim}
                        />
                      </div>
                      <FieldDescription>{t("embedding.dimHint")}</FieldDescription>
                    </Field>

                    <Field className="gap-2 sm:flex-row sm:items-start sm:justify-between sm:gap-8">
                      <div className="flex flex-col gap-1">
                        <FieldLabel>{t("embedding.normalizeTitle")}</FieldLabel>
                        <FieldDescription>{t("embedding.normalizeHint")}</FieldDescription>
                      </div>
                      <Switch
                        aria-label={t("embedding.normalizeTitle")}
                        checked={lane.normalize}
                        className="sm:mt-1"
                        onCheckedChange={(checked) =>
                          setLane((prev) => ({ ...prev, normalize: checked }))
                        }
                      />
                    </Field>
                  </div>
                </CollapsiblePanel>
              </Collapsible>
            </CardPanel>
          </Card>
        </div>
      </div>
    </div>
  );
}
