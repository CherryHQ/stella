import type { AgentsPageState } from "../AgentsPage";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";

interface Props {
  state: AgentsPageState;
  onSetState: (patch: Partial<AgentsPageState>) => void;
  onApplySoul: (soulID: string) => void;
}

export function PromptTab({ state, onSetState, onApplySoul }: Props) {
  const { form, builtinSouls, selectedSoulID, editingId, isAdmin } = state;

  const setForm = (patch: Partial<typeof form>) =>
    onSetState({ form: { ...form, ...patch } });

  const canEdit = !editingId || isAdmin || form.creator_id === state.currentUserId;

  return (
    <div className="space-y-4">
      {builtinSouls.length > 0 && (
        <div>
          <label className="block text-sm font-mono mb-1">Soul preset</label>
          <div className="flex flex-wrap gap-2">
            {builtinSouls.map((soul) => (
              <Badge
                key={soul.id}
                render={<button type="button" title={soul.description} />}
                variant={selectedSoulID === soul.id ? "default" : "outline"}
                size="sm"
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
        <label className="block text-sm font-mono mb-1">Soul</label>
        <p className="text-xs text-muted-foreground mb-1">
          Default personality for all users. Each user can override their own.
        </p>
        <Textarea
          value={form.soul}
          onChange={(e) => setForm({ soul: (e.target as HTMLTextAreaElement).value })}
          rows={3}
          placeholder="Personality and behavior tone..."
          className="text-sm font-mono"
        />
      </div>
      <div>
        <label className="block text-sm font-mono mb-1">System Prompt</label>
        <Textarea
          value={form.system_prompt}
          onChange={(e) => setForm({ system_prompt: (e.target as HTMLTextAreaElement).value })}
          rows={10}
          disabled={!canEdit}
          className="text-sm font-mono"
        />
      </div>
    </div>
  );
}
