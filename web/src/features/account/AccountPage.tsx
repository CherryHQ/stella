import { useCallback, useState } from "react";
import { api } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
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
      await api("PUT", "/api/auth/profile/password", {
        current_password: currentPassword,
        new_password: newPassword,
      });
      showToast("Password changed successfully");
      setCurrentPassword("");
      setNewPassword("");
      setConfirmPassword("");
    } catch (e) {
      showToast(e instanceof Error ? e.message : "An error occurred", "error");
    } finally {
      setChangingPassword(false);
    }
  }, [currentPassword, newPassword, confirmPassword, showToast]);

  return (
    <div>
      <SettingsPageHeader title={t("account.title")} description="Manage your account settings." />

      <div className="mb-10">
        <h2 className="text-xl font-semibold mb-4">{t("account.changePassword")}</h2>
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
              <label className="mb-1.5 block text-sm font-medium">{t("account.newPassword")}</label>
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

      <ToastAlert toast={toast} />
    </div>
  );
}
