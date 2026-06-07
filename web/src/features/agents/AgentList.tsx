import type { AgentDetail } from "@/lib/types";
import type { AgentsPageState, ModelOption } from "./AgentsPage";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/lib/i18n";
import {
  SettingsListHeader,
  SettingsListItem,
  SettingsListBody,
} from "@/features/settings/SettingsListPanel";
import { Plus } from "lucide-react";

interface Props {
  state: AgentsPageState;
  selectedId?: string;
  onEdit: (a: AgentDetail) => void;
  onCreateAgent: () => void;
}

function modelLabel(value: string, cachedModels: ModelOption[]): string {
  return cachedModels.find((m) => m.value === value)?.label ?? value;
}

export function AgentList({ state, selectedId, onEdit, onCreateAgent }: Props) {
  const { t } = useI18n();
  const { agents, isAdmin, currentUserId, cachedModels } = state;

  const canEditAgent = (a: AgentDetail) =>
    isAdmin || (a.creator_id && a.creator_id === currentUserId);

  return (
    <>
      <SettingsListHeader
        title={t("settings.nav.agents")}
        action={
          <Button onClick={onCreateAgent} variant="ghost" size="icon-sm">
            <Plus className="size-4" />
          </Button>
        }
      />
      <SettingsListBody>
        {agents.map((a) => {
          const canEdit = canEditAgent(a);
          return (
            <SettingsListItem
              key={a.id}
              active={selectedId === a.id}
              onClick={() => canEdit && onEdit(a)}
              className={canEdit ? undefined : "opacity-60 cursor-default"}
            >
              <div className="flex items-center gap-2">
                <span
                  className={`shrink-0 size-1.5 rounded-full ${a.enabled ? "bg-green-500" : "bg-muted-foreground"}`}
                />
                <span className="text-sm truncate">{a.name}</span>
              </div>
              <span className="text-xs text-muted-foreground truncate">
                {a.model ? modelLabel(a.model, cachedModels) : "—"}
              </span>
            </SettingsListItem>
          );
        })}
      </SettingsListBody>
    </>
  );
}
