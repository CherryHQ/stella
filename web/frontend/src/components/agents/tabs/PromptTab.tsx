import type { AgentsPageState } from "../AgentsPage";

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
          <label className="label"><span className="label-text font-mono text-sm">Soul preset</span></label>
          <div className="flex flex-wrap gap-2">
            {builtinSouls.map((soul) => (
              <button
                key={soul.id}
                onClick={() => {
                  onSetState({ selectedSoulID: soul.id });
                  onApplySoul(soul.id);
                }}
                type="button"
                className={`badge badge-sm cursor-pointer transition-colors ${
                  selectedSoulID === soul.id ? "badge-primary" : "badge-ghost"
                }`}
                title={soul.description}
              >
                {soul.name}
              </button>
            ))}
          </div>
        </div>
      )}
      <div>
        <label className="label"><span className="label-text font-mono text-sm">Soul</span></label>
        <p className="text-xs text-base-content/60 mb-1">
          Default personality for all users. Each user can override their own.
        </p>
        <textarea
          value={form.soul}
          onChange={(e) => setForm({ soul: e.target.value })}
          rows={3}
          placeholder="Personality and behavior tone..."
          className="textarea textarea-bordered w-full text-sm font-mono resize-y"
        />
      </div>
      <div>
        <label className="label"><span className="label-text font-mono text-sm">System Prompt</span></label>
        <textarea
          value={form.system_prompt}
          onChange={(e) => setForm({ system_prompt: e.target.value })}
          rows={10}
          disabled={!canEdit}
          className="textarea textarea-bordered w-full text-sm font-mono resize-y"
        />
      </div>
    </div>
  );
}
