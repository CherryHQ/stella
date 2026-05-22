import type { AgentDetail } from "@/lib/types";
import type { AgentsPageState } from "./AgentsPage";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { SettingsListBody, SettingsListItem } from "@/features/settings/SettingsListPanel";

interface Props {
  state: AgentsPageState;
  onEdit: (a: AgentDetail) => void;
  onConfirmDelete: (msg: string, action: () => void) => void;
  onDeleteAgent: (id: string) => void;
}

export function AgentList({ state, onEdit, onConfirmDelete, onDeleteAgent }: Props) {
  const { agents, editingId, isAdmin, currentUserId } = state;

  const canEditAgent = (a: AgentDetail) =>
    isAdmin || (a.creator_id && a.creator_id === currentUserId);

  return (
    <div>
      {agents.length === 0 && (
        <div className="px-3 py-8 text-center">
          <p className="text-sm text-muted-foreground">No agents configured yet.</p>
        </div>
      )}
      <SettingsListBody>
        {agents.map((a) => (
          <SettingsListItem
            key={a.id}
            onClick={() => canEditAgent(a) && onEdit(a)}
            active={editingId === a.id}
            className={canEditAgent(a) ? "cursor-pointer" : "cursor-default opacity-60"}
          >
            <div className="flex items-start justify-between gap-1">
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-1 flex-wrap">
                  <span className="font-medium text-sm truncate">{a.name}</span>
                  <Badge variant={a.enabled ? "success" : "outline"} size="sm">
                    {a.enabled ? "on" : "off"}
                  </Badge>
                  {a.scope === "restricted" && (
                    <Badge variant="warning" size="sm">
                      restricted
                    </Badge>
                  )}
                </div>
                <div className="text-xs font-mono text-muted-foreground mt-0.5 truncate">
                  {a.model || "—"}
                </div>
              </div>
              {canEditAgent(a) && (
                <Button
                  onClick={(e) => {
                    e.stopPropagation();
                    onConfirmDelete(`Delete ${a.name}?`, () => onDeleteAgent(a.id));
                  }}
                  variant="ghost"
                  size="icon-xs"
                  className="text-destructive shrink-0 opacity-0 group-hover:opacity-100 transition-opacity"
                  title="Delete agent"
                >
                  ✕
                </Button>
              )}
            </div>
          </SettingsListItem>
        ))}
      </SettingsListBody>
    </div>
  );
}
