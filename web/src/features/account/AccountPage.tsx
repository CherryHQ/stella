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
import { TokensSection } from "@/features/account/TokensSection";
import { ThemeAppearanceControl, ThemeAccentControl } from "@/components/ThemeControls";

type Toast = { message: string; type: "success" | "error" } | null;

function ToastAlert({ toast }: { toast: Toast }) {
  if (!toast) return null;
  return (
    <div
      className={`fixed bottom-4 right-4 z-50 w-auto max-w-sm rounded-xl border px-4 py-3 text-sm ${
        toast.type === "error"
          ? "border-destructive/20 bg-destructive/10 text-destructive-foreground"
          : "border-success/20 bg-success/10 text-success-foreground"
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
  const sessions = sessionsData?.sessions;

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
    <div className="h-full overflow-y-auto bg-background">
      <div className="mx-auto max-w-3xl p-6 sm:p-8 lg:p-10 space-y-8">
        <SettingsPageHeader title={t("account.title")} description={t("account.description")} />

        {/* Profile section */}
        <section>
          <h2 className="text-base font-semibold text-foreground mb-3">{t("account.profile")}</h2>
          <div className="rounded-xl border border-border bg-card p-6">
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
              <div className="flex flex-col gap-1.5">
                {me?.name && <p className="text-base font-semibold text-foreground">{me.name}</p>}
                {me?.username && (
                  <p className="text-sm text-muted-foreground font-mono">{me.username}</p>
                )}
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
        </section>

        {/* Appearance — the full theme surface. The user menu only carries the
            light/dark/system row; accent lives here where it has room. */}
        <section>
          <h2 className="text-base font-semibold text-foreground mb-3">
            {t("account.appearance")}
          </h2>
          <div className="rounded-xl border border-border bg-card p-6 max-w-sm space-y-5">
            <ThemeAppearanceControl />
            <ThemeAccentControl />
          </div>
        </section>

        {/* Password change — only shown for users who have local credentials */}
        {me?.has_credentials && (
          <section>
            <h2 className="text-base font-semibold text-foreground mb-3">
              {t("account.changePassword")}
            </h2>
            <div className="rounded-xl border border-border bg-card p-6 space-y-4">
              <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
                <div className="space-y-1.5">
                  <label className="block text-xs font-medium text-muted-foreground">
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
                <div className="space-y-1.5">
                  <label className="block text-xs font-medium text-muted-foreground">
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
                <div className="space-y-1.5">
                  <label className="block text-xs font-medium text-muted-foreground">
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
              <div className="pt-2 flex justify-end">
                <Button size="sm" loading={changingPassword} onClick={changePassword}>
                  {t("account.changePassword")}
                </Button>
              </div>
            </div>
          </section>
        )}

        {/* Personal access tokens section */}
        <TokensSection notify={showToast} />

        {/* Sessions section */}
        <section>
          <h2 className="text-base font-semibold text-foreground mb-3">{t("account.sessions")}</h2>
          <div className="rounded-xl border border-border bg-card p-6">
            {!sessions?.length ? (
              <p className="text-sm text-muted-foreground py-4 text-center">
                {t("account.noSessions")}
              </p>
            ) : (
              <div className="space-y-3">
                {sessions.map((sess) => (
                  <div
                    key={sess.id}
                    className="flex items-center justify-between rounded-lg border border-border bg-background px-4 py-3"
                  >
                    <div className="flex flex-col gap-1 min-w-0">
                      <span className="text-sm font-mono text-foreground font-medium truncate">
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
                      className="text-destructive hover:bg-destructive/10 shrink-0 cursor-pointer"
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
        </section>
      </div>

      <ToastAlert toast={toast} />
    </div>
  );
}
