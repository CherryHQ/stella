import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { listAuthProviders } from "@/lib/api-client/sdk.gen";
import { Mail, Lock, User, Eye, EyeOff, Loader2, AlertCircle } from "lucide-react";

export function SignupPage() {
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
  const providers = (providersData?.data as any)?.providers ?? [];
  const hasLocalProvider = providers.some((p: any) => p.name === "local");
  const hasRegisterEnabled = providers.some((p: any) => p.name === "local" && p.register_url);

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
      // 1. Initiate OIDC request to set PKCE/state cookies for registration
      const initialUrl = "/auth/login/local?mode=register";
      const initRes = await fetch(initialUrl);
      if (!initRes.ok) {
        throw new Error("Failed to initialize registration flow");
      }

      // The response URL is the direct same-origin /oidc/local/authorize?... endpoint
      const authorizeUrl = initRes.url;

      // 2. Submit credentials in the background
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

      // 3. Success! The redirect_url points to the callback.
      // Parse relative path to avoid SSL/port issues.
      const redirectUrlObj = new URL(data.redirect_url, window.location.origin);
      window.location.href = redirectUrlObj.pathname + redirectUrlObj.search;
    } catch (err) {
      setError(err instanceof Error ? err.message : "An unexpected error occurred");
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="min-h-screen w-full flex items-center justify-center bg-background relative overflow-hidden">
      {/* Background Gradients */}
      <div className="absolute top-[-20%] left-[-20%] w-[60%] h-[60%] rounded-full bg-primary/10 blur-[150px] pointer-events-none" />
      <div className="absolute bottom-[-20%] right-[-20%] w-[60%] h-[60%] rounded-full bg-violet-600/10 blur-[150px] pointer-events-none" />

      {/* Decorative Grid Overlay */}
      <div className="absolute inset-0 bg-[linear-gradient(to_right,#80808008_1px,transparent_1px),linear-gradient(to_bottom,#80808008_1px,transparent_1px)] bg-[size:24px_24px] pointer-events-none [mask-image:radial-gradient(ellipse_60%_50%_at_50%_50%,#000_80%,transparent_100%)]" />

      {/* Main glass card container */}
      <div className="w-full max-w-[420px] mx-4 rounded-2xl border border-border/40 bg-card/45 backdrop-blur-xl shadow-2xl p-8 relative z-10 transition-all duration-300 hover:shadow-primary/5 hover:border-border/60">
        <div className="text-center mb-6">
          <div className="inline-flex items-center justify-center p-3 rounded-2xl bg-primary/5 border border-primary/10 mb-4 shadow-inner">
            <img
              src="/stella-monogram.svg"
              alt="Stella"
              width={40}
              height={40}
              className="rounded-lg shadow-sm animate-pulse"
            />
          </div>
          <h1 className="font-serif italic text-primary text-4xl tracking-tight select-none">
            stella
          </h1>
          <p className="text-muted-foreground text-sm mt-2">Create an account to get started</p>
        </div>

        {error && (
          <div className="mb-6 p-4 rounded-lg bg-destructive/10 border border-destructive/20 text-destructive text-sm flex items-start gap-3 animate-shake">
            <AlertCircle className="size-4 mt-0.5 shrink-0" />
            <span>{error}</span>
          </div>
        )}

        {!hasLocalProvider ? (
          <p className="text-center text-sm text-muted-foreground">
            Local registration is not available. Please contact your administrator.
          </p>
        ) : !hasRegisterEnabled ? (
          <p className="text-center text-sm text-muted-foreground">
            Registration is currently disabled on this instance.
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
                  Signing up...
                </span>
              ) : (
                "Sign up"
              )}
            </Button>

            <p className="text-center text-sm text-muted-foreground mt-4">
              Already have an account?{" "}
              <Link to="/login" className="text-primary hover:underline font-medium ml-1">
                Sign in
              </Link>
            </p>
          </form>
        )}
      </div>
    </div>
  );
}
