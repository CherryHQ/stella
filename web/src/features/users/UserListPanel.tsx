import { Badge } from "@/components/ui/badge";
import { useI18n } from "@/lib/i18n";
import {
  SettingsListHeader,
  SettingsListItem,
  SettingsListBody,
} from "@/features/settings/SettingsListPanel";
import type { User } from "@/lib/types";

interface UserListPanelProps {
  users: User[];
  selectedId?: string;
  onSelect: (id: string) => void;
}

export function UserListPanel({ users, selectedId, onSelect }: UserListPanelProps) {
  const { t } = useI18n();

  const sorted = [...users].sort(
    (a, b) =>
      (a.name || a.email || "").localeCompare(b.name || b.email || "") || a.id.localeCompare(b.id),
  );

  const grouped = sorted.reduce<Record<string, User[]>>((acc, u) => {
    const role = u.role || "user";
    if (!acc[role]) acc[role] = [];
    acc[role].push(u);
    return acc;
  }, {});

  const groups = Object.entries(grouped)
    .map(([role, items]) => ({
      role,
      label: role === "admin" ? t("users.admins") : t("users.users"),
      users: items,
    }))
    .sort((a, b) => a.label.localeCompare(b.label));

  return (
    <>
      <SettingsListHeader title={t("users.title")} />
      <SettingsListBody>
        {groups.map((group) => (
          <div key={group.role} className="space-y-0.5">
            <div className="flex items-center gap-2 px-3 py-1.5">
              <span className="text-xs font-medium text-muted-foreground">{group.label}</span>
              <Badge variant="secondary" size="sm">
                {group.users.length}
              </Badge>
            </div>
            {group.users.map((u) => (
              <SettingsListItem
                key={u.id}
                active={selectedId === u.id}
                onClick={() => onSelect(u.id)}
              >
                <div className="flex items-center gap-2">
                  <span
                    className={`shrink-0 size-1.5 rounded-full ${u.is_active ? "bg-green-500" : "bg-muted-foreground"}`}
                  />
                  <span className="text-sm truncate">{u.name || u.email || u.id}</span>
                </div>
                <span className="text-xs text-muted-foreground">{u.role}</span>
              </SettingsListItem>
            ))}
          </div>
        ))}
      </SettingsListBody>
    </>
  );
}
