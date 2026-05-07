import { useState } from "react";

export function LoginPage() {
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
      window.location.href = "/";
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
      window.location.href = "/";
    } catch (e) {
      setError((e as Error).message || "Registration failed");
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-base-100">
      <div className="card bg-base-200 shadow-xl w-full max-w-sm">
        <div className="card-body">
          {/* Logo */}
          <div className="text-center mb-4">
            <span className="font-serif italic text-primary text-3xl tracking-tight select-none">anna</span>
            <p className="text-secondary text-sm mt-1">Admin Panel</p>
          </div>
          {/* Error message */}
          {error && (
            <div className="alert alert-error text-sm mb-4">
              <span>{error}</span>
            </div>
          )}
          {/* Login form */}
          {!isRegister && (
            <form onSubmit={login}>
              <div className="form-control w-full mb-3">
                <label className="label">
                  <span className="label-text font-mono font-medium text-sm">Username</span>
                </label>
                <input
                  type="text"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  placeholder="username"
                  className="input input-bordered w-full text-sm"
                  required
                  autoComplete="username"
                />
              </div>
              <div className="form-control w-full mb-5">
                <label className="label">
                  <span className="label-text font-mono font-medium text-sm">Password</span>
                </label>
                <input
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  placeholder="password"
                  className="input input-bordered w-full text-sm"
                  required
                  autoComplete="current-password"
                />
              </div>
              <button type="submit" disabled={loading} className="btn btn-primary w-full">
                {loading && <span className="loading loading-spinner loading-xs"></span>}
                {loading ? "Signing in..." : "Sign in"}
              </button>
            </form>
          )}
          {/* Register form */}
          {isRegister && (
            <form onSubmit={register}>
              <div className="form-control w-full mb-3">
                <label className="label">
                  <span className="label-text font-mono font-medium text-sm">Username</span>
                </label>
                <input
                  type="text"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  placeholder="username"
                  className="input input-bordered w-full text-sm"
                  required
                  autoComplete="username"
                />
              </div>
              <div className="form-control w-full mb-3">
                <label className="label">
                  <span className="label-text font-mono font-medium text-sm">Password</span>
                </label>
                <input
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  placeholder="min 8 characters"
                  className="input input-bordered w-full text-sm"
                  required
                  minLength={8}
                  autoComplete="new-password"
                />
              </div>
              <div className="form-control w-full mb-5">
                <label className="label">
                  <span className="label-text font-mono font-medium text-sm">Confirm Password</span>
                </label>
                <input
                  type="password"
                  value={confirmPassword}
                  onChange={(e) => setConfirmPassword(e.target.value)}
                  placeholder="confirm password"
                  className="input input-bordered w-full text-sm"
                  required
                  minLength={8}
                  autoComplete="new-password"
                />
              </div>
              <button type="submit" disabled={loading} className="btn btn-primary w-full">
                {loading && <span className="loading loading-spinner loading-xs"></span>}
                {loading ? "Creating account..." : "Create account"}
              </button>
            </form>
          )}
          {/* Toggle */}
          <div className="text-center mt-4">
            <button
              type="button"
              onClick={toggleMode}
              className="btn btn-ghost btn-sm text-secondary"
            >
              {isRegister ? "Already have an account? Sign in" : "Need an account? Register"}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
