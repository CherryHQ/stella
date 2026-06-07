import { useNavigate, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { authUsersQueryOptions } from "@/lib/queries/users";
import { useI18n } from "@/lib/i18n";
import { useToast, ToastContainer } from "@/hooks/use-toast";
import { SettingsDetailLayout } from "@/features/settings/SettingsDetailLayout";
import { SettingsEmptyState } from "@/features/settings/SettingsEmptyState";
import { UserListPanel } from "./UserListPanel";
import { UserDetailPanel } from "./UserDetailPanel";

export function UsersPage() {
  const { t } = useI18n();
  const { toasts } = useToast();
  const navigate = useNavigate();
  const params = useParams({ strict: false }) as { userId?: string };
  const userId = params.userId;

  const { data: users = [] } = useQuery(authUsersQueryOptions);

  return (
    <>
      <SettingsDetailLayout
        list={
          <UserListPanel
            users={users}
            selectedId={userId}
            onSelect={(id) =>
              void navigate({ to: "/settings/users/$userId", params: { userId: id } })
            }
          />
        }
        detail={userId ? <UserDetailPanel key={userId} userId={userId} /> : undefined}
        emptyState={
          <SettingsEmptyState message={t("users.noUsers")} description={t("users.noUsersDesc")} />
        }
        onBack={() => void navigate({ to: "/settings/users" })}
      />
      <ToastContainer messages={toasts} />
    </>
  );
}
