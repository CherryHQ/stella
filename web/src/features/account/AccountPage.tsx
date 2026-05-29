import { useCallback, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  changePassword as changePasswordRequest,
  listAuthSessions,
  deleteAuthSession,
} from "@/lib/api-client/sdk.gen";
import { meQueryOptions } from "@/lib/queries/me";
import { useI18n } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { SettingsPageHeader } from "@/features/settings/SettingsPageHeader";

type Toast = { message: string; type: "success" | "error" } | null;

function ToastAlert({ toast }: { toast: Toast }) {
  if (!toast) return null;
  return (
    <div
      className={`fixed bottom-4 right-4 z-50 w-auto max-w-sm rounded-lg border px-4 py-3 text-sm shadow-md ${
        toast.type === "error"
          ? "border-destructive/36 bg-destructive/8 text-destructive-foreground"
          : "border-success/36 bg-success/8 text-success-foreground"
      }`}
    >
      {toast.message}
    </div>
  );
}

export function AccountPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { data: me } = useQuery(meQueryOptions);
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [changingPassword, setChangingPassword] = useState(false);
  const [toast, setToast] = useState<Toast>(null);

  const showToast = useCallback((message: string, type: "success" | "error" = "success") => {
    setToast({ message, type });
    setTimeout(() => setToast(null), 3000);
  }, []);

  const changePassword = useCallback(async () => {
    if (!currentPassword || !newPassword) {
      showToast("Please fill in all password fields", "error");
      return;
    }
    if (newPassword.length < 8) {
      showToast("New password must be at least 8 characters", "error");
      return;
    }
    if (newPassword !== confirmPassword) {
      showToast("New passwords do not match", "error");
      return;
    }

    setChangingPassword(true);
    try {
      await changePasswordRequest({
        body: { current_password: currentPassword, new_password: newPassword },
        throwOnError: true,
      });
      showToast(t("account.passwordUpdated"));
      setCurrentPassword("");
      setNewPassword("");
      setConfirmPassword("");
    } catch (e) {
      showToast(e instanceof Error ? e.message : t("account.passwordUpdateFailed"), "error");
    } finally {
      setChangingPassword(false);
    }
  }, [currentPassword, newPassword, confirmPassword, showToast, t]);

  // Sessions query
  const { data: sessionsData } = useQuery({
    queryKey: ["authSessions"],
    queryFn: async () => {
      const { data } = await listAuthSessions({ throwOnError: true });
      return data;
    },
  });
  const sessions = (
    sessionsData as { items?: Array<{ id: string; created_at: string; expires_at: string }> }
  )?.items;

  const revokeSession = useMutation({
    mutationFn: async (id: string) => {
      await deleteAuthSession({ path: { id }, throwOnError: true });
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["authSessions"] });
      showToast(t("account.sessionRevoked"));
    },
    onError: (e) => showToast(e instanceof Error ? e.message : "Failed to revoke session", "error"),
  });

  return (
    <div className="p-6 sm:p-8 lg:p-10">
      <SettingsPageHeader title={t("account.title")} description={t("account.description")} />

      {/* Profile section */}
      <div className="mb-10">
        <h2 className="font-serif text-xl mb-4">{t("account.profile")}</h2>
        <div className="border-t border-border pt-6">
          <div className="flex items-start gap-5">
            {me?.avatar_url ? (
              <img
                src={me.avatar_url}
                alt=""
                className="h-16 w-16 rounded-full border border-border object-cover"
              />
            ) : (
              <div className="flex h-16 w-16 items-center justify-center rounded-full border border-border bg-muted text-xl font-medium text-muted-foreground">
                {(me?.name || me?.username || "?").charAt(0).toUpperCase()}
              </div>
            )}
            <div className="flex flex-col gap-2">
              {me?.name && <p className="text-lg font-medium">{me.name}</p>}
              {me?.username && <p className="text-sm text-muted-foreground">{me.username}</p>}
              {me?.email && me.email !== me.username && (
                <p className="text-sm text-muted-foreground">{me.email}</p>
              )}
              <div className="flex items-center gap-2 mt-1">
                <Badge variant={me?.is_admin ? "default" : "outline"} size="sm">
                  {me?.role ?? "user"}
                </Badge>
              </div>
            </div>
          </div>
        </div>
      </div>

      {/* Password change — only shown for users who have local credentials */}
      {me?.has_credentials && (
        <div className="mb-10">
          <h2 className="font-serif text-xl mb-4">{t("account.changePassword")}</h2>
          <div className="border-t border-border pt-6">
            <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
              <div>
                <label className="mb-1.5 block text-sm font-medium">
                  {t("account.currentPassword")}
                </label>
                <Input
                  type="password"
                  value={currentPassword}
                  onChange={(e) => setCurrentPassword(e.target.value)}
                  placeholder="current password"
                  autoComplete="current-password"
                  nativeInput
                />
              </div>
              <div>
                <label className="mb-1.5 block text-sm font-medium">
                  {t("account.newPassword")}
                </label>
                <Input
                  type="password"
                  value={newPassword}
                  onChange={(e) => setNewPassword(e.target.value)}
                  placeholder="min 8 characters"
                  minLength={8}
                  autoComplete="new-password"
                  nativeInput
                />
              </div>
              <div>
                <label className="mb-1.5 block text-sm font-medium">
                  {t("account.confirmNewPassword")}
                </label>
                <Input
                  type="password"
                  value={confirmPassword}
                  onChange={(e) => setConfirmPassword(e.target.value)}
                  placeholder="confirm new password"
                  autoComplete="new-password"
                  nativeInput
                />
              </div>
            </div>
            <div className="mt-4">
              <Button size="sm" loading={changingPassword} onClick={changePassword}>
                {t("account.changePassword")}
              </Button>
            </div>
          </div>
        </div>
      )}

      {/* Sessions section */}
      <div className="mb-10">
        <h2 className="font-serif text-xl mb-4">{t("account.sessions")}</h2>
        <div className="border-t border-border pt-6">
          {!sessions?.length ? (
            <p className="text-sm text-muted-foreground">{t("account.noSessions")}</p>
          ) : (
            <div className="space-y-3">
              {sessions.map((sess) => (
                <div
                  key={sess.id}
                  className="flex items-center justify-between rounded-md border border-border px-4 py-3"
                >
                  <div className="flex flex-col gap-0.5">
                    <span className="text-sm font-mono text-muted-foreground">
                      {sess.id.slice(0, 12)}...
                    </span>
                    <span className="text-xs text-muted-foreground">
                      Created: {new Date(sess.created_at).toLocaleString()}
                    </span>
                    <span className="text-xs text-muted-foreground">
                      Expires: {new Date(sess.expires_at).toLocaleString()}
                    </span>
                  </div>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="text-destructive"
                    loading={revokeSession.isPending}
                    onClick={() => revokeSession.mutate(sess.id)}
                  >
                    {t("account.revokeSession")}
                  </Button>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>

      <ToastAlert toast={toast} />
    </div>
  );
}
