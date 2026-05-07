import type { User } from "@/lib/types";
import type { AgentsPageState } from "../AgentsPage";

interface Props {
  state: AgentsPageState;
  availableUsers: User[];
  onSetState: (patch: Partial<AgentsPageState>) => void;
  onAddUser: () => void;
  onRemoveUser: (userId: number) => void;
}

export function UsersTab({ state, availableUsers, onSetState, onAddUser, onRemoveUser }: Props) {
  const { assignedUsers, addUserId } = state;

  return (
    <div className="space-y-4">
      <p className="text-xs text-base-content/60">Manage user access for restricted-scope agents.</p>
      <div className="divide-y divide-base-300">
        {assignedUsers.map((u) => (
          <div key={u.id} className="flex items-center justify-between py-2">
            <span className="text-sm font-mono">{u.username}</span>
            <button onClick={() => onRemoveUser(u.id)} className="btn btn-ghost btn-xs text-error">
              remove
            </button>
          </div>
        ))}
        {assignedUsers.length === 0 && (
          <div className="text-xs text-base-content/50 py-2">No users assigned.</div>
        )}
      </div>
      <div className="flex gap-2">
        <select
          value={addUserId}
          onChange={(e) => onSetState({ addUserId: e.target.value })}
          className="select select-bordered select-sm flex-1"
        >
          <option value="">Select user...</option>
          {availableUsers.map((u) => (
            <option key={u.id} value={u.id}>{u.username}</option>
          ))}
        </select>
        <button onClick={onAddUser} disabled={!addUserId} className="btn btn-primary btn-sm">
          Add
        </button>
      </div>
    </div>
  );
}
