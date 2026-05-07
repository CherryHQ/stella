import { useCallback, useState } from "react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

type Toast = { message: string; type: "success" | "error" } | null;

function ToastAlert({ toast }: { toast: Toast }) {
  if (!toast) return null;
  return (
    <div
      className={`alert ${toast.type === "error" ? "alert-error" : "alert-success"} fixed bottom-4 right-4 z-50 w-auto max-w-sm shadow-lg`}
    >
      <span>{toast.message}</span>
    </div>
  );
}

export function AccountPage() {
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
    <div className="py-6">
      <div className="mb-6">
        <h1 className="font-serif text-3xl tracking-tight">Account</h1>
        <p className="text-secondary text-sm mt-1">Manage your account settings.</p>
      </div>

      <div className="mb-10">
        <h2 className="font-serif text-xl mb-4">Change Password</h2>
        <div className="card bg-base-200">
          <div className="card-body">
            <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
              <div>
                <label className="label mb-1">
                  <span className="label-text text-sm font-medium">Current Password</span>
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
                <label className="label mb-1">
                  <span className="label-text text-sm font-medium">New Password</span>
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
                <label className="label mb-1">
                  <span className="label-text text-sm font-medium">Confirm New Password</span>
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
                Change Password
              </Button>
            </div>
          </div>
        </div>
      </div>

      <ToastAlert toast={toast} />
    </div>
  );
}
