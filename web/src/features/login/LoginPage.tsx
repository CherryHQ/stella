import { useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { queryClient } from "@/lib/queryClient";
import {
  login as loginRequest,
  register as registerRequest,
  listAuthProviders,
} from "@/lib/api-client/sdk.gen";
import { meQueryOptions } from "@/lib/queries/me";
import { useI18n } from "@/lib/i18n";

export function LoginPage() {
  const navigate = useNavigate();
  const { t } = useI18n();
  const [isRegister, setIsRegister] = useState(false);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  const { data: providersData } = useQuery({
    queryKey: ["auth-providers"],
    queryFn: () => listAuthProviders({ throwOnError: true }),
    staleTime: 60_000,
  });
  const providers = providersData?.data?.providers ?? [];
  const hasOIDC = providers.length > 0;

  function toggleMode() {
    setIsRegister(!isRegister);
    setError("");
    setPassword("");
    setConfirmPassword("");
  }

  async function login(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    setLoading(true);
    try {
      await loginRequest({ body: { username, password }, throwOnError: true });
      await queryClient.invalidateQueries(meQueryOptions);
      void navigate({ to: "/sessions" as any });
    } catch (e) {
      setError(apiErrorMessage(e, t("login.loginFailed")));
    } finally {
      setLoading(false);
    }
  }

  async function register(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    if (password !== confirmPassword) {
      setError(t("login.passwordsMismatch"));
      return;
    }
    if (password.length < 8) {
      setError(t("login.passwordTooShort"));
      return;
    }
    setLoading(true);
    try {
      await registerRequest({ body: { username, password }, throwOnError: true });
      await queryClient.invalidateQueries(meQueryOptions);
      void navigate({ to: "/sessions" as any });
    } catch (e) {
      setError(apiErrorMessage(e, t("login.registrationFailed")));
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-background">
      <div className="w-full max-w-sm rounded-xl border border-border bg-card shadow-sm p-8">
        <div className="text-center mb-6">
          <span className="font-serif italic text-primary text-3xl tracking-tight select-none">
            stella
          </span>
          <p className="text-muted-foreground text-sm mt-1">{t("login.adminPanel")}</p>
        </div>

        {error && (
          <div className="mb-4 rounded-lg border border-destructive/36 bg-destructive/8 px-3 py-2 text-sm text-destructive-foreground">
            {error}
          </div>
        )}

        {hasOIDC && (
          <div className="space-y-3 mb-4">
            {providers.map((p) => (
              <a key={p.name} href={p.login_url} className="block">
                <Button type="button" variant="outline" className="w-full">
                  {t("login.signIn")} {p.name}
                </Button>
              </a>
            ))}
          </div>
        )}

        {!hasOIDC && !isRegister && (
          <form onSubmit={login} className="space-y-4">
            <div className="space-y-1.5">
              <label className="text-sm font-medium font-mono">{t("login.username")}</label>
              <Input
                type="text"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                placeholder={t("login.usernamePlaceholder")}
                required
                autoComplete="username"
                nativeInput
              />
            </div>
            <div className="space-y-1.5">
              <label className="text-sm font-medium font-mono">{t("login.password")}</label>
              <Input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder={t("login.passwordPlaceholder")}
                required
                autoComplete="current-password"
                nativeInput
              />
            </div>
            <Button type="submit" loading={loading} className="w-full">
              {loading ? t("login.signingIn") : t("login.signIn")}
            </Button>
          </form>
        )}

        {!hasOIDC && isRegister && (
          <form onSubmit={register} className="space-y-4">
            <div className="space-y-1.5">
              <label className="text-sm font-medium font-mono">{t("login.username")}</label>
              <Input
                type="text"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                placeholder={t("login.usernamePlaceholder")}
                required
                autoComplete="username"
                nativeInput
              />
            </div>
            <div className="space-y-1.5">
              <label className="text-sm font-medium font-mono">{t("login.password")}</label>
              <Input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder={t("login.passwordMinLength")}
                required
                minLength={8}
                autoComplete="new-password"
                nativeInput
              />
            </div>
            <div className="space-y-1.5">
              <label className="text-sm font-medium font-mono">{t("login.confirmPassword")}</label>
              <Input
                type="password"
                value={confirmPassword}
                onChange={(e) => setConfirmPassword(e.target.value)}
                placeholder={t("login.confirmPasswordPlaceholder")}
                required
                minLength={8}
                autoComplete="new-password"
                nativeInput
              />
            </div>
            <Button type="submit" loading={loading} className="w-full">
              {loading ? t("login.creatingAccount") : t("login.createAccount")}
            </Button>
          </form>
        )}

        {!hasOIDC && (
          <div className="text-center mt-4">
            <Button type="button" variant="ghost" size="sm" onClick={toggleMode}>
              {isRegister ? t("login.alreadyHaveAccount") : t("login.needAccount")}
            </Button>
          </div>
        )}
      </div>
    </div>
  );
}

function apiErrorMessage(error: unknown, fallback: string) {
  if (error && typeof error === "object" && "error" in error) {
    const message = (error as { error?: unknown }).error;
    if (typeof message === "string" && message) return message;
  }
  if (error instanceof Error && error.message) return error.message;
  return fallback;
}
