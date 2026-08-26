import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { listAuthProviders, loginLocal } from "@/lib/api-client/sdk.gen";
import { useI18n } from "@/lib/i18n";
import type { OidcProviderList } from "@/lib/api-client/types.gen";
import { Mail, Lock, Eye, EyeOff, Loader2 } from "lucide-react";
import { AuthLayout } from "@/features/auth/AuthLayout";
import { authErrorMessage } from "@/lib/auth-error";
import feishuIcon from "@/assets/auth/feishu.svg";

const AUTH_PROVIDER_LABELS: Record<string, string> = {
  feishu: "飞书",
  github: "GitHub",
  google: "Google",
};

function authProviderLabel(name: string): string {
  const key = name.toLowerCase();
  return (
    AUTH_PROVIDER_LABELS[key] ??
    name.replace(/[-_]+/g, " ").replace(/\b\w/g, (c) => c.toUpperCase())
  );
}

function authProviderIcon(name: string, label: string) {
  const key = name.toLowerCase();
  if (key === "github") return <span className="font-semibold text-[13px]">GH</span>;
  if (key === "google") return <span className="font-semibold text-[13px]">G</span>;
  if (key === "feishu") return <img src={feishuIcon} alt="" className="size-5" />;
  return <span className="font-semibold text-xs">{label.slice(0, 2).toUpperCase()}</span>;
}

function authProviderIconClass(name: string): string {
  const key = name.toLowerCase();
  if (key === "github") return "bg-foreground text-background";
  if (key === "google") return "bg-background text-primary border border-border";
  if (key === "feishu") return "bg-background border border-border";
  return "bg-primary/10 text-primary border border-primary/20";
}

export function LoginPage() {
  const { t } = useI18n();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
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
  const otherProviders = providers.filter((p) => p.name !== "local");
  const hasRegisterEnabled = providers.some((p) => p.name === "local" && p.register_url);

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setLoading(true);
    setError("");

    try {
      const { data } = await loginLocal({
        body: { email, password },
        throwOnError: true,
      });
      const redirectUrlObj = new URL(data.redirect_url, window.location.origin);
      window.location.href = redirectUrlObj.pathname + redirectUrlObj.search;
    } catch (err) {
      setError(authErrorMessage(err, "Authentication failed"));
    } finally {
      setLoading(false);
    }
  };

  return (
    <AuthLayout subtitle={t("login.subtitle")} error={error || undefined}>
      {providersLoading ? (
        <div className="flex justify-center py-8">
          <Loader2 className="size-6 animate-spin text-muted-foreground" />
        </div>
      ) : providersFailed ? (
        <p className="text-center text-sm text-muted-foreground">
          {authErrorMessage(providersError, t("login.providersUnavailable"))}
        </p>
      ) : hasLocalProvider ? (
        <form onSubmit={onSubmit} className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="email">{t("login.email")}</Label>
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
            <div className="flex justify-between items-center">
              <Label htmlFor="password">{t("login.password")}</Label>
            </div>
            <div className="relative">
              <Lock className="absolute left-3 top-1/2 -translate-y-1/2 size-4 text-muted-foreground" />
              <Input
                id="password"
                type={showPassword ? "text" : "password"}
                required
                placeholder="••••••••"
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

          <Button
            type="submit"
            disabled={loading}
            className="w-full py-6 mt-2 relative overflow-hidden group"
          >
            {loading ? (
              <span className="flex items-center justify-center gap-2">
                <Loader2 className="size-4 animate-spin" />
                {t("login.signingIn")}
              </span>
            ) : (
              t("login.signIn")
            )}
          </Button>

          {hasRegisterEnabled && (
            <p className="text-center text-sm text-muted-foreground mt-4">
              {t("login.noAccount")}{" "}
              <Link to="/signup" className="text-primary hover:underline font-medium ml-1">
                {t("login.signUpLink")}
              </Link>
            </p>
          )}
        </form>
      ) : (
        providers.length === 0 && (
          <p className="text-center text-sm text-muted-foreground">{t("login.noProviders")}</p>
        )
      )}

      {otherProviders.length > 0 && (
        <div className="mt-6 space-y-4">
          {hasLocalProvider && (
            <div className="relative">
              <div className="absolute inset-0 flex items-center">
                <span className="w-full border-t border-border/40" />
              </div>
              <div className="relative flex justify-center text-xs uppercase">
                <span className="bg-card/45 px-2 text-muted-foreground">
                  {t("login.orContinueWith")}
                </span>
              </div>
            </div>
          )}

          <div className="space-y-3">
            {otherProviders.map((p) => {
              const label = authProviderLabel(p.name);
              return (
                <a key={p.name} href={p.login_url} className="block group">
                  <Button
                    type="button"
                    variant="outline"
                    className="w-full py-5 text-sm font-medium border-border/60 hover:border-primary/40 hover:bg-primary/5 group-hover:scale-[1.01] transition-all duration-200"
                  >
                    <span
                      className={`mr-2 inline-flex size-6 shrink-0 items-center justify-center rounded-full ${authProviderIconClass(p.name)}`}
                    >
                      {authProviderIcon(p.name, label)}
                    </span>
                    {t("login.signInWith", { provider: label })}
                  </Button>
                </a>
              );
            })}
          </div>
        </div>
      )}
    </AuthLayout>
  );
}
