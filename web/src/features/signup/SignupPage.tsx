import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { listAuthProviders, registerLocal } from "@/lib/api-client/sdk.gen";
import { useI18n } from "@/lib/i18n";
import type { OidcProviderList } from "@/lib/api-client/types.gen";
import { Mail, Lock, User, Eye, EyeOff, Loader2 } from "lucide-react";
import { AuthLayout } from "@/features/auth/AuthLayout";
import { authErrorMessage } from "@/lib/auth-error";

export function SignupPage() {
  const { t } = useI18n();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [name, setName] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [showConfirmPassword, setShowConfirmPassword] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  const {
    data: providersData,
    error: providersError,
    isError: providersFailed,
    isPending: providersLoading,
  } = useQuery({
    queryKey: ["auth-providers"],
    queryFn: () => listAuthProviders({ throwOnError: true }),
    staleTime: 60_000,
  });
  // SAFETY: listAuthProviders returns the OIDC provider list under data.
  const providers = (providersData?.data as OidcProviderList)?.providers ?? [];
  const hasLocalProvider = providers.some((p) => p.name === "local");
  const hasRegisterEnabled = providers.some((p) => p.name === "local" && p.register_url);

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault();

    if (password !== confirmPassword) {
      setError(t("login.passwordMismatch"));
      return;
    }
    if (password.length < 8) {
      setError(t("login.passwordTooShort"));
      return;
    }

    setLoading(true);
    setError("");

    try {
      const { data } = await registerLocal({
        body: { email, password, confirm_password: confirmPassword, name },
        throwOnError: true,
      });
      const redirectUrlObj = new URL(data.redirect_url, window.location.origin);
      window.location.href = redirectUrlObj.pathname + redirectUrlObj.search;
    } catch (err) {
      setError(authErrorMessage(err, "Registration failed"));
    } finally {
      setLoading(false);
    }
  };

  return (
    <AuthLayout subtitle={t("login.signupSubtitle")} error={error || undefined}>
      {providersLoading ? (
        <div className="flex justify-center py-8">
          <Loader2 className="size-6 animate-spin text-muted-foreground" />
        </div>
      ) : providersFailed ? (
        <p className="text-center text-sm text-muted-foreground">
          {authErrorMessage(providersError, t("login.providersUnavailable"))}
        </p>
      ) : !hasLocalProvider ? (
        <p className="text-center text-sm text-muted-foreground">
          {t("login.noLocalRegistration")}
        </p>
      ) : !hasRegisterEnabled ? (
        <p className="text-center text-sm text-muted-foreground">
          {t("login.registrationDisabled")}
        </p>
      ) : (
        <form onSubmit={onSubmit} className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="name">{t("signup.name")}</Label>
            <div className="relative">
              <User className="absolute left-3 top-1/2 -translate-y-1/2 size-4 text-muted-foreground" />
              <Input
                id="name"
                type="text"
                required
                placeholder="John Doe"
                className="pl-9 bg-background/50"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="email">{t("signup.email")}</Label>
            <div className="relative">
              <Mail className="absolute left-3 top-1/2 -translate-y-1/2 size-4 text-muted-foreground" />
              <Input
                id="email"
                type="email"
                required
                placeholder="name@example.com"
                className="pl-9 bg-background/50"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
              />
            </div>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="password">{t("signup.password")}</Label>
            <div className="relative">
              <Lock className="absolute left-3 top-1/2 -translate-y-1/2 size-4 text-muted-foreground" />
              <Input
                id="password"
                type={showPassword ? "text" : "password"}
                required
                placeholder="•••••••• (min 8 chars)"
                className="pl-9 pr-10 bg-background/50"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
              <Button
                type="button"
                variant="ghost"
                size="icon-xs"
                onClick={() => setShowPassword(!showPassword)}
                className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
              >
                {showPassword ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
              </Button>
            </div>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="confirmPassword">{t("signup.confirmPassword")}</Label>
            <div className="relative">
              <Lock className="absolute left-3 top-1/2 -translate-y-1/2 size-4 text-muted-foreground" />
              <Input
                id="confirmPassword"
                type={showConfirmPassword ? "text" : "password"}
                required
                placeholder="••••••••"
                className="pl-9 pr-10 bg-background/50"
                value={confirmPassword}
                onChange={(e) => setConfirmPassword(e.target.value)}
              />
              <Button
                type="button"
                variant="ghost"
                size="icon-xs"
                onClick={() => setShowConfirmPassword(!showConfirmPassword)}
                className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
              >
                {showConfirmPassword ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
              </Button>
            </div>
          </div>

          <Button
            type="submit"
            disabled={loading}
            className="w-full py-6 mt-2 relative overflow-hidden group"
          >
            {loading ? (
              <span className="flex items-center justify-center gap-2">
                <Loader2 className="size-4 animate-spin" />
                {t("login.signingUp")}
              </span>
            ) : (
              t("login.signUp")
            )}
          </Button>

          <p className="text-center text-sm text-muted-foreground mt-4">
            {t("login.hasAccount")}{" "}
            <Link to="/login" className="text-primary hover:underline font-medium ml-1">
              {t("login.signInLink")}
            </Link>
          </p>
        </form>
      )}
    </AuthLayout>
  );
}
