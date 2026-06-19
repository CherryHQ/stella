import { useCallback, useState } from "react";
import { createDeliverable } from "@/lib/api-client";
import type { ComponentsDeliverable } from "@/lib/api-client/types.gen";
import { priorityLabel, policyLabel } from "@/features/deliverables/lib";
import { useI18n } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";

interface Props {
  agentId: string;
  projectId?: string;
  onCreated: (deliverable: ComponentsDeliverable) => void;
}

type Kind = "leaf" | "composite";
type Priority = "routine" | "urgent";
type Policy = "none" | "human";

const SELECT_CLS =
  "h-8.5 w-full rounded-lg border border-input bg-background px-3 text-sm text-foreground outline-none focus:ring-2 focus:ring-ring";

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block space-y-1.5">
      <span className="block text-xs font-medium text-muted-foreground">{label}</span>
      {children}
    </label>
  );
}

export function DeliverableForm({ agentId, projectId, onCreated }: Props) {
  const { t } = useI18n();
  const [title, setTitle] = useState("");
  const [intent, setIntent] = useState("");
  const [kind, setKind] = useState<Kind>("leaf");
  const [priority, setPriority] = useState<Priority>("routine");
  const [reviewPolicy, setReviewPolicy] = useState<Policy>("none");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const valid = !!title.trim();

  const create = useCallback(
    async (activate: boolean) => {
      if (!valid) return;
      setSaving(true);
      setError(null);
      try {
        const { data } = await createDeliverable({
          body: {
            title: title.trim(),
            intent: intent.trim() || undefined,
            agent_id: agentId,
            project_id: projectId,
            kind,
            priority,
            review_policy: reviewPolicy,
            activate: kind === "leaf" ? activate : undefined,
          },
          throwOnError: true,
        });
        if (data) onCreated(data);
      } catch (e) {
        setError(e instanceof Error ? e.message : t("common.error"));
      } finally {
        setSaving(false);
      }
    },
    [valid, title, intent, agentId, projectId, kind, priority, reviewPolicy, onCreated, t],
  );

  return (
    <div className="space-y-4">
      <Field label={t("deliverables.newTitle")}>
        <Input
          nativeInput
          value={title}
          onChange={(e) => setTitle((e.target as HTMLInputElement).value)}
          placeholder={t("deliverables.newTitlePlaceholder")}
          className="text-sm"
          autoFocus
        />
      </Field>

      <Field label={t("deliverables.newIntent")}>
        <Textarea
          value={intent}
          onChange={(e) => setIntent((e.target as HTMLTextAreaElement).value)}
          rows={5}
          placeholder={t("deliverables.newIntentPlaceholder")}
          className="text-sm"
        />
      </Field>

      <div>
        <span className="mb-1.5 block text-xs font-medium text-muted-foreground">
          {t("deliverables.newKind")}
        </span>
        <ToggleGroup
          variant="outline"
          value={[kind]}
          onValueChange={(v: string[]) => v[0] && setKind(v[0] as Kind)}
        >
          <ToggleGroupItem value="leaf">{t("deliverables.newKindLeaf")}</ToggleGroupItem>
          <ToggleGroupItem value="composite">{t("deliverables.newKindComposite")}</ToggleGroupItem>
        </ToggleGroup>
        <p className="mt-1.5 text-xs text-muted-foreground">
          {kind === "leaf"
            ? t("deliverables.newKindLeafDesc")
            : t("deliverables.newKindCompositeDesc")}
        </p>
      </div>

      <Field label={t("deliverables.fieldPriority")}>
        <select
          value={priority}
          onChange={(e) => setPriority(e.target.value as Priority)}
          className={SELECT_CLS}
        >
          <option value="routine">{priorityLabel(t, "routine")}</option>
          <option value="urgent">{priorityLabel(t, "urgent")}</option>
        </select>
      </Field>

      <Field label={t("deliverables.fieldReviewPolicy")}>
        <select
          value={reviewPolicy}
          onChange={(e) => setReviewPolicy(e.target.value as Policy)}
          className={SELECT_CLS}
        >
          <option value="none">{policyLabel(t, "none")}</option>
          <option value="human">{policyLabel(t, "human")}</option>
        </select>
      </Field>

      {error && <p className="text-xs text-destructive">{error}</p>}

      <div className="flex items-center gap-2 pt-1">
        <Button size="sm" disabled={saving || !valid} onClick={() => void create(false)}>
          {saving ? t("deliverables.creating") : t("deliverables.create")}
        </Button>
        {kind === "leaf" && (
          <Button
            size="sm"
            variant="outline"
            disabled={saving || !valid}
            onClick={() => void create(true)}
          >
            {t("deliverables.createRun")}
          </Button>
        )}
      </div>

      {kind === "composite" && (
        <p className="text-xs text-muted-foreground">{t("deliverables.newCompositeHint")}</p>
      )}
    </div>
  );
}
