import type { User } from "@/lib/types";
import type { AgentsPageState } from "../AgentsPage";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/lib/i18n";

interface Props {
  state: AgentsPageState;
  availableUsers: User[];
  onSetState: (patch: Partial<AgentsPageState>) => void;
  onAddUser: () => void;
  onRemoveUser: (userId: string) => void;
}

export function UsersTab({ state, availableUsers, onSetState, onAddUser, onRemoveUser }: Props) {
  const { t } = useI18n();
  const { assignedUsers, addUserId } = state;

  return (
    <div className="space-y-6">
      <p className="text-[11px] text-muted-foreground">
        Manage user access for restricted-scope agents.
      </p>
      <div className="divide-y divide-border/60 border-t border-b border-border/40">
        {assignedUsers.map((u) => (
          <div
            key={u.id}
            className="flex items-center justify-between py-2.5 px-1 hover:bg-muted/10 transition-colors duration-120"
          >
            <span className="text-xs font-mono text-foreground/80">{u.name || u.email}</span>
            <Button
              onClick={() => onRemoveUser(u.id)}
              variant="ghost"
              size="xs"
              className="text-destructive hover:bg-destructive/10 cursor-pointer"
            >
              {t("common.remove")}
            </Button>
          </div>
        ))}
        {assignedUsers.length === 0 && (
          <div className="text-xs text-muted-foreground py-4 text-center">
            {t("agents.users.noUsers")}
          </div>
        )}
      </div>
      <div className="flex gap-2.5 items-center">
        <select
          value={addUserId}
          onChange={(e) => onSetState({ addUserId: e.target.value })}
          className="flex-1 rounded-lg border border-border bg-background px-3 py-2 text-sm outline-none focus:border-primary focus:ring-2 focus:ring-primary/20 transition-all duration-120 cursor-pointer text-foreground/80 font-medium"
        >
          <option value="">Select user...</option>
          {availableUsers.map((u) => (
            <option key={u.id} value={u.id}>
              {u.name || u.email}
            </option>
          ))}
        </select>
        <Button onClick={onAddUser} disabled={!addUserId} size="sm" className="cursor-pointer">
          {t("common.add")}
        </Button>
      </div>
    </div>
  );
}
