import { useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { queryClient } from "@/lib/queryClient";
import { meQueryOptions } from "@/lib/queries/me";

export function LoginPage() {
  const navigate = useNavigate();
  const [isRegister, setIsRegister] = useState(false);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

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
      const res = await fetch("/api/auth/login", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username, password }),
      });
      const json = await res.json();
      if (json.error) {
        setError(json.error);
        return;
      }
      await queryClient.invalidateQueries(meQueryOptions);
      navigate({ to: "/agents" as any });
    } catch (e) {
      setError((e as Error).message || "Login failed");
    } finally {
      setLoading(false);
    }
  }

  async function register(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    if (password !== confirmPassword) {
      setError("Passwords do not match");
      return;
    }
    if (password.length < 8) {
      setError("Password must be at least 8 characters");
      return;
    }
    setLoading(true);
    try {
      const res = await fetch("/api/auth/register", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username, password }),
      });
      const json = await res.json();
      if (json.error) {
        setError(json.error);
        return;
      }
      await queryClient.invalidateQueries(meQueryOptions);
      navigate({ to: "/agents" as any });
    } catch (e) {
      setError((e as Error).message || "Registration failed");
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-background">
      <div className="w-full max-w-sm rounded-xl border border-border bg-card shadow-sm p-8">
        <div className="text-center mb-6">
          <span className="font-serif italic text-primary text-3xl tracking-tight select-none">
            anna
          </span>
          <p className="text-muted-foreground text-sm mt-1">Admin Panel</p>
        </div>

        {error && (
          <div className="mb-4 rounded-lg border border-destructive/36 bg-destructive/8 px-3 py-2 text-sm text-destructive-foreground">
            {error}
          </div>
        )}

        {!isRegister && (
          <form onSubmit={login} className="space-y-4">
            <div className="space-y-1.5">
              <label className="text-sm font-medium font-mono">Username</label>
              <Input
                type="text"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                placeholder="username"
                required
                autoComplete="username"
                nativeInput
              />
            </div>
            <div className="space-y-1.5">
              <label className="text-sm font-medium font-mono">Password</label>
              <Input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder="password"
                required
                autoComplete="current-password"
                nativeInput
              />
            </div>
            <Button type="submit" loading={loading} className="w-full">
              {loading ? "Signing in…" : "Sign in"}
            </Button>
          </form>
        )}

        {isRegister && (
          <form onSubmit={register} className="space-y-4">
            <div className="space-y-1.5">
              <label className="text-sm font-medium font-mono">Username</label>
              <Input
                type="text"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                placeholder="username"
                required
                autoComplete="username"
                nativeInput
              />
            </div>
            <div className="space-y-1.5">
              <label className="text-sm font-medium font-mono">Password</label>
              <Input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder="min 8 characters"
                required
                minLength={8}
                autoComplete="new-password"
                nativeInput
              />
            </div>
            <div className="space-y-1.5">
              <label className="text-sm font-medium font-mono">Confirm Password</label>
              <Input
                type="password"
                value={confirmPassword}
                onChange={(e) => setConfirmPassword(e.target.value)}
                placeholder="confirm password"
                required
                minLength={8}
                autoComplete="new-password"
                nativeInput
              />
            </div>
            <Button type="submit" loading={loading} className="w-full">
              {loading ? "Creating account…" : "Create account"}
            </Button>
          </form>
        )}

        <div className="text-center mt-4">
          <Button type="button" variant="ghost" size="sm" onClick={toggleMode}>
            {isRegister ? "Already have an account? Sign in" : "Need an account? Register"}
          </Button>
        </div>
      </div>
    </div>
  );
}
