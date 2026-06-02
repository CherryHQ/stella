import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { listAuthProviders } from "@/lib/api-client/sdk.gen";
import { useI18n } from "@/lib/i18n";
import type { OidcProviderList } from "@/lib/api-client/types.gen";
import { Mail, Lock, User, Eye, EyeOff, Loader2 } from "lucide-react";
import { AuthLayout } from "@/features/auth/AuthLayout";

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

  const { data: providersData } = useQuery({
    queryKey: ["auth-providers"],
    queryFn: () => listAuthProviders({ throwOnError: true }),
    staleTime: 60_000,
  });
  const providers = (providersData?.data as OidcProviderList)?.providers ?? [];
  const hasLocalProvider = providers.some((p) => p.name === "local");
  const hasRegisterEnabled = providers.some((p) => p.name === "local" && p.register_url);

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault();

    if (password !== confirmPassword) {
      setError("Passwords do not match");
      return;
    }
    if (password.length < 8) {
      setError("Password must be at least 8 characters long");
      return;
    }

    setLoading(true);
    setError("");

    try {
      const initRes = await fetch("/auth/login/local?mode=register");
      if (!initRes.ok) {
        throw new Error("Failed to initialize registration flow");
      }

      const authorizeUrl = initRes.url;

      const body = new URLSearchParams();
      body.append("email", email);
      body.append("password", password);
      body.append("confirm_password", confirmPassword);
      body.append("name", name);

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
        setError(data.error || "Registration failed");
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
    <AuthLayout subtitle={t("login.signupSubtitle")} error={error || undefined}>
      {!hasLocalProvider ? (
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
            <Label htmlFor="name">Name</Label>
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
            <Label htmlFor="password">Password</Label>
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
              <button
                type="button"
                onClick={() => setShowPassword(!showPassword)}
                className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground transition-colors"
              >
                {showPassword ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
              </button>
            </div>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="confirmPassword">Confirm Password</Label>
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
              <button
                type="button"
                onClick={() => setShowConfirmPassword(!showConfirmPassword)}
                className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground transition-colors"
              >
                {showConfirmPassword ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
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
