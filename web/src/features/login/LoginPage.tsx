import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { listAuthProviders } from "@/lib/api-client/sdk.gen";
import { useI18n } from "@/lib/i18n";
import type { OidcProviderList } from "@/lib/api-client/types.gen";
import { Mail, Lock, Eye, EyeOff, Loader2 } from "lucide-react";
import { AuthLayout } from "@/features/auth/AuthLayout";

export function LoginPage() {
  const { t } = useI18n();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  const { data: providersData } = useQuery({
    queryKey: ["auth-providers"],
    queryFn: () => listAuthProviders({ throwOnError: true }),
    staleTime: 60_000,
  });
  const providers = (providersData?.data as OidcProviderList)?.providers ?? [];

  const hasLocalProvider = providers.some((p) => p.name === "local");
  const otherProviders = providers.filter((p) => p.name !== "local");
  const hasRegisterEnabled = providers.some((p) => p.name === "local" && p.register_url);

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setLoading(true);
    setError("");

    try {
      const initRes = await fetch("/auth/login/local");
      if (!initRes.ok) {
        throw new Error("Failed to initialize authentication flow");
      }

      const authorizeUrl = initRes.url;

      const body = new URLSearchParams();
      body.append("email", email);
      body.append("password", password);

      const authRes = await fetch(authorizeUrl, {
        method: "POST",
        headers: {
          "Content-Type": "application/x-www-form-urlencoded",
          Accept: "application/json",
        },
        body: body.toString(),
      });

      const data = await authRes.json();
      if (!authRes.ok || !data.success) {
        setError(data.error || "Authentication failed");
        return;
      }

      const redirectUrlObj = new URL(data.redirect_url, window.location.origin);
      window.location.href = redirectUrlObj.pathname + redirectUrlObj.search;
    } catch (err) {
      setError(err instanceof Error ? err.message : "An unexpected error occurred");
    } finally {
      setLoading(false);
    }
  };

  return (
    <AuthLayout subtitle={t("login.subtitle")} error={error || undefined}>
      {hasLocalProvider ? (
        <form onSubmit={onSubmit} className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="email">Email</Label>
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
              <Label htmlFor="password">Password</Label>
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
              <button
                type="button"
                onClick={() => setShowPassword(!showPassword)}
                className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground transition-colors"
              >
                {showPassword ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
              </button>
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
            {otherProviders.map((p) => (
              <a key={p.name} href={p.login_url} className="block group">
                <Button
                  type="button"
                  variant="outline"
                  className="w-full py-5 text-sm font-medium border-border/60 hover:border-primary/40 hover:bg-primary/5 group-hover:scale-[1.01] transition-all duration-200"
                >
                  {t("login.signIn")} {p.name}
                </Button>
              </a>
            ))}
          </div>
        </div>
      )}
    </AuthLayout>
  );
}
