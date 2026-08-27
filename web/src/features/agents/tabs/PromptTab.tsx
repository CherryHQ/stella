import type { AgentsPageState } from "../agent-detail-state";
import { targetValue } from "@/lib/utils";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import { useI18n } from "@/lib/i18n";
import { canEditAgent } from "../agent-detail-state";

interface Props {
  state: AgentsPageState;
  onSetState: (patch: Partial<AgentsPageState>) => void;
  onApplySoul: (soulID: string) => void;
}

export function PromptTab({ state, onSetState, onApplySoul }: Props) {
  const { t } = useI18n();
  const { form, builtinSouls, selectedSoulID, editingId } = state;

  const setForm = (patch: Partial<typeof form>) => onSetState({ form: { ...form, ...patch } });

  const canEdit = !editingId || canEditAgent(form);

  return (
    <div className="space-y-6">
      {builtinSouls.length > 0 && (
        <div>
          <label className="block text-xs font-semibold text-muted-foreground mb-2">
            {t("agents.form.soulPreset")}
          </label>
          <div className="flex flex-wrap gap-2">
            {builtinSouls.map((soul) => (
              <Badge
                key={soul.id}
                render={<button type="button" title={soul.description} />}
                variant={selectedSoulID === soul.id ? "default" : "outline"}
                className="cursor-pointer"
                onClick={() => {
                  onSetState({ selectedSoulID: soul.id });
                  onApplySoul(soul.id);
                }}
              >
                {soul.name}
              </Badge>
            ))}
          </div>
        </div>
      )}
      <div>
        <label className="block text-xs font-semibold text-muted-foreground mb-1">
          {t("agents.form.soul")}
        </label>
        <p className="text-xs text-muted-foreground mb-2">{t("agents.form.soulDesc")}</p>
        <Textarea
          value={form.soul}
          onChange={(e) => setForm({ soul: targetValue(e) })}
          rows={3}
          placeholder={t("agents.form.soulPlaceholder")}
          className="text-sm font-mono"
        />
      </div>
      <div>
        <label className="block text-xs font-semibold text-muted-foreground mb-2">
          {t("agents.form.systemPrompt")}
        </label>
        <Textarea
          value={form.system_prompt}
          onChange={(e) => setForm({ system_prompt: targetValue(e) })}
          rows={10}
          disabled={!canEdit}
          className="text-sm font-mono"
        />
      </div>
    </div>
  );
}
