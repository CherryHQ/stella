import type { AgentsPageState } from "../AgentsPage";

interface Props {
  state: AgentsPageState;
  onSetState: (patch: Partial<AgentsPageState>) => void;
  onSaveSoul: () => void;
  onSaveProfile: () => void;
}

export function PersonalTab({ state, onSetState, onSaveSoul, onSaveProfile }: Props) {
  const { personalisation } = state;

  const setPers = (patch: Partial<typeof personalisation>) =>
    onSetState({ personalisation: { ...personalisation, ...patch } });

  if (!personalisation.loaded) {
    return (
      <div className="flex justify-center py-4">
        <span className="loading loading-spinner loading-sm"></span>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div>
        <label className="label"><span className="label-text font-mono text-sm">Soul override</span></label>
        <p className="text-xs text-base-content/60 mb-1">
          Your personal soul for this agent. Replaces the agent default for your sessions only.
        </p>
        <textarea
          value={personalisation.soulDraft}
          onChange={(e) => setPers({ soulDraft: e.target.value })}
          rows={3}
          placeholder="Customise this agent's personality for yourself..."
          className="textarea textarea-bordered w-full text-sm font-mono resize-y"
        />
        <button
          onClick={onSaveSoul}
          disabled={personalisation.soulDraft === personalisation.soul}
          className="btn btn-ghost btn-xs text-primary disabled:opacity-30 mt-1"
        >
          Save
        </button>
      </div>
      <div>
        <label className="label"><span className="label-text font-mono text-sm">User profile</span></label>
        <p className="text-xs text-base-content/60 mb-1">
          What this agent knows about you across conversations.
        </p>
        <textarea
          value={personalisation.profileDraft}
          onChange={(e) => setPers({ profileDraft: e.target.value })}
          rows={3}
          placeholder="Add context about yourself for this agent..."
          className="textarea textarea-bordered w-full text-sm font-mono resize-y"
        />
        <button
          onClick={onSaveProfile}
          disabled={personalisation.profileDraft === personalisation.profile}
          className="btn btn-ghost btn-xs text-primary disabled:opacity-30 mt-1"
        >
          Save
        </button>
      </div>
    </div>
  );
}
