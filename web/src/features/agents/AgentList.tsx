import type { AgentDetail } from "@/lib/types";
import type { AgentsPageState, ModelOption } from "./AgentsPage";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { SettingsEmptyState } from "@/features/settings/SettingsEmptyState";
import { Trash2 } from "lucide-react";
import { useI18n } from "@/lib/i18n";

interface Props {
  state: AgentsPageState;
  onEdit: (a: AgentDetail) => void;
  onConfirmDelete: (msg: string, action: () => void) => void;
  onDeleteAgent: (id: string) => void;
  onCreateAgent: () => void;
}

function modelLabel(value: string, cachedModels: ModelOption[]): string {
  return cachedModels.find((m) => m.value === value)?.label ?? value;
}

export function AgentList({ state, onEdit, onConfirmDelete, onDeleteAgent, onCreateAgent }: Props) {
  const { t } = useI18n();
  const { agents, isAdmin, currentUserId, cachedModels } = state;

  const canEditAgent = (a: AgentDetail) =>
    isAdmin || (a.creator_id && a.creator_id === currentUserId);

  if (agents.length === 0) {
    return (
      <SettingsEmptyState
        message={t("agents.noAgentsConfigured")}
        description={t("agents.createFirst")}
        action={
          <Button onClick={onCreateAgent} variant="outline" size="sm">
            {t("agents.new")}
          </Button>
        }
      />
    );
  }

  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
      {agents.map((a) => {
        const canEdit = canEditAgent(a);
        return (
          <Card
            key={a.id}
            onClick={() => canEdit && onEdit(a)}
            className={`group relative p-5 hover:border-foreground/20 ${
              canEdit ? "cursor-pointer" : "cursor-default opacity-80"
            }`}
          >
            <div className="space-y-3">
              <div className="flex items-center justify-between gap-3">
                <div className="flex items-center gap-2 min-w-0">
                  <span
                    className={`shrink-0 w-1.5 h-1.5 rounded-full ${
                      a.enabled ? "bg-success" : "bg-muted-foreground"
                    }`}
                  />
                  <h3 className="text-sm font-medium text-foreground truncate">{a.name}</h3>
                </div>
                <div className="flex items-center gap-1.5 shrink-0">
                  {a.scope === "restricted" && (
                    <Badge variant="warning">{t("agents.restricted")}</Badge>
                  )}
                  <Badge variant={a.enabled ? "success" : "outline"}>
                    {a.enabled ? t("agents.active") : t("agents.disabled")}
                  </Badge>
                </div>
              </div>
              <p className="font-mono text-[10px] text-muted-foreground truncate max-w-full flex items-center gap-1">
                <span className="text-[9px] text-muted-foreground">Model:</span>
                <span className="text-foreground font-medium">
                  {a.model ? modelLabel(a.model, cachedModels) : "—"}
                </span>
              </p>
            </div>
            <div className="mt-5 flex items-center justify-between pt-2 border-t border-border/10">
              <span className="text-[10px] text-muted-foreground font-mono flex items-center gap-1">
                <span>ID:</span>
                <span className="text-muted-foreground">{a.id}</span>
              </span>
              <div className="flex items-center gap-2">
                {canEdit && (
                  <Button
                    onClick={(e) => {
                      e.stopPropagation();
                      onConfirmDelete(`Delete ${a.name}?`, () => onDeleteAgent(a.id));
                    }}
                    variant="ghost"
                    size="icon-xs"
                    className="text-destructive hover:bg-destructive/10 shrink-0 opacity-0 group-hover:opacity-100 transition-opacity duration-120 cursor-pointer"
                    title="Delete agent"
                  >
                    <Trash2 className="size-3.5" />
                  </Button>
                )}
                {canEdit && (
                  <span className="text-xs font-medium text-primary opacity-0 group-hover:opacity-100 transition-all duration-120 transform translate-x-1 group-hover:translate-x-0">
                    Configure →
                  </span>
                )}
              </div>
            </div>
          </Card>
        );
      })}
    </div>
  );
}
