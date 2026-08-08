import type { User } from "@/lib/types";
import type { AgentsPageState } from "../agent-detail-state";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/lib/i18n";

interface Props {
  state: AgentsPageState;
  availableUsers: User[];
  onSetState: (patch: Partial<AgentsPageState>) => void;
  onAddUser: () => void;
  onRemoveUser: (userId: string) => void;
}

/**
 * Access for one agent. Everyone who may edit the agent picks its reach — only
 * themselves, or every user of the deployment — because that choice never names
 * another account. The per-user assignment list stays admin-only: an ordinary
 * user must not be able to learn who else exists here.
 */
export function UsersTab({ state, availableUsers, onSetState, onAddUser, onRemoveUser }: Props) {
  const { t } = useI18n();
  const { assignedUsers, addUserId, form, isAdmin } = state;

  const scope = form.scope === "system" ? "system" : "restricted";
  const setScope = (next: "system" | "restricted") =>
    onSetState({ form: { ...form, scope: next } });

  return (
    <div className="space-y-6">
      <div className="space-y-2">
        <p className="text-xs font-semibold text-muted-foreground">
          {t("agents.users.visibility")}
        </p>
        <div className="grid gap-2 sm:grid-cols-2">
          {(["restricted", "system"] as const).map((value) => (
            <button
              key={value}
              type="button"
              onClick={() => setScope(value)}
              className={`rounded-lg border px-3 py-2.5 text-left transition-colors cursor-pointer ${
                scope === value ? "border-primary bg-primary/5" : "border-border hover:bg-muted/20"
              }`}
            >
              <span className="block text-sm font-medium text-foreground">
                {value === "system"
                  ? t("agents.users.scopeSystem")
                  : t("agents.users.scopeRestricted")}
              </span>
              <span className="mt-0.5 block text-xs text-muted-foreground">
                {value === "system"
                  ? t("agents.users.scopeSystemDesc")
                  : t("agents.users.scopeRestrictedDesc")}
              </span>
            </button>
          ))}
        </div>
      </div>

      {isAdmin && scope === "restricted" && (
        <div className="space-y-3">
          <p className="text-xs text-muted-foreground">{t("agents.users.assignedDesc")}</p>
          <div className="divide-y divide-border/60 border-t border-b border-border/40">
            {assignedUsers.map((u) => (
              <div
                key={u.id}
                className="flex items-center justify-between py-2.5 px-1 hover:bg-muted/10"
              >
                <span className="text-xs font-mono text-foreground">{u.name || u.email}</span>
                <Button
                  onClick={() => onRemoveUser(u.id)}
                  variant="ghost"
                  size="xs"
                  className="text-destructive-foreground hover:bg-destructive/10 cursor-pointer"
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
              className="flex-1 rounded-lg border border-border bg-background px-3 py-2 text-sm outline-none focus:border-primary focus:ring-2 focus:ring-primary/20 cursor-pointer text-foreground font-medium"
            >
              <option value="">{t("agents.users.selectUser")}</option>
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
      )}
      {isAdmin && scope === "system" && (
        <p className="text-xs text-muted-foreground">{t("agents.users.systemHint")}</p>
      )}
    </div>
  );
}
