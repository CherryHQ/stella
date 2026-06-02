import type { AgentDetail } from "@/lib/types";
import type { AgentsPageState, ModelOption } from "./AgentsPage";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { SettingsEmptyState } from "@/features/settings/SettingsEmptyState";

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
  const { agents, isAdmin, currentUserId, cachedModels } = state;

  const canEditAgent = (a: AgentDetail) =>
    isAdmin || (a.creator_id && a.creator_id === currentUserId);

  if (agents.length === 0) {
    return (
      <SettingsEmptyState
        message="No agents configured"
        description="Create your first agent profile to get started."
        action={
          <Button onClick={onCreateAgent} variant="outline" size="sm" className="rounded-xl">
            New Agent
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
          <div
            key={a.id}
            onClick={() => canEdit && onEdit(a)}
            className={`group relative flex flex-col justify-between rounded-2xl border border-border/40 bg-card p-5 transition-all hover:border-border/80 hover:shadow-sm ${
              canEdit ? "cursor-pointer" : "cursor-default opacity-85"
            }`}
          >
            <div className="space-y-3">
              <div className="flex items-center justify-between gap-3">
                <div className="flex items-center gap-2 min-w-0">
                  <span
                    className={`shrink-0 w-1.5 h-1.5 rounded-full ${
                      a.enabled ? "bg-green-500" : "bg-muted-foreground/40"
                    }`}
                  />
                  <h3 className="text-sm font-semibold text-foreground/90 truncate">{a.name}</h3>
                </div>
                <div className="flex items-center gap-1.5 shrink-0">
                  {a.scope === RestrictedScope(a) && (
                    <Badge
                      variant="warning"
                      className="text-[9px] tracking-wide uppercase font-semibold"
                    >
                      restricted
                    </Badge>
                  )}
                  <Badge
                    variant={a.enabled ? "success" : "outline"}
                    className="text-[9px] tracking-wide uppercase font-semibold"
                  >
                    {a.enabled ? "active" : "disabled"}
                  </Badge>
                </div>
              </div>
              <p className="font-mono text-[10px] text-muted-foreground truncate max-w-full">
                Model: {a.model ? modelLabel(a.model, cachedModels) : "—"}
              </p>
            </div>
            <div className="mt-4 flex items-center justify-between">
              <span className="text-xs text-muted-foreground font-mono">ID: {a.id}</span>
              <div className="flex items-center gap-2">
                {canEdit && (
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
                {canEdit && (
                  <span className="text-xs font-medium text-primary opacity-0 group-hover:opacity-100 transition-opacity">
                    Configure →
                  </span>
                )}
              </div>
            </div>
          </div>
        );
      })}
    </div>
  );
}

function RestrictedScope(a: AgentDetail) {
  return a.scope === "restricted" ? "restricted" : "";
}
