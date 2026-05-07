import type { AgentsPageState } from "../AgentsPage";
import { Textarea } from "@/components/ui/textarea";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";

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
        <Spinner className="size-5" />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div>
        <label className="block text-sm font-mono mb-1">Soul override</label>
        <p className="text-xs text-muted-foreground mb-1">
          Your personal soul for this agent. Replaces the agent default for your sessions only.
        </p>
        <Textarea
          value={personalisation.soulDraft}
          onChange={(e) => setPers({ soulDraft: (e.target as HTMLTextAreaElement).value })}
          rows={3}
          placeholder="Customise this agent's personality for yourself..."
          className="text-sm font-mono"
        />
        <Button
          onClick={onSaveSoul}
          disabled={personalisation.soulDraft === personalisation.soul}
          variant="ghost"
          size="xs"
          className="text-primary mt-1"
        >
          Save
        </Button>
      </div>
      <div>
        <label className="block text-sm font-mono mb-1">User profile</label>
        <p className="text-xs text-muted-foreground mb-1">
          What this agent knows about you across conversations.
        </p>
        <Textarea
          value={personalisation.profileDraft}
          onChange={(e) => setPers({ profileDraft: (e.target as HTMLTextAreaElement).value })}
          rows={3}
          placeholder="Add context about yourself for this agent..."
          className="text-sm font-mono"
        />
        <Button
          onClick={onSaveProfile}
          disabled={personalisation.profileDraft === personalisation.profile}
          variant="ghost"
          size="xs"
          className="text-primary mt-1"
        >
          Save
        </Button>
      </div>
    </div>
  );
}
