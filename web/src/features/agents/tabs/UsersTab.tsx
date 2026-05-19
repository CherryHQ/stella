import type { User } from "@/lib/types";
import type { AgentsPageState } from "../AgentsPage";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/lib/i18n";

interface Props {
  state: AgentsPageState;
  availableUsers: User[];
  onSetState: (patch: Partial<AgentsPageState>) => void;
  onAddUser: () => void;
  onRemoveUser: (userId: number) => void;
}

export function UsersTab({ state, availableUsers, onSetState, onAddUser, onRemoveUser }: Props) {
  const { t } = useI18n();
  const { assignedUsers, addUserId } = state;

  return (
    <div className="space-y-4">
      <p className="text-xs text-muted-foreground">
        Manage user access for restricted-scope agents.
      </p>
      <div className="divide-y divide-border">
        {assignedUsers.map((u) => (
          <div key={u.id} className="flex items-center justify-between py-2">
            <span className="text-sm font-mono">{u.username}</span>
            <Button
              onClick={() => onRemoveUser(u.id)}
              variant="ghost"
              size="xs"
              className="text-destructive"
            >
              {t("common.remove")}
            </Button>
          </div>
        ))}
        {assignedUsers.length === 0 && (
          <div className="text-xs text-muted-foreground py-2">{t("agents.users.noUsers")}</div>
        )}
      </div>
      <div className="flex gap-2">
        <select
          value={addUserId}
          onChange={(e) => onSetState({ addUserId: e.target.value })}
          className="flex-1 rounded-xl border border-input bg-background px-3 py-1.5 text-sm outline-none focus:ring-2 focus:ring-ring"
        >
          <option value="">Select user...</option>
          {availableUsers.map((u) => (
            <option key={u.id} value={u.id}>
              {u.username}
            </option>
          ))}
        </select>
        <Button onClick={onAddUser} disabled={!addUserId} size="sm">
          {t("common.add")}
        </Button>
      </div>
    </div>
  );
}
