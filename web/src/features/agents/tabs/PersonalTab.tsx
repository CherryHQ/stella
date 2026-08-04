import type { AgentsPageState } from "../agent-detail-state";
import { Textarea } from "@/components/ui/textarea";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { useI18n } from "@/lib/i18n";

interface Props {
  state: AgentsPageState;
  onSetState: (patch: Partial<AgentsPageState>) => void;
  onSaveSoul: () => void;
  onSaveProfile: () => void;
}

export function PersonalTab({ state, onSetState, onSaveSoul, onSaveProfile }: Props) {
  const { t } = useI18n();
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
        <label className="block text-sm font-medium mb-1">{t("sessions.soul.title")}</label>
        <p className="text-xs text-muted-foreground mb-1">{t("sessions.soul.subtitle")}</p>
        <Textarea
          value={personalisation.soulDraft}
          onChange={(e) => setPers({ soulDraft: (e.target as HTMLTextAreaElement).value })}
          rows={3}
          placeholder={t("sessions.soul.placeholder")}
          className="font-mono"
        />
        <Button
          onClick={onSaveSoul}
          disabled={personalisation.soulDraft === personalisation.soul}
          variant="link"
          size="xs"
          className="mt-1"
        >
          {t("common.save")}
        </Button>
      </div>
      <div>
        <label className="block text-sm font-medium mb-1">{t("sessions.memory.title")}</label>
        <p className="text-xs text-muted-foreground mb-1">{t("sessions.memory.context")}</p>
        <Textarea
          value={personalisation.profileDraft}
          onChange={(e) => setPers({ profileDraft: (e.target as HTMLTextAreaElement).value })}
          rows={3}
          placeholder={t("sessions.memory.placeholder")}
          className="font-mono"
        />
        <Button
          onClick={onSaveProfile}
          disabled={personalisation.profileDraft === personalisation.profile}
          variant="link"
          size="xs"
          className="mt-1"
        >
          {t("common.save")}
        </Button>
      </div>
    </div>
  );
}
