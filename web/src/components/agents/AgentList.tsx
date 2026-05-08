import type { AgentDetail } from "@/lib/types";
import type { AgentsPageState } from "./AgentsPage";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";

interface Props {
  state: AgentsPageState;
  onEdit: (a: AgentDetail) => void;
  onStartCreate: () => void;
  onConfirmDelete: (msg: string, action: () => void) => void;
  onDeleteAgent: (id: string) => void;
}

export function AgentList({ state, onEdit, onStartCreate, onConfirmDelete, onDeleteAgent }: Props) {
  const { agents, editingId, showForm, isAdmin, currentUserId } = state;

  const canEditAgent = (a: AgentDetail) =>
    isAdmin || (a.creator_id !== 0 && a.creator_id === currentUserId);

  return (
    <aside
      className={`lg:border-r border-border px-3 py-4 lg:min-h-screen ${showForm ? "hidden lg:block" : "block"}`}
    >
      <div className="flex items-center justify-between mb-3 px-1">
        <span className="text-xs font-mono text-muted-foreground">{agents.length} agents</span>
        <Button onClick={onStartCreate} variant="ghost" size="xs" className="text-primary font-medium">
          + New
        </Button>
      </div>
      {agents.length === 0 && (
        <div className="py-8 text-center">
          <p className="text-sm text-muted-foreground">No agents configured yet.</p>
        </div>
      )}
      <div className="space-y-0.5">
        {agents.map((a) => (
          <div
            key={a.id}
            onClick={() => onEdit(a)}
            className={`group rounded-lg px-3 py-2.5 cursor-pointer transition-colors border ${
              editingId === a.id
                ? "border-primary/50 bg-primary/5"
                : "border-transparent hover:bg-muted"
            }`}
          >
            <div className="flex items-start justify-between gap-2">
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-1.5 flex-wrap">
                  <span className="font-medium text-sm truncate">{a.name}</span>
                  <Badge variant={a.enabled ? "success" : "outline"} size="sm">
                    {a.enabled ? "on" : "off"}
                  </Badge>
                  {a.scope === "restricted" && (
                    <Badge variant="warning" size="sm">restricted</Badge>
                  )}
                </div>
                <div className="text-xs font-mono text-muted-foreground mt-0.5 truncate">{a.model || "—"}</div>
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
          </div>
        ))}
      </div>
    </aside>
  );
}
