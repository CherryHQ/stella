import { useCallback, useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { formatTime } from "@/lib/time";
import type { OAuthFlow, OAuthProvider, OAuthProviderConfig, VaultEntry } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useI18n } from "@/lib/i18n";
import { meQueryOptions } from "@/lib/queries/me";
import { SettingsDetailLayout } from "@/features/settings/SettingsDetailLayout";

type Toast = { message: string; type: "success" | "error" } | null;
type Section = "vault" | "oauth";

export function CredentialsPage() {
  const { t } = useI18n();
  const { data: me } = useQuery(meQueryOptions);
  const isAdmin = me?.is_admin ?? false;
  const [activeSection, setActiveSection] = useState<Section>("oauth");

  const [vaultEntries, setVaultEntries] = useState<VaultEntry[]>([]);
  const [vaultLoading, setVaultLoading] = useState(false);
  const [vaultSaving, setVaultSaving] = useState(false);
  const [newSecretName, setNewSecretName] = useState("");
  const [newSecretValue, setNewSecretValue] = useState("");

  const [oauthProviders, setOauthProviders] = useState<OAuthProvider[]>([]);
  const [oauthStatus, setOauthStatus] = useState<
    Record<string, "checking" | "connected" | "disconnected">
  >({});
  const [oauthFlow, setOauthFlow] = useState<Record<string, OAuthFlow | null>>({});
  const [oauthFlowActive, setOauthFlowActive] = useState<Record<string, boolean>>({});

  // Configure form state per provider
  const [configOpen, setConfigOpen] = useState<Record<string, boolean>>({});
  const [configValues, setConfigValues] = useState<
    Record<string, { clientId: string; clientSecret: string; redirectUrl: string }>
  >({});
  const [configSaving, setConfigSaving] = useState<Record<string, boolean>>({});

  const [toast, setToast] = useState<Toast>(null);
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
      const providers =
        (await api<OAuthProvider[]>("GET", "/api/auth/profile/oauth/providers")) ?? [];
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

  const [hasExistingSecret, setHasExistingSecret] = useState<Record<string, boolean>>({});

  const loadProviderConfig = useCallback(async (provider: string) => {
    try {
      const cfg = await api<OAuthProviderConfig>(
        "GET",
        `/api/admin/oauth-providers/${provider}/config`,
      );
      if (cfg) {
        setConfigValues((prev) => ({
          ...prev,
          [provider]: {
            clientId: cfg.client_id,
            clientSecret: "",
            redirectUrl: cfg.redirect_url ?? "",
          },
        }));
        setHasExistingSecret((prev) => ({
          ...prev,
          [provider]: cfg.client_secret === "***",
        }));
      }
    } catch {
      // not admin or no config yet — ignore
    }
  }, []);

  const checkOAuthConnected = useCallback(async (provider: string) => {
    setOauthStatus((prev) => ({ ...prev, [provider]: "checking" }));
    try {
      const data = await api<{ connected: boolean }>(
        "GET",
        `/api/auth/profile/oauth/${provider}/connected`,
      );
      setOauthStatus((prev) => ({
        ...prev,
        [provider]: data?.connected ? "connected" : "disconnected",
      }));
    } catch {
      setOauthStatus((prev) => ({ ...prev, [provider]: "disconnected" }));
    }
  }, []);

  useEffect(() => {
    const init = async () => {
      await loadVaultEntries();
      const providers = await loadOAuthProviders();
      await Promise.all([
        ...providers.map((p) => checkOAuthConnected(p.provider)),
        ...(isAdmin ? providers.map((p) => loadProviderConfig(p.provider)) : []),
      ]);
    };
    void init();
    return () => {
      if (toastTimerRef.current) clearTimeout(toastTimerRef.current);
      for (const key of Object.keys(pollAbortRef.current)) {
        pollAbortRef.current[key] = true;
      }
    };
  }, [loadVaultEntries, loadOAuthProviders, checkOAuthConnected, loadProviderConfig, isAdmin]);

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
      await api("PUT", `/api/auth/profile/vault/${encodeURIComponent(newSecretName)}`, {
        value: newSecretValue,
      });
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
          status = await api<{ state: string }>(
            "GET",
            `/api/auth/profile/oauth/${provider}/status/${flowID}`,
          );
        } catch {
          break;
        }
        if (!status || status.state !== "pending") {
          if (status?.state === "authorized") showToast(`${provider} connected successfully`);
          else if (status) showToast(`${provider} authorization ${status.state}`, "error");
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

  const saveProviderConfig = useCallback(
    async (provider: string) => {
      const vals = configValues[provider];
      if (!vals?.clientId) {
        showToast("Client ID is required", "error");
        return;
      }
      setConfigSaving((prev) => ({ ...prev, [provider]: true }));
      try {
        await api("PUT", `/api/admin/oauth-providers/${provider}/config`, {
          client_id: vals.clientId,
          client_secret: vals.clientSecret,
          redirect_url: vals.redirectUrl || undefined,
        });
        showToast(`${provider} credentials saved`);
        await loadOAuthProviders();
        await loadProviderConfig(provider);
        await checkOAuthConnected(provider);
      } catch (e) {
        showToast(e instanceof Error ? e.message : "Failed to save config", "error");
      } finally {
        setConfigSaving((prev) => ({ ...prev, [provider]: false }));
      }
    },
    [configValues, showToast, loadOAuthProviders, loadProviderConfig, checkOAuthConnected],
  );

  const deleteProviderConfig = useCallback(
    async (provider: string) => {
      if (!window.confirm(`Reset ${provider} credentials to defaults?`)) return;
      try {
        await api("DELETE", `/api/admin/oauth-providers/${provider}/config`);
        showToast(`${provider} credentials reset to defaults`);
        await loadOAuthProviders();
        await loadProviderConfig(provider);
        await checkOAuthConnected(provider);
      } catch (e) {
        showToast(e instanceof Error ? e.message : "Failed to reset config", "error");
      }
    },
    [showToast, loadOAuthProviders, loadProviderConfig, checkOAuthConnected],
  );

  const sections: { id: Section; label: string; subtitle: string }[] = [
    { id: "oauth", label: "OAuth", subtitle: "Provider connections" },
    { id: "vault", label: "Vault", subtitle: "Secret key-value store" },
  ];

  const listHeader = (
    <div className="px-3 py-3 border-b border-border">
      <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
        {t("credentials.title")}
      </span>
    </div>
  );

  const list = (
    <div>
      {sections.map((s) => (
        <button
          key={s.id}
          onClick={() => setActiveSection(s.id)}
          className={`w-full text-left px-3 py-2.5 hover:bg-muted/50 transition-colors ${
            activeSection === s.id ? "bg-primary/8" : ""
          }`}
        >
          <p className="text-sm font-medium leading-tight">{s.label}</p>
          <p className="text-[11px] font-mono text-muted-foreground">{s.subtitle}</p>
        </button>
      ))}
    </div>
  );

  const toastBanner = toast ? (
    <div
      className={`mb-4 rounded-lg border px-3 py-2 text-sm ${
        toast.type === "error"
          ? "border-destructive/36 bg-destructive/8 text-destructive-foreground"
          : "border-success/36 bg-success/8 text-success-foreground"
      }`}
    >
      {toast.message}
    </div>
  ) : null;

  const vaultDetail = (
    <div className="p-6">
      {toastBanner}
      <h2 className="font-serif text-xl mb-1">Vault</h2>
      <p className="text-sm text-muted-foreground mb-6">
        Encrypted secrets injected as environment variables in sandbox sessions.
      </p>

      {vaultLoading && <p className="text-sm text-muted-foreground">Loading…</p>}

      {vaultEntries.length > 0 && (
        <div className="overflow-x-auto mb-6">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-border">
                <th className="pb-2 text-left font-mono text-xs text-muted-foreground">Name</th>
                <th className="pb-2 text-left font-mono text-xs text-muted-foreground">Created</th>
                <th className="pb-2 text-left font-mono text-xs text-muted-foreground">Updated</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {vaultEntries.map((entry) => (
                <tr key={entry.name} className="border-b border-border/50">
                  <td className="py-2 font-mono">{entry.name}</td>
                  <td className="py-2 text-xs text-muted-foreground">
                    {formatTime(entry.created_at)}
                  </td>
                  <td className="py-2 text-xs text-muted-foreground">
                    {formatTime(entry.updated_at)}
                  </td>
                  <td className="py-2 text-right">
                    <Button
                      size="xs"
                      variant="destructive-outline"
                      onClick={() => deleteVaultEntry(entry.name)}
                    >
                      {t("common.delete")}
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {vaultEntries.length === 0 && !vaultLoading && (
        <p className="text-sm text-muted-foreground mb-6">No secrets stored yet.</p>
      )}

      <div className="border-t border-border pt-4">
        <h3 className="text-sm font-medium mb-3">Add Secret</h3>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div className="space-y-1.5">
            <label className="text-sm font-medium">Name</label>
            <Input
              type="text"
              value={newSecretName}
              onChange={(e) => setNewSecretName(e.target.value)}
              placeholder="e.g. MY_API_KEY"
              autoComplete="off"
              nativeInput
            />
          </div>
          <div className="space-y-1.5">
            <label className="text-sm font-medium">Value</label>
            <Input
              type="password"
              value={newSecretValue}
              onChange={(e) => setNewSecretValue(e.target.value)}
              placeholder="secret value"
              autoComplete="new-password"
              nativeInput
            />
          </div>
        </div>
        <div className="mt-4">
          <Button size="sm" loading={vaultSaving} onClick={addVaultEntry}>
            Save Secret
          </Button>
        </div>
      </div>
    </div>
  );

  const oauthDetail = (
    <div className="p-6">
      {toastBanner}
      <h2 className="font-serif text-xl mb-1">OAuth Providers</h2>
      <p className="text-sm text-muted-foreground mb-6">
        Connect your GitHub or Lark/Feishu account so stella can act on your behalf in CLI tools and
        runners.
      </p>

      <div className="flex flex-col gap-4">
        {oauthProviders.map((p) => (
          <div key={p.provider}>
            <div className="flex items-center justify-between gap-4 rounded-lg border border-border p-4">
              <div className="flex items-center gap-3">
                <span className="font-medium text-sm">{p.provider}</span>
                {oauthStatus[p.provider] === "connected" && (
                  <Badge variant="success">Connected</Badge>
                )}
                {oauthStatus[p.provider] === "disconnected" && (
                  <Badge variant="outline">Not connected</Badge>
                )}
                {oauthStatus[p.provider] === "checking" && (
                  <span className="text-xs text-muted-foreground">Checking…</span>
                )}
                {p.configured && <Badge variant="secondary">Configured</Badge>}
              </div>
              <div className="flex items-center gap-2">
                {isAdmin && (
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() =>
                      setConfigOpen((prev) => ({ ...prev, [p.provider]: !prev[p.provider] }))
                    }
                  >
                    {configOpen[p.provider] ? "Hide" : "Configure"}
                  </Button>
                )}
                {p.available && oauthStatus[p.provider] !== "connected" ? (
                  <Button
                    size="sm"
                    loading={oauthFlowActive[p.provider]}
                    onClick={() => connectOAuth(p.provider)}
                  >
                    Connect
                  </Button>
                ) : oauthStatus[p.provider] === "connected" && p.available ? (
                  <Button
                    size="sm"
                    variant="destructive-outline"
                    className="text-destructive"
                    onClick={() => disconnectOAuth(p.provider)}
                  >
                    Disconnect
                  </Button>
                ) : null}
              </div>
            </div>

            {configOpen[p.provider] && (
              <div className="mt-2 rounded-lg border border-border p-4">
                <h3 className="text-sm font-medium mb-3">Configure {p.provider} OAuth App</h3>
                <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                  <div className="space-y-1.5">
                    <label className="text-sm font-medium">Client ID</label>
                    <Input
                      type="text"
                      value={configValues[p.provider]?.clientId ?? ""}
                      onChange={(e) =>
                        setConfigValues((prev) => ({
                          ...prev,
                          [p.provider]: { ...prev[p.provider], clientId: e.target.value },
                        }))
                      }
                      placeholder="OAuth app client ID"
                      autoComplete="off"
                      nativeInput
                    />
                  </div>
                  <div className="space-y-1.5">
                    <label className="text-sm font-medium">Client Secret</label>
                    <Input
                      type="password"
                      value={configValues[p.provider]?.clientSecret ?? ""}
                      onChange={(e) =>
                        setConfigValues((prev) => ({
                          ...prev,
                          [p.provider]: {
                            ...prev[p.provider],
                            clientSecret: e.target.value,
                          },
                        }))
                      }
                      placeholder={
                        hasExistingSecret[p.provider]
                          ? "Leave empty to keep existing"
                          : "OAuth app client secret"
                      }
                      autoComplete="new-password"
                      nativeInput
                    />
                  </div>
                </div>
                <div className="mt-4 flex items-center gap-2">
                  <Button
                    size="sm"
                    loading={configSaving[p.provider]}
                    onClick={() => saveProviderConfig(p.provider)}
                  >
                    Save
                  </Button>
                  <Button
                    size="sm"
                    variant="destructive-outline"
                    onClick={() => deleteProviderConfig(p.provider)}
                  >
                    Reset to defaults
                  </Button>
                </div>
              </div>
            )}

            {oauthFlow[p.provider] && (
              <div className="mt-2 rounded-lg border border-info/36 bg-info/8 p-4 text-sm">
                <p className="font-medium">Authorize stella:</p>
                <a
                  href={oauthFlow[p.provider]!.verification_uri}
                  target="_blank"
                  rel="noreferrer"
                  className="font-mono text-xs break-all text-primary underline"
                >
                  {oauthFlow[p.provider]!.verification_uri}
                </a>
                {oauthFlow[p.provider]!.user_code && (
                  <p className="mt-1">
                    Code:{" "}
                    <span className="font-mono font-bold">{oauthFlow[p.provider]!.user_code}</span>
                  </p>
                )}
                <p className="mt-1 text-xs text-muted-foreground">Waiting for authorization…</p>
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  );

  const detail = activeSection === "vault" ? vaultDetail : oauthDetail;

  return (
    // Escape the p-8 px-10 padding from SettingsLayout's outlet wrapper
    <div className="h-full">
      <SettingsDetailLayout listHeader={listHeader} list={list} detail={detail} />
    </div>
  );
}
