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
import { EmailAccountsPanel } from "@/features/credentials/EmailAccountsPanel";
import {
  SettingsDetailSheet,
  SettingsGridPage,
  SettingsList,
  SettingsRow,
  SettingsSection,
} from "@/features/settings/SettingsCardGrid";
import type { RowAction } from "@/features/settings/SettingsCardGrid";
import {
  DetailPanel,
  DetailPanelHeader,
  FormSectionTitle,
} from "@/features/settings/SettingsDetailPanel";
import { KeyRound, Plug, Plus } from "lucide-react";
import { siGithub, siX } from "simple-icons";

const SIMPLE_ICON_PATHS: Record<string, string> = {
  github: siGithub.path,
  x: siX.path,
};

function ProviderIcon({ icon, label }: { icon?: string; label: string }) {
  const [family, name] = (icon ?? "").split(":");
  const path = family === "simpleicons" ? SIMPLE_ICON_PATHS[name?.toLowerCase()] : undefined;
  if (!path) return <Plug className="size-4" />;
  return (
    <svg viewBox="0 0 24 24" className="size-4" fill="currentColor" aria-label={label}>
      <path d={path} />
    </svg>
  );
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
  const [showAddSecret, setShowAddSecret] = useState(false);

  const [oauthProviders, setOauthProviders] = useState<OAuthProvider[]>([]);
  const [oauthStatus, setOauthStatus] = useState<
    Record<string, "checking" | "connected" | "disconnected">
  >({});
  const [oauthFlow, setOauthFlow] = useState<Record<string, OAuthFlow | null>>({});
  const [oauthFlowActive, setOauthFlowActive] = useState<Record<string, boolean>>({});

  const [sheetProvider, setSheetProvider] = useState<string | null>(null);
  const [configValues, setConfigValues] = useState<
    Record<string, { clientId: string; clientSecret: string; redirectUrl: string }>
  >({});
  const [configSaving, setConfigSaving] = useState<Record<string, boolean>>({});
  const [hasExistingSecret, setHasExistingSecret] = useState<Record<string, boolean>>({});

  const { toasts, showToast } = useToast();
  const pollAbortRef = useRef<Record<string, boolean>>({});

  const loadVaultEntries = useCallback(async () => {
    setVaultLoading(true);
    try {
      const { data } = await listVaultEntries({ throwOnError: true });
      setVaultEntries((data?.entries as VaultEntry[]) ?? []);
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
        setHasExistingSecret((prev) => ({ ...prev, [provider]: cfg.client_secret === "***" }));
      }
    } catch {
      // not admin or no config yet
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
      setShowAddSecret(false);
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
  const sheetProviderData = sheetProvider
    ? oauthProviders.find((p) => p.provider === sheetProvider)
    : undefined;

  function statusBadge(p: OAuthProvider) {
    const status = oauthStatus[p.provider];
    if (status === "connected")
      return (
        <Badge variant="success" size="sm">
          Connected
        </Badge>
      );
    if (!p.available)
      return (
        <Badge variant="warning" size="sm">
          Setup required
        </Badge>
      );
    if (status === "checking")
      return (
        <Badge variant="outline" size="sm">
          Checking
        </Badge>
      );
    return (
      <Badge variant="secondary" size="sm">
        Ready
      </Badge>
    );
  }

  const sp = sheetProviderData;
  const spConnected = sp ? oauthStatus[sp.provider] === "connected" : false;
  const spFlow = sp ? oauthFlow[sp.provider] : null;
  const providerSheet = sp ? (
    <DetailPanel>
      <DetailPanelHeader title={sp.provider} subtitle={statusBadge(sp)} />

      <div className="flex flex-wrap items-center gap-2">
        {sp.available && !spConnected && (
          <Button
            size="sm"
            loading={oauthFlowActive[sp.provider]}
            onClick={() => connectOAuth(sp.provider)}
          >
            Connect
          </Button>
        )}
        {spConnected && sp.available && (
          <Button
            size="sm"
            variant="destructive-outline"
            className="text-destructive hover:bg-destructive/10"
            onClick={() => disconnectOAuth(sp.provider)}
          >
            Disconnect
          </Button>
        )}
      </div>

      {spFlow && (
        <div className="rounded-lg border border-info/36 bg-info/8 p-3 text-xs">
          <p className="font-semibold">Authorize stella:</p>
          <a
            href={spFlow.verification_uri}
            target="_blank"
            rel="noreferrer"
            className="mt-1 block break-all font-mono text-xs text-primary underline"
          >
            {spFlow.verification_uri}
          </a>
          {spFlow.user_code && (
            <p className="mt-1 font-medium">
              Code: <span className="font-mono font-bold text-foreground">{spFlow.user_code}</span>
            </p>
          )}
          <p className="mt-1 text-[11px] text-muted-foreground">Waiting for authorization…</p>
        </div>
      )}

      {isAdmin && (
        <div className="space-y-3 border-t border-border pt-4">
          <FormSectionTitle>OAuth app</FormSectionTitle>
          <div className="space-y-1.5">
            <label className="text-xs font-medium text-muted-foreground">Client ID</label>
            <Input
              type="text"
              value={configValues[sp.provider]?.clientId ?? ""}
              onChange={(e) =>
                setConfigValues((prev) => ({
                  ...prev,
                  [sp.provider]: { ...prev[sp.provider], clientId: e.target.value },
                }))
              }
              placeholder="OAuth app client ID"
              autoComplete="off"
              nativeInput
            />
          </div>
          <div className="space-y-1.5">
            <label className="text-xs font-medium text-muted-foreground">Client Secret</label>
            <Input
              type="password"
              value={configValues[sp.provider]?.clientSecret ?? ""}
              onChange={(e) =>
                setConfigValues((prev) => ({
                  ...prev,
                  [sp.provider]: { ...prev[sp.provider], clientSecret: e.target.value },
                }))
              }
              placeholder={
                hasExistingSecret[sp.provider]
                  ? "Keep existing secret"
                  : sp.configured
                    ? "Configured"
                    : "OAuth app client secret"
              }
              autoComplete="new-password"
              nativeInput
            />
          </div>
          <div className="flex items-center justify-end gap-2 pt-1">
            {sp.configured && (
              <Button
                size="sm"
                variant="ghost"
                className="text-destructive hover:bg-destructive/10"
                onClick={() => deleteProviderConfig(sp.provider)}
              >
                Reset
              </Button>
            )}
            <Button
              size="sm"
              loading={configSaving[sp.provider]}
              onClick={() => saveProviderConfig(sp.provider)}
            >
              Save
            </Button>
          </div>
        </div>
      )}
    </DetailPanel>
  ) : null;

  return (
    <>
      <SettingsGridPage title={t("credentials.title")}>
        <SettingsSection
          icon={<Plug className="size-4" />}
          title={t("credentials.tab.oauth")}
          count={oauthProviders.length}
        >
          {oauthProviders.length === 0 ? (
            <p className="text-sm text-muted-foreground">No OAuth providers available.</p>
          ) : (
            <SettingsList>
              {oauthProviders.map((p) => {
                const status = oauthStatus[p.provider];
                const connected = status === "connected";
                const ready = p.available && !connected;
                const needsSetup = !p.available;
                const clientId = configValues[p.provider]?.clientId ?? "";
                const clientIdPreview =
                  clientId.length > 12 ? `${clientId.slice(0, 6)}…${clientId.slice(-4)}` : clientId;
                const subtitle = !p.configured
                  ? "App not configured"
                  : connected
                    ? clientIdPreview
                      ? `Connected · ${clientIdPreview}`
                      : "Connected"
                    : `${clientIdPreview || "Configured"} · not connected`;

                const menu: RowAction[] = [];
                if (isAdmin)
                  menu.push({ label: "Edit app", onClick: () => setSheetProvider(p.provider) });
                if (connected && p.available)
                  menu.push({
                    label: "Disconnect",
                    destructive: true,
                    onClick: () => void disconnectOAuth(p.provider),
                  });

                let primary: React.ReactNode = undefined;
                if (needsSetup && isAdmin) {
                  primary = (
                    <Button size="sm" onClick={() => setSheetProvider(p.provider)}>
                      Set up
                    </Button>
                  );
                } else if (ready) {
                  primary = (
                    <Button
                      size="sm"
                      loading={oauthFlowActive[p.provider]}
                      onClick={() => {
                        setSheetProvider(p.provider);
                        void connectOAuth(p.provider);
                      }}
                    >
                      Connect
                    </Button>
                  );
                }

                return (
                  <SettingsRow
                    key={p.provider}
                    icon={<ProviderIcon icon={p.icon} label={p.provider} />}
                    title={p.provider}
                    subtitle={subtitle}
                    status={statusBadge(p)}
                    primary={primary}
                    menu={menu}
                  />
                );
              })}
            </SettingsList>
          )}
        </SettingsSection>

        <EmailAccountsPanel showToast={showToast} />

        <SettingsSection
          icon={<KeyRound className="size-4" />}
          title={t("credentials.tab.vault")}
          count={filteredVaultEntries.length}
          action={
            <Button
              variant="ghost"
              size="xs"
              onClick={() => setShowAddSecret((v) => !v)}
              className="cursor-pointer"
            >
              <Plus className="size-3.5" />
              {t("credentials.addSecret")}
            </Button>
          }
        >
          {vaultLoading && <p className="text-sm text-muted-foreground">Loading…</p>}

          {filteredVaultEntries.length > 0 && (
            <div className="overflow-x-auto rounded-xl border border-border">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-border bg-muted/40 text-left">
                    <th className="px-4 py-2.5 font-mono text-xs font-semibold text-muted-foreground">
                      Name
                    </th>
                    <th className="px-4 py-2.5 font-mono text-xs font-semibold text-muted-foreground">
                      Created
                    </th>
                    <th className="px-4 py-2.5 font-mono text-xs font-semibold text-muted-foreground">
                      Updated
                    </th>
                    <th></th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border">
                  {filteredVaultEntries.map((entry) => (
                    <tr key={entry.name}>
                      <td className="px-4 py-2.5 font-mono font-medium text-foreground">
                        {entry.name}
                      </td>
                      <td className="px-4 py-2.5 text-xs text-muted-foreground">
                        {formatTime(entry.created_at)}
                      </td>
                      <td className="px-4 py-2.5 text-xs text-muted-foreground">
                        {formatTime(entry.updated_at)}
                      </td>
                      <td className="px-4 py-2.5 text-right">
                        <Button
                          size="xs"
                          variant="destructive-outline"
                          className="text-destructive hover:bg-destructive/10 cursor-pointer"
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

          {showAddSecret && (
            <div className="rounded-xl border border-border bg-card p-4 mt-3 space-y-4">
              <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                <div className="space-y-1.5">
                  <label className="text-xs font-medium text-muted-foreground">
                    {t("credentials.secretName")}
                  </label>
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
              <div className="flex justify-end gap-2 pt-1">
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => setShowAddSecret(false)}
                  className="cursor-pointer"
                >
                  {t("common.cancel")}
                </Button>
                <Button
                  size="sm"
                  loading={vaultSaving}
                  onClick={addVaultEntry}
                  className="cursor-pointer"
                >
                  {t("credentials.addSecret")}
                </Button>
              </div>
            </div>
          )}
        </SettingsSection>
      </SettingsGridPage>

      <SettingsDetailSheet open={!!sheetProvider} onClose={() => setSheetProvider(null)}>
        {providerSheet}
      </SettingsDetailSheet>

      <ToastContainer messages={toasts} />
    </>
  );
}
