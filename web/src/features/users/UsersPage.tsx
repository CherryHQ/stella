import { useNavigate, useParams, useRouterState } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { authUsersQueryOptions } from "@/lib/queries/users";
import { useI18n } from "@/lib/i18n";
import { Badge } from "@/components/ui/badge";
import { Spinner } from "@/components/ui/spinner";
import { ErrorState } from "@/components/RouteFallback";
import { SettingsDetailSheet, SettingsGridPage } from "@/features/settings/SettingsCardGrid";
import { UserDetailPanel } from "./UserDetailPanel";

export function UsersPage() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const isAdminSurface = useRouterState({
    select: (state) => state.location.pathname.startsWith("/admin/"),
  });
  const listRoute = isAdminSurface ? "/admin/users" : "/settings/users";
  const detailRoute = isAdminSurface ? "/admin/users/$userId" : "/settings/users/$userId";
  // SAFETY: this route may or may not carry a userId param; read as optional.
  const params = useParams({ strict: false }) as { userId?: string };
  const userId = params.userId;

  // Not defaulted to `[]` on purpose. TanStack Query resolves a rejection into
  // `isError` rather than throwing to the router boundary, so a swallowed
  // failure renders an empty table — "this deployment has no users" is a
  // convincing lie to tell an admin during an outage.
  const { data: users, isPending, isError, refetch } = useQuery(authUsersQueryOptions);

  const sorted = [...(users ?? [])].sort(
    (a, b) =>
      (a.name || a.email || "").localeCompare(b.name || b.email || "") || a.id.localeCompare(b.id),
  );

  return (
    <>
      <SettingsGridPage title={t("users.title")}>
        {isPending ? (
          <div className="flex justify-center py-8">
            <Spinner />
          </div>
        ) : isError ? (
          <ErrorState
            title={t("route.error.title")}
            description={t("route.loadFailed")}
            onRetry={() => void refetch()}
          />
        ) : (
          <div className="overflow-x-auto rounded-xl border border-border">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-border bg-muted/40 text-left">
                  <th className="px-4 py-2.5 text-xs font-semibold text-muted-foreground">
                    {t("users.title")}
                  </th>
                  <th className="px-4 py-2.5 text-xs font-semibold text-muted-foreground">
                    {t("users.role")}
                  </th>
                  <th className="px-4 py-2.5 text-xs font-semibold text-muted-foreground">
                    {t("common.status")}
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {sorted.map((u) => (
                  <tr
                    key={u.id}
                    onClick={() => void navigate({ to: detailRoute, params: { userId: u.id } })}
                    className={`cursor-pointer hover:bg-muted/50 ${
                      userId === u.id ? "bg-muted/60" : ""
                    }`}
                  >
                    <td className="px-4 py-2.5">
                      <div className="font-medium text-foreground">{u.name || u.email || u.id}</div>
                      {u.email && u.name && (
                        <div className="text-xs text-muted-foreground">{u.email}</div>
                      )}
                    </td>
                    <td className="px-4 py-2.5">
                      <Badge variant={u.role === "admin" ? "default" : "outline"} size="sm">
                        {u.role}
                      </Badge>
                    </td>
                    <td className="px-4 py-2.5">
                      <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
                        <span
                          className={`size-1.5 rounded-full ${
                            u.is_active ? "bg-success" : "bg-muted-foreground"
                          }`}
                        />
                        {u.is_active ? t("users.active") : t("users.inactive")}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </SettingsGridPage>

      <SettingsDetailSheet open={!!userId} onClose={() => void navigate({ to: listRoute })}>
        {userId ? <UserDetailPanel key={userId} userId={userId} /> : null}
      </SettingsDetailSheet>
    </>
  );
}
