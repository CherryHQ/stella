import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { formatTime } from "@/lib/time";
import type { OAuthFlow, OAuthProvider, VaultEntry } from "@/lib/types";

export function CredentialsPage() {
  const [vaultEntries, setVaultEntries] = useState<VaultEntry[]>([]);
  const [vaultLoading, setVaultLoading] = useState(false);
  const [vaultSaving, setVaultSaving] = useState(false);
  const [newSecretName, setNewSecretName] = useState("");
  const [newSecretValue, setNewSecretValue] = useState("");

  const [oauthProviders, setOauthProviders] = useState<OAuthProvider[]>([]);
  const [oauthStatus, setOauthStatus] = useState<Record<string, "checking" | "connected" | "disconnected">>({});
  const [oauthFlow, setOauthFlow] = useState<Record<string, OAuthFlow | null>>({});
  const [oauthFlowActive, setOauthFlowActive] = useState<Record<string, boolean>>({});

  const [toast, setToast] = useState<{ message: string; type: "success" | "error" } | null>(null);
  const toastTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const pollAbortRef = useRef<Record<string, boolean>>({});

  const showToast = useCallback((message: string, type: "success" | "error" = "success") => {
    setToast({ message, type });
    if (toastTimerRef.current) clearTimeout(toastTimerRef.current);
    toastTimerRef.current = setTimeout(() => setToast(null), 3000);
  }, []);

  const loadVaultEntries = useCallback(async () => {
    setVaultLoading(true);
    try {
      const entries = (await api<VaultEntry[]>("GET", "/api/auth/profile/vault")) ?? [];
      setVaultEntries(entries);
    } catch {
      setVaultEntries([]);
    } finally {
      setVaultLoading(false);
    }
  }, []);

  const loadOAuthProviders = useCallback(async () => {
    try {
      const providers = (await api<OAuthProvider[]>("GET", "/api/auth/profile/oauth/providers")) ?? [];
      setOauthProviders(providers);
      setOauthStatus((prev) => {
        const next = { ...prev };
        for (const p of providers) {
          if (!next[p.provider]) next[p.provider] = "checking";
        }
        return next;
      });
      setOauthFlow((prev) => {
        const next = { ...prev };
        for (const p of providers) {
          if (!(p.provider in next)) next[p.provider] = null;
        }
        return next;
      });
      setOauthFlowActive((prev) => {
        const next = { ...prev };
        for (const p of providers) {
          if (!(p.provider in next)) next[p.provider] = false;
        }
        return next;
      });
      return providers;
    } catch {
      setOauthProviders([]);
      return [];
    }
  }, []);

  const checkOAuthConnected = useCallback(async (provider: string) => {
    setOauthStatus((prev) => ({ ...prev, [provider]: "checking" }));
    try {
      const data = await api<{ connected: boolean }>("GET", `/api/auth/profile/oauth/${provider}/connected`);
      setOauthStatus((prev) => ({ ...prev, [provider]: data?.connected ? "connected" : "disconnected" }));
    } catch {
      setOauthStatus((prev) => ({ ...prev, [provider]: "disconnected" }));
    }
  }, []);

  useEffect(() => {
    const init = async () => {
      await loadVaultEntries();
      const providers = await loadOAuthProviders();
      await Promise.all(providers.map((p) => checkOAuthConnected(p.provider)));
    };
    init();
    return () => {
      if (toastTimerRef.current) clearTimeout(toastTimerRef.current);
      // Signal all ongoing polls to stop
      for (const key of Object.keys(pollAbortRef.current)) {
        pollAbortRef.current[key] = true;
      }
    };
  }, [loadVaultEntries, loadOAuthProviders, checkOAuthConnected]);

  const addVaultEntry = useCallback(async () => {
    if (!newSecretName) {
      showToast("Secret name is required", "error");
      return;
    }
    if (!newSecretValue) {
      showToast("Secret value is required", "error");
      return;
    }
    setVaultSaving(true);
    try {
      await api("PUT", `/api/auth/profile/vault/${encodeURIComponent(newSecretName)}`, { value: newSecretValue });
      showToast("Secret saved");
      setNewSecretName("");
      setNewSecretValue("");
      await loadVaultEntries();
    } catch (e) {
      showToast(e instanceof Error ? e.message : "Failed to save secret", "error");
    } finally {
      setVaultSaving(false);
    }
  }, [newSecretName, newSecretValue, showToast, loadVaultEntries]);

  const deleteVaultEntry = useCallback(
    async (name: string) => {
      if (!window.confirm(`Delete secret "${name}"?`)) return;
      try {
        await api("DELETE", `/api/auth/profile/vault/${encodeURIComponent(name)}`);
        showToast("Secret deleted");
        await loadVaultEntries();
      } catch (e) {
        showToast(e instanceof Error ? e.message : "Failed to delete secret", "error");
      }
    },
    [showToast, loadVaultEntries],
  );

  const pollUntilDone = useCallback(
    async (provider: string, flowID: string) => {
      pollAbortRef.current[provider] = false;
      while (!pollAbortRef.current[provider]) {
        await new Promise((r) => setTimeout(r, 3000));
        if (pollAbortRef.current[provider]) break;
        let status: { state: string } | null = null;
        try {
          status = await api<{ state: string }>("GET", `/api/auth/profile/oauth/${provider}/status/${flowID}`);
        } catch {
          break;
        }
        if (!status || status.state !== "pending") {
          if (status?.state === "authorized") {
            showToast(`${provider} connected successfully`);
          } else if (status) {
            showToast(`${provider} authorization ${status.state}`, "error");
          }
          break;
        }
      }
    },
    [showToast],
  );

  const connectOAuth = useCallback(
    async (provider: string) => {
      setOauthFlowActive((prev) => ({ ...prev, [provider]: true }));
      setOauthFlow((prev) => ({ ...prev, [provider]: null }));
      try {
        const flow = await api<OAuthFlow>("POST", `/api/auth/profile/oauth/${provider}/start`);
        setOauthFlow((prev) => ({ ...prev, [provider]: flow }));
        await pollUntilDone(provider, flow.flow_id);
      } catch (e) {
        showToast(e instanceof Error ? e.message : "OAuth error", "error");
      } finally {
        setOauthFlowActive((prev) => ({ ...prev, [provider]: false }));
        setOauthFlow((prev) => ({ ...prev, [provider]: null }));
        await checkOAuthConnected(provider);
      }
    },
    [pollUntilDone, showToast, checkOAuthConnected],
  );

  const disconnectOAuth = useCallback(
    async (provider: string) => {
      if (!window.confirm(`Disconnect ${provider} credentials?`)) return;
      try {
        await api("DELETE", `/api/auth/profile/oauth/${provider}`);
        showToast(`${provider} disconnected`);
        await checkOAuthConnected(provider);
      } catch (e) {
        showToast(e instanceof Error ? e.message : "Failed to disconnect", "error");
      }
    },
    [showToast, checkOAuthConnected],
  );

  return (
    <div>
      {/* Page Header */}
      <div className="mb-6">
        <h1 className="font-serif text-3xl tracking-tight">Credentials</h1>
        <p className="text-sm text-base-content/60 mt-1">Manage OAuth connections and vault secrets.</p>
      </div>

      {/* Toast */}
      {toast && (
        <div className={`alert ${toast.type === "error" ? "alert-error" : "alert-success"} mb-4 text-sm`}>
          {toast.message}
        </div>
      )}

      {/* OAuth CLI */}
      <div className="mb-10">
        <h2 className="font-serif text-xl mb-4">OAuth CLI Credentials</h2>
        <div className="card bg-base-200">
          <div className="card-body">
            <p className="text-sm text-secondary">
              Connect your GitHub or Lark/Feishu account so anna can act on your behalf in CLI tools and runners.
            </p>
            <div className="mt-4 flex flex-col gap-4">
              {oauthProviders.map((p) => (
                <div key={p.provider}>
                  <div className="flex items-center justify-between gap-4 border border-base-300 rounded-lg p-4">
                    <div className="flex items-center gap-3">
                      <span className="font-medium text-sm">{p.provider}</span>
                      {oauthStatus[p.provider] === "connected" && (
                        <span className="badge badge-success badge-sm">Connected</span>
                      )}
                      {oauthStatus[p.provider] === "disconnected" && (
                        <span className="badge badge-ghost badge-sm">Not connected</span>
                      )}
                      {oauthStatus[p.provider] === "checking" && (
                        <span className="loading loading-spinner loading-xs"></span>
                      )}
                    </div>
                    <div>
                      {oauthStatus[p.provider] !== "connected" ? (
                        <button
                          onClick={() => connectOAuth(p.provider)}
                          disabled={oauthFlowActive[p.provider]}
                          className="btn btn-primary btn-sm"
                        >
                          {oauthFlowActive[p.provider] && (
                            <span className="loading loading-spinner loading-xs"></span>
                          )}
                          Connect
                        </button>
                      ) : (
                        <button
                          onClick={() => disconnectOAuth(p.provider)}
                          className="btn btn-error btn-sm btn-outline"
                        >
                          Disconnect
                        </button>
                      )}
                    </div>
                  </div>
                  {oauthFlow[p.provider] && (
                    <div className="alert alert-info text-sm flex flex-col items-start gap-1 p-4">
                      <p>Authorize anna:</p>
                      <a
                        href={oauthFlow[p.provider]!.verification_uri}
                        target="_blank"
                        rel="noreferrer"
                        className="link font-mono text-xs break-all"
                      >
                        {oauthFlow[p.provider]!.verification_uri}
                      </a>
                      {oauthFlow[p.provider]!.user_code && (
                        <p>
                          Code:{" "}
                          <span className="font-mono font-bold">{oauthFlow[p.provider]!.user_code}</span>
                        </p>
                      )}
                      <p className="text-xs text-secondary">Waiting for authorization…</p>
                    </div>
                  )}
                </div>
              ))}
            </div>
          </div>
        </div>
      </div>

      {/* Vault */}
      <div className="mb-10">
        <h2 className="font-serif text-xl mb-4">Secret Vault</h2>
        <div className="card bg-base-200">
          <div className="card-body">
            <p className="text-sm text-secondary">
              Encrypted secrets injected as environment variables in sandbox sessions.
            </p>

            {/* Entry list */}
            {vaultEntries.length > 0 && (
              <div className="mt-4">
                <table className="table table-sm w-full">
                  <thead>
                    <tr>
                      <th className="font-mono text-xs">Name</th>
                      <th className="font-mono text-xs">Created</th>
                      <th className="font-mono text-xs">Updated</th>
                      <th></th>
                    </tr>
                  </thead>
                  <tbody>
                    {vaultEntries.map((entry) => (
                      <tr key={entry.name}>
                        <td className="font-mono text-sm">{entry.name}</td>
                        <td className="text-xs text-secondary">{formatTime(entry.created_at)}</td>
                        <td className="text-xs text-secondary">{formatTime(entry.updated_at)}</td>
                        <td className="text-right">
                          <button
                            onClick={() => deleteVaultEntry(entry.name)}
                            className="btn btn-ghost btn-xs btn-error"
                          >
                            Delete
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}

            {vaultEntries.length === 0 && !vaultLoading && (
              <div className="mt-4">
                <p className="text-sm text-secondary">No secrets stored yet.</p>
              </div>
            )}

            {/* Add secret form */}
            <div className="mt-4 border-t border-base-300 pt-4">
              <h3 className="text-sm font-medium mb-3">Add Secret</h3>
              <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                <div className="form-control">
                  <label className="label">
                    <span className="label-text text-sm">Name</span>
                  </label>
                  <input
                    type="text"
                    value={newSecretName}
                    onChange={(e) => setNewSecretName(e.target.value)}
                    className="input input-bordered w-full text-sm font-mono"
                    placeholder="e.g. MY_API_KEY"
                    autoComplete="off"
                  />
                </div>
                <div className="form-control">
                  <label className="label">
                    <span className="label-text text-sm">Value</span>
                  </label>
                  <input
                    type="password"
                    value={newSecretValue}
                    onChange={(e) => setNewSecretValue(e.target.value)}
                    className="input input-bordered w-full text-sm"
                    placeholder="secret value"
                    autoComplete="new-password"
                  />
                </div>
              </div>
              <div className="mt-4">
                <button onClick={addVaultEntry} disabled={vaultSaving} className="btn btn-primary btn-sm">
                  {vaultSaving && <span className="loading loading-spinner loading-xs"></span>}
                  Save Secret
                </button>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
