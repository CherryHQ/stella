import { useCallback, useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  deleteOAuthProviderConfig,
  deleteVaultEntry as deleteVaultEntryRequest,
  disconnectOAuth as disconnectOAuthRequest,
  getOAuthConnected,
  getOAuthProviderConfig,
  listOAuthProviders,
  listVaultEntries,
  pollOAuthFlow,
  setOAuthProviderConfig,
  setVaultEntry,
  startOAuthFlow,
} from "@/lib/api-client/sdk.gen";
import { formatTime } from "@/lib/time";
import type { OAuthFlow, OAuthProvider, VaultEntry } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useI18n } from "@/lib/i18n";
import { useToast, ToastContainer } from "@/hooks/use-toast";
import { meQueryOptions } from "@/lib/queries/me";
import { SettingsPageHeader } from "@/features/settings/SettingsPageHeader";
import { EmailAccountsPanel } from "@/features/credentials/EmailAccountsPanel";
import { siGithub, siX } from "simple-icons";

const SIMPLE_ICON_PATHS: Record<string, string> = {
  github: siGithub.path,
  x: siX.path,
};

function ProviderIcon({ icon, label }: { icon?: string; label: string }) {
  if (!icon) {
    return <span className="size-4 shrink-0 rounded-full bg-muted" aria-hidden="true" />;
  }

  const [family, name] = icon.split(":");
  const path = family === "simpleicons" ? simpleIconPath(name) : undefined;
  if (!path) {
    return <span className="size-4 shrink-0 rounded-full bg-muted" aria-hidden="true" />;
  }

  return (
    <svg viewBox="0 0 24 24" className="size-4 shrink-0" fill="currentColor" aria-label={label}>
      <path d={path} />
    </svg>
  );
}

function simpleIconPath(name: string) {
  return SIMPLE_ICON_PATHS[name.toLowerCase()];
}

export function CredentialsPage() {
  const { t } = useI18n();
  const { data: me } = useQuery(meQueryOptions);
  const isAdmin = me?.is_admin ?? false;

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

  const { toasts, showToast } = useToast();
  const pollAbortRef = useRef<Record<string, boolean>>({});

  const loadVaultEntries = useCallback(async () => {
    setVaultLoading(true);
    try {
      const { data } = await listVaultEntries({ throwOnError: true });
      const entries = (data?.entries as VaultEntry[]) ?? [];
      setVaultEntries(entries);
    } catch {
      setVaultEntries([]);
    } finally {
      setVaultLoading(false);
    }
  }, []);

  const loadOAuthProviders = useCallback(async () => {
    try {
      const { data } = await listOAuthProviders({ throwOnError: true });
      const providers = (data?.providers as OAuthProvider[]) ?? [];
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
      const { data: cfg } = await getOAuthProviderConfig({
        path: { id: provider },
        throwOnError: true,
      });
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
      const { data } = await getOAuthConnected({ path: { provider }, throwOnError: true });
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
      await setVaultEntry({
        path: { name: newSecretName },
        body: { value: newSecretValue },
        throwOnError: true,
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
        await deleteVaultEntryRequest({ path: { name }, throwOnError: true });
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
          const { data } = await pollOAuthFlow({
            path: { provider, flowId: flowID },
            throwOnError: true,
          });
          status = data as { state: string };
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
        const { data } = await startOAuthFlow({ path: { provider }, throwOnError: true });
        const flow = data as OAuthFlow;
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
        await disconnectOAuthRequest({ path: { provider }, throwOnError: true });
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
        await setOAuthProviderConfig({
          path: { id: provider },
          body: {
            client_id: vals.clientId,
            client_secret: vals.clientSecret,
            redirect_url: vals.redirectUrl || undefined,
          },
          throwOnError: true,
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
        await deleteOAuthProviderConfig({ path: { id: provider }, throwOnError: true });
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

  const filteredVaultEntries = vaultEntries.filter((entry) => entry.name !== "EMAIL_CONFIG");

  const vaultDetail = (
    <div className="rounded-xl border border-border bg-card p-6 shadow-none space-y-6">
      {vaultLoading && <p className="text-sm text-muted-foreground">Loading…</p>}

      {filteredVaultEntries.length > 0 && (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-border">
                <th className="pb-2 text-left font-mono text-xs text-muted-foreground font-semibold">
                  Name
                </th>
                <th className="pb-2 text-left font-mono text-xs text-muted-foreground font-semibold">
                  Created
                </th>
                <th className="pb-2 text-left font-mono text-xs text-muted-foreground font-semibold">
                  Updated
                </th>
                <th></th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {filteredVaultEntries.map((entry) => (
                <tr key={entry.name}>
                  <td className="py-2.5 font-mono text-foreground font-medium">{entry.name}</td>
                  <td className="py-2.5 text-xs text-muted-foreground">
                    {formatTime(entry.created_at)}
                  </td>
                  <td className="py-2.5 text-xs text-muted-foreground">
                    {formatTime(entry.updated_at)}
                  </td>
                  <td className="py-2.5 text-right">
                    <Button
                      size="xs"
                      variant="destructive-outline"
                      className="text-destructive hover:bg-destructive/10 cursor-pointer duration-120"
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

      {filteredVaultEntries.length === 0 && !vaultLoading && (
        <p className="text-sm text-muted-foreground py-4 text-center">No secrets stored yet.</p>
      )}

      <div className="border-t border-border pt-5 space-y-4">
        <h3 className="text-sm font-semibold text-foreground/90 font-sans">Add Secret</h3>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div className="space-y-1.5">
            <label className="text-xs font-medium text-muted-foreground">Name</label>
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
            <label className="text-xs font-medium text-muted-foreground">Value</label>
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
        <div className="flex justify-end pt-2">
          <Button
            size="sm"
            loading={vaultSaving}
            onClick={addVaultEntry}
            className="cursor-pointer duration-120"
          >
            Save Secret
          </Button>
        </div>
      </div>
    </div>
  );

  const oauthDetail = (
    <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
      {oauthProviders.map((p) => {
        const status = oauthStatus[p.provider];
        const connected = status === "connected";
        const ready = p.available && !connected;
        const needsSetup = !p.available;
        const statusLabel = connected
          ? "Connected"
          : ready
            ? "Ready to connect"
            : needsSetup
              ? "Setup required"
              : "Checking";
        const statusVariant = connected ? "success" : ready ? "secondary" : "outline";
        const clientId = configValues[p.provider]?.clientId ?? "";
        const clientIdPreview =
          clientId.length > 12 ? `${clientId.slice(0, 6)}...${clientId.slice(-4)}` : clientId;
        const appLabel = p.configured
          ? clientIdPreview
            ? clientIdPreview
            : "Configured"
          : "Not configured";
        const accountLabel = connected
          ? "Connected"
          : status === "checking"
            ? "Checking"
            : "Not connected";

        return (
          <div
            key={p.provider}
            className="min-w-0 rounded-xl border border-border bg-card p-5 shadow-none flex flex-col justify-between"
          >
            <div>
              <div className="flex min-w-0 items-start justify-between gap-3">
                <div className="flex min-w-0 items-center gap-3">
                  <ProviderIcon icon={p.icon} label={p.provider} />
                  <span className="min-w-0 truncate text-sm font-medium">{p.provider}</span>
                </div>
                <Badge variant={statusVariant}>{statusLabel}</Badge>
              </div>

              <div className="mt-3 space-y-1 text-xs text-muted-foreground">
                <div>
                  <span className="text-foreground font-medium">App:</span> {appLabel}
                </div>
                <div>
                  <span className="text-foreground font-medium">Account:</span> {accountLabel}
                </div>
              </div>

              <div className="mt-4 flex flex-wrap items-center gap-2">
                {ready ? (
                  <Button
                    size="sm"
                    loading={oauthFlowActive[p.provider]}
                    onClick={() => connectOAuth(p.provider)}
                    className="cursor-pointer duration-120"
                  >
                    Connect
                  </Button>
                ) : connected && p.available ? (
                  <Button
                    size="sm"
                    variant="destructive-outline"
                    className="text-destructive hover:bg-destructive/10 cursor-pointer duration-120"
                    onClick={() => disconnectOAuth(p.provider)}
                  >
                    Disconnect
                  </Button>
                ) : null}
                {isAdmin && (
                  <Button
                    size="sm"
                    variant={needsSetup ? "default" : "outline"}
                    className="cursor-pointer duration-120"
                    onClick={() =>
                      setConfigOpen((prev) => ({ ...prev, [p.provider]: !prev[p.provider] }))
                    }
                  >
                    {configOpen[p.provider] ? "Hide app" : needsSetup ? "Set up" : "Edit app"}
                  </Button>
                )}
              </div>
            </div>

            {configOpen[p.provider] && (
              <div className="mt-4 border-t border-border pt-4 space-y-3">
                <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
                  <div className="space-y-1">
                    <label className="text-xs font-medium text-muted-foreground">
                      Client ID{p.configured ? "" : " required"}
                    </label>
                    <Input
                      type="text"
                      value={configValues[p.provider]?.clientId ?? ""}
                      onChange={(e) =>
                        setConfigValues((prev) => ({
                          ...prev,
                          [p.provider]: { ...prev[p.provider], clientId: e.target.value },
                        }))
                      }
                      placeholder={p.configured ? appLabel : "OAuth app client ID"}
                      autoComplete="off"
                      nativeInput
                    />
                  </div>
                  <div className="space-y-1">
                    <label className="text-xs font-medium text-muted-foreground">
                      Client Secret{p.configured ? "" : " required"}
                    </label>
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
                          ? "Keep existing secret"
                          : p.configured
                            ? "Configured"
                            : "OAuth app client secret"
                      }
                      autoComplete="new-password"
                      nativeInput
                    />
                  </div>
                </div>
                <div className="flex items-center gap-2 pt-1.5 justify-end">
                  {p.configured && (
                    <Button
                      size="sm"
                      variant="ghost"
                      className="text-destructive hover:bg-destructive/10 cursor-pointer duration-120"
                      onClick={() => deleteProviderConfig(p.provider)}
                    >
                      Reset
                    </Button>
                  )}
                  <Button
                    size="sm"
                    loading={configSaving[p.provider]}
                    onClick={() => saveProviderConfig(p.provider)}
                    className="cursor-pointer duration-120"
                  >
                    Save
                  </Button>
                </div>
              </div>
            )}

            {oauthFlow[p.provider] && (
              <div className="mt-3 rounded-lg border border-info/36 bg-info/8 p-3 text-xs">
                <p className="font-semibold">Authorize stella:</p>
                <a
                  href={oauthFlow[p.provider]!.verification_uri}
                  target="_blank"
                  rel="noreferrer"
                  className="font-mono text-xs break-all text-primary underline block mt-1"
                >
                  {oauthFlow[p.provider]!.verification_uri}
                </a>
                {oauthFlow[p.provider]!.user_code && (
                  <p className="mt-1 font-medium">
                    Code:{" "}
                    <span className="font-mono font-bold text-foreground">
                      {oauthFlow[p.provider]!.user_code}
                    </span>
                  </p>
                )}
                <p className="mt-1 text-[11px] text-muted-foreground">Waiting for authorization…</p>
              </div>
            )}
          </div>
        );
      })}
    </div>
  );

  return (
    <div className="h-full overflow-y-auto bg-background">
      <div className="mx-auto max-w-3xl p-6 sm:p-8 lg:p-10 space-y-8">
        <SettingsPageHeader
          title={t("credentials.title")}
          description="Manage vault secrets and third-party OAuth app authorizations."
        />

        <div className="space-y-8">
          {/* OAuth Providers Section */}
          <div className="space-y-4">
            <div className="flex items-center gap-2 border-b border-border pb-2">
              <svg
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2"
                className="size-4 shrink-0 text-muted-foreground/80"
              >
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  d="M13.19 8.688a4.5 4.5 0 0 1 1.242 7.244l-4.5 4.5a4.5 4.5 0 0 1-6.364-6.364l1.757-1.757m13.35-.622 1.757-1.757a4.5 4.5 0 0 0-6.364-6.364l-4.5 4.5a4.5 4.5 0 0 0 1.242 7.244"
                />
              </svg>
              <h4 className="font-mono text-[9px] uppercase tracking-[0.08em] text-muted-foreground/60">
                OAuth Providers
              </h4>
            </div>
            <div className="space-y-4">{oauthDetail}</div>
          </div>

          {/* Email Accounts Section */}
          <div className="space-y-4">
            <div className="flex items-center gap-2 border-b border-border pb-2">
              <svg
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2"
                className="size-4 shrink-0 text-muted-foreground/80"
              >
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  d="M21.75 6.75v10.5a2.25 2.25 0 0 1-2.25 2.25h-15a2.25 2.25 0 0 1-2.25-2.25V6.75m19.5 0A2.25 2.25 0 0 0 19.5 4.5h-15a2.25 2.25 0 0 0-2.25 2.25m19.5 0v.243a2.25 2.25 0 0 1-1.07 1.916l-7.5 4.615a2.25 2.25 0 0 1-2.36 0L3.32 8.91a2.25 2.25 0 0 1-1.07-1.916V6.75"
                />
              </svg>
              <h4 className="font-mono text-[9px] uppercase tracking-[0.08em] text-muted-foreground/60">
                Email Accounts
              </h4>
            </div>
            <EmailAccountsPanel showToast={showToast} />
          </div>

          {/* Vault Secrets Section */}
          <div className="space-y-4">
            <div className="flex items-center gap-2 border-b border-border pb-2">
              <svg
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2"
                className="size-4 shrink-0 text-muted-foreground/80"
              >
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  d="M16.5 10.5V6.75a4.5 4.5 0 1 0-9 0v3.75m-.75 11.25h10.5a2.25 2.25 0 0 0 2.25-2.25v-6.75a2.25 2.25 0 0 0-2.25-2.25H6.75a2.25 2.25 0 0 0-2.25 2.25v6.75a2.25 2.25 0 0 0 2.25 2.25Z"
                />
              </svg>
              <h4 className="font-mono text-[9px] uppercase tracking-[0.08em] text-muted-foreground/60">
                Vault Secrets
              </h4>
            </div>
            <div className="space-y-4">{vaultDetail}</div>
          </div>
        </div>
      </div>
      <ToastContainer messages={toasts} />
    </div>
  );
}
