import { useCallback, useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  deleteOAuthProviderConfig,
  deleteScopedVaultEntry as deleteVaultEntryRequest,
  disconnectOAuth as disconnectOAuthRequest,
  getOAuthConnected,
  getOAuthProviderConfig,
  getScopedVaultEntry,
  listAgents,
  listOAuthProviders,
  listScopedVaultEntries,
  pollOAuthFlow,
  setOAuthProviderConfig,
  setScopedVaultEntry,
  startOAuthFlow,
} from "@/lib/api-client/sdk.gen";
import { formatTime } from "@/lib/time";
import type { Agent, OAuthFlow, OAuthProvider, VaultEntry } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectItem,
  SelectPopup,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/messages";
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
import { KeyRound, Lock, Plug, Plus } from "lucide-react";
import { siGithub, siX } from "simple-icons";

const SIMPLE_ICON_PATHS: Record<string, string> = {
  github: siGithub.path,
  x: siX.path,
};

type VaultScope = VaultEntry["scope"];
type ScopeOwner = "me" | "global";
type ScopeRange = "all" | "specific";

// Reserved keys are written and rotated by stella itself. Surface them as
// read-only so users don't delete a managed credential.
const RESERVED_VAULT_KEYS = new Set([
  "STELLA_TOKEN",
  "GH_OAUTH",
  "LARK_CLI_OAUTH",
  "FEISHU_CLI_OAUTH",
]);
const RESERVED_VAULT_PREFIXES = ["OAUTH_", "MCP_TOKEN_"];

function isReservedVaultKey(name: string) {
  return (
    RESERVED_VAULT_KEYS.has(name) ||
    RESERVED_VAULT_PREFIXES.some((prefix) => name.startsWith(prefix))
  );
}

function isAgentVaultScope(scope: VaultScope) {
  return scope === "user_agent" || scope === "system_agent";
}

function toVaultScope(owner: ScopeOwner, range: ScopeRange): VaultScope {
  if (range === "specific") return owner === "global" ? "system_agent" : "user_agent";
  return owner === "global" ? "system" : "user";
}

// One hue per scope, drawn from the chart palette tokens. Reused by the list
// group rails, the row icon tint, and the precedence ladder so a scope reads as
// the same color everywhere.
const SCOPE_COLOR: Record<VaultScope, { dot: string; text: string; soft: string }> = {
  user: { dot: "bg-chart-2", text: "text-chart-2", soft: "bg-chart-2/12" },
  user_agent: {
    dot: "bg-chart-1",
    text: "text-chart-1",
    soft: "bg-chart-1/12",
  },
  system: { dot: "bg-chart-4", text: "text-chart-4", soft: "bg-chart-4/12" },
  system_agent: {
    dot: "bg-chart-5",
    text: "text-chart-5",
    soft: "bg-chart-5/12",
  },
};

// Render order for the grouped vault list.
const SCOPE_ORDER: VaultScope[] = ["user", "user_agent", "system", "system_agent"];

// Resolution precedence, highest first: a higher scope's value overrides a lower
// one at runtime. Drives the precedence ladder so the override chain is visible.
const SCOPE_PRIORITY: VaultScope[] = ["user_agent", "user", "system_agent", "system"];

const SCOPE_LABEL_KEY: Record<VaultScope, MessageKey> = {
  user: "credentials.scope.user.label",
  user_agent: "credentials.scope.userAgent.label",
  system: "credentials.scope.system.label",
  system_agent: "credentials.scope.systemAgent.label",
};

const SCOPE_DESC_KEY: Record<VaultScope, MessageKey> = {
  user: "credentials.scope.user.desc",
  user_agent: "credentials.scope.userAgent.desc",
  system: "credentials.scope.system.desc",
  system_agent: "credentials.scope.systemAgent.desc",
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
  const [agents, setAgents] = useState<Agent[]>([]);
  const [editingEntry, setEditingEntry] = useState<VaultEntry | null>(null);
  const [existingSecretValue, setExistingSecretValue] = useState("");
  // Add-form scope state, independent of the list (which shows every visible scope).
  const [formOwner, setFormOwner] = useState<ScopeOwner>("me");
  const [formRange, setFormRange] = useState<ScopeRange>("all");
  const [formAgentID, setFormAgentID] = useState("");
  const [newSecretName, setNewSecretName] = useState("");
  const [newSecretValue, setNewSecretValue] = useState("");
  const [addSheetOpen, setAddSheetOpen] = useState(false);

  const resetVaultForm = useCallback(() => {
    setNewSecretName("");
    setNewSecretValue("");
    setExistingSecretValue("");
    setEditingEntry(null);
    setFormOwner("me");
    setFormRange("all");
    setFormAgentID("");
  }, []);

  const openAddSheet = useCallback(() => {
    resetVaultForm();
    setAddSheetOpen(true);
  }, [resetVaultForm]);

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

  // Fetch every scope the caller can see and merge into one flat list. Agent
  // scopes are keyed per-agent, so they need one query per agent; the page loads
  // once, so the fan-out stays bounded. Empty/failed scopes contribute nothing.
  const loadVaultEntries = useCallback(
    async (agentList: Agent[]) => {
      setVaultLoading(true);
      try {
        const fetchScope = async (scope: VaultScope, agentID?: string) => {
          try {
            const { data } = await listScopedVaultEntries({
              query: { scope, agent_id: agentID },
              throwOnError: true,
            });
            return (data?.entries as VaultEntry[]) ?? [];
          } catch {
            return [];
          }
        };
        const jobs: Promise<VaultEntry[]>[] = [fetchScope("user")];
        if (isAdmin) jobs.push(fetchScope("system"));
        for (const agent of agentList) {
          jobs.push(fetchScope("user_agent", agent.id));
          if (isAdmin) jobs.push(fetchScope("system_agent", agent.id));
        }
        const results = await Promise.all(jobs);
        setVaultEntries(results.flat());
      } finally {
        setVaultLoading(false);
      }
    },
    [isAdmin],
  );

  // Refetch a single scope (plus agent, for agent-keyed scopes) and splice it
  // back into the flat list. A mutation only changes one slice, so this avoids
  // the full 2N+2 fan-out of loadVaultEntries on every add/delete.
  const reloadScope = useCallback(async (scope: VaultScope, agentID?: string) => {
    let fetched: VaultEntry[] = [];
    try {
      const { data } = await listScopedVaultEntries({
        query: { scope, agent_id: agentID },
        throwOnError: true,
      });
      fetched = (data?.entries as VaultEntry[]) ?? [];
    } catch {
      fetched = [];
    }
    setVaultEntries((prev) => [
      ...prev.filter((e) => !(e.scope === scope && (agentID ? e.agent_id === agentID : true))),
      ...fetched,
    ]);
  }, []);

  const loadAgents = useCallback(async () => {
    try {
      const { data } = await listAgents({
        query: { include_all: true },
        throwOnError: true,
      });
      const list = (data?.agents as Agent[]) ?? [];
      setAgents(list);
      return list;
    } catch {
      setAgents([]);
      return [];
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
        setHasExistingSecret((prev) => ({
          ...prev,
          [provider]: cfg.client_secret === "***",
        }));
      }
    } catch {
      // not admin or no config yet
    }
  }, []);

  const checkOAuthConnected = useCallback(async (provider: string) => {
    setOauthStatus((prev) => ({ ...prev, [provider]: "checking" }));
    try {
      const { data } = await getOAuthConnected({
        path: { provider },
        throwOnError: true,
      });
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
      const agentList = await loadAgents();
      await loadVaultEntries(agentList);
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
  }, [
    loadVaultEntries,
    loadAgents,
    loadOAuthProviders,
    checkOAuthConnected,
    loadProviderConfig,
    isAdmin,
  ]);

  const openEditSheet = useCallback(async (entry: VaultEntry) => {
    setEditingEntry(entry);
    setNewSecretName(entry.name);
    setNewSecretValue("");
    setExistingSecretValue("");
    setFormOwner(entry.scope === "system" || entry.scope === "system_agent" ? "global" : "me");
    setFormRange(isAgentVaultScope(entry.scope) ? "specific" : "all");
    setFormAgentID(entry.agent_id ?? "");
    setAddSheetOpen(true);
    try {
      const { data } = await getScopedVaultEntry({
        path: { name: entry.name },
        query: {
          scope: entry.scope,
          agent_id: isAgentVaultScope(entry.scope) ? (entry.agent_id ?? undefined) : undefined,
        },
        throwOnError: true,
      });
      setExistingSecretValue(data?.value ?? "");
    } catch {
      setExistingSecretValue("");
    }
  }, []);

  const addVaultEntry = useCallback(async () => {
    if (!newSecretName) {
      showToast(t("credentials.secretNameRequired"), "error");
      return;
    }
    if (isReservedVaultKey(newSecretName)) {
      showToast(t("credentials.secretNameReserved"), "error");
      return;
    }
    const value = newSecretValue || existingSecretValue;
    if (!value) {
      showToast(t("credentials.secretValueRequired"), "error");
      return;
    }
    const scope = toVaultScope(formOwner, formRange);
    const agentScoped = isAgentVaultScope(scope);
    if (agentScoped && !formAgentID) {
      showToast(t("credentials.scope.agentMissing"), "error");
      return;
    }
    setVaultSaving(true);
    try {
      await setScopedVaultEntry({
        path: { name: newSecretName },
        body: {
          value,
          scope,
          agent_id: agentScoped ? formAgentID : undefined,
        },
        throwOnError: true,
      });
      showToast(t("credentials.secretSaved"));
      setAddSheetOpen(false);
      resetVaultForm();
      await reloadScope(scope, agentScoped ? formAgentID : undefined);
      if (editingEntry && (editingEntry.scope !== scope || editingEntry.agent_id !== formAgentID)) {
        await reloadScope(
          editingEntry.scope,
          isAgentVaultScope(editingEntry.scope) ? (editingEntry.agent_id ?? undefined) : undefined,
        );
      }
    } catch (e) {
      showToast(e instanceof Error ? e.message : t("credentials.secretSaveFailed"), "error");
    } finally {
      setVaultSaving(false);
    }
  }, [
    newSecretName,
    newSecretValue,
    existingSecretValue,
    formOwner,
    formRange,
    formAgentID,
    editingEntry,
    showToast,
    reloadScope,
    resetVaultForm,
    t,
  ]);

  const deleteVaultEntry = useCallback(
    async (entry: VaultEntry) => {
      if (!window.confirm(t("credentials.deleteSecretConfirm", { name: entry.name }))) return;
      try {
        await deleteVaultEntryRequest({
          path: { name: entry.name },
          query: {
            scope: entry.scope,
            agent_id: isAgentVaultScope(entry.scope) ? (entry.agent_id ?? undefined) : undefined,
          },
          throwOnError: true,
        });
        showToast(t("credentials.secretDeleted"));
        await reloadScope(
          entry.scope,
          isAgentVaultScope(entry.scope) ? (entry.agent_id ?? undefined) : undefined,
        );
      } catch (e) {
        showToast(e instanceof Error ? e.message : t("credentials.secretDeleteFailed"), "error");
      }
    },
    [showToast, reloadScope, t],
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
          if (status?.state === "authorized")
            showToast(t("credentials.oauth.connectedSuccess", { provider }));
          else if (status)
            showToast(
              t("credentials.oauth.authorizationState", {
                provider,
                state: status.state,
              }),
              "error",
            );
          break;
        }
      }
    },
    [showToast, t],
  );

  const connectOAuth = useCallback(
    async (provider: string) => {
      setOauthFlowActive((prev) => ({ ...prev, [provider]: true }));
      setOauthFlow((prev) => ({ ...prev, [provider]: null }));
      try {
        const { data } = await startOAuthFlow({
          path: { provider },
          throwOnError: true,
        });
        const flow = data as OAuthFlow;
        setOauthFlow((prev) => ({ ...prev, [provider]: flow }));
        await pollUntilDone(provider, flow.flow_id);
      } catch (e) {
        showToast(e instanceof Error ? e.message : t("credentials.oauth.error"), "error");
      } finally {
        setOauthFlowActive((prev) => ({ ...prev, [provider]: false }));
        setOauthFlow((prev) => ({ ...prev, [provider]: null }));
        await checkOAuthConnected(provider);
      }
    },
    [pollUntilDone, showToast, checkOAuthConnected, t],
  );

  const disconnectOAuth = useCallback(
    async (provider: string) => {
      if (!window.confirm(t("credentials.oauth.disconnectConfirm", { provider }))) return;
      try {
        await disconnectOAuthRequest({
          path: { provider },
          throwOnError: true,
        });
        showToast(t("credentials.oauth.disconnected", { provider }));
        await checkOAuthConnected(provider);
      } catch (e) {
        showToast(
          e instanceof Error ? e.message : t("credentials.oauth.disconnectFailed"),
          "error",
        );
      }
    },
    [showToast, checkOAuthConnected, t],
  );

  const saveProviderConfig = useCallback(
    async (provider: string) => {
      const vals = configValues[provider];
      if (!vals?.clientId) {
        showToast(t("credentials.oauth.clientIdRequired"), "error");
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
        showToast(t("credentials.oauth.configSaved", { provider }));
        await loadOAuthProviders();
        await loadProviderConfig(provider);
        await checkOAuthConnected(provider);
      } catch (e) {
        showToast(
          e instanceof Error ? e.message : t("credentials.oauth.configSaveFailed"),
          "error",
        );
      } finally {
        setConfigSaving((prev) => ({ ...prev, [provider]: false }));
      }
    },
    [configValues, showToast, loadOAuthProviders, loadProviderConfig, checkOAuthConnected, t],
  );

  const deleteProviderConfig = useCallback(
    async (provider: string) => {
      if (!window.confirm(t("credentials.oauth.resetConfirm", { provider }))) return;
      try {
        await deleteOAuthProviderConfig({
          path: { id: provider },
          throwOnError: true,
        });
        showToast(t("credentials.oauth.configReset", { provider }));
        await loadOAuthProviders();
        await loadProviderConfig(provider);
        await checkOAuthConnected(provider);
      } catch (e) {
        showToast(
          e instanceof Error ? e.message : t("credentials.oauth.configResetFailed"),
          "error",
        );
      }
    },
    [showToast, loadOAuthProviders, loadProviderConfig, checkOAuthConnected, t],
  );

  const filteredVaultEntries = vaultEntries.filter((entry) => entry.name !== "EMAIL_CONFIG");
  const agentName = (id?: string | null) =>
    (id && agents.find((a) => a.id === id)?.name) || id || "";
  const vaultGroups = SCOPE_ORDER.map((scope) => ({
    scope,
    entries: filteredVaultEntries.filter((e) => e.scope === scope),
  })).filter((g) => g.entries.length > 0);
  const formScope = toVaultScope(formOwner, formRange);
  const editingVault = !!editingEntry;

  const selectScope = (scope: VaultScope) => {
    if (editingVault) return;
    setFormOwner(scope === "system" || scope === "system_agent" ? "global" : "me");
    setFormRange(scope === "user_agent" || scope === "system_agent" ? "specific" : "all");
  };

  const vaultAddPanel = (
    <DetailPanel>
      <DetailPanelHeader
        title={
          editingVault
            ? t("credentials.editTitle", { name: editingEntry?.name ?? "" })
            : t("credentials.addTitle")
        }
      />

      {/* The precedence ladder IS the scope picker: each row is selectable and
          its position shows where the secret lands in the runtime override order.
          One control replaces a separate picker plus a static legend. */}
      <div className="space-y-3">
        <p className="text-xs font-medium text-muted-foreground">
          {t("credentials.scope.priorityTitle")}
        </p>
        <ul className="space-y-1">
          {SCOPE_PRIORITY.filter(
            (scope) => isAdmin || (scope !== "system" && scope !== "system_agent"),
          ).map((scope) => {
            const active = scope === formScope;
            return (
              <li key={scope}>
                <button
                  type="button"
                  disabled={editingVault}
                  onClick={() => selectScope(scope)}
                  className={`flex w-full cursor-pointer items-center gap-2.5 rounded-md px-3 py-2 text-left text-sm transition-colors disabled:cursor-not-allowed disabled:opacity-64 ${
                    active ? SCOPE_COLOR[scope].soft : "hover:bg-muted/60"
                  }`}
                >
                  <span className={`size-2.5 shrink-0 rounded-full ${SCOPE_COLOR[scope].dot}`} />
                  <span
                    className={
                      active ? `font-semibold ${SCOPE_COLOR[scope].text}` : "text-foreground"
                    }
                  >
                    {t(SCOPE_LABEL_KEY[scope])}
                  </span>
                  {active && (
                    <span className="ml-auto text-xs font-medium text-muted-foreground">
                      {t("credentials.scope.current")}
                    </span>
                  )}
                </button>
              </li>
            );
          })}
        </ul>

        <p className="px-1 text-xs text-muted-foreground">{t(SCOPE_DESC_KEY[formScope])}</p>

        {formRange === "specific" && (
          <Select
            value={formAgentID || null}
            disabled={editingVault}
            onValueChange={(value) => setFormAgentID((value as string | null) ?? "")}
          >
            <SelectTrigger>
              <SelectValue placeholder={t("credentials.scope.selectAgent")}>
                {(value) =>
                  value ? agents.find((agent) => agent.id === value)?.name || value : null
                }
              </SelectValue>
            </SelectTrigger>
            <SelectPopup>
              {agents.map((agent) => (
                <SelectItem key={agent.id} value={agent.id}>
                  {agent.name || agent.id}
                </SelectItem>
              ))}
            </SelectPopup>
          </Select>
        )}
      </div>

      <div className="space-y-3 border-t border-border pt-4">
        <div className="space-y-1.5">
          <label className="text-xs font-medium text-muted-foreground">
            {t("credentials.secretName")}
          </label>
          <Input
            type="text"
            value={newSecretName}
            onChange={(e) => setNewSecretName(e.target.value)}
            placeholder={t("credentials.secretNamePlaceholder")}
            autoComplete="off"
            disabled={editingVault}
            nativeInput
          />
        </div>
        <div className="space-y-1.5">
          <label className="text-xs font-medium text-muted-foreground">
            {t("credentials.value")}
          </label>
          <Input
            type="password"
            value={newSecretValue}
            onChange={(e) => setNewSecretValue(e.target.value)}
            placeholder={
              editingVault
                ? t("credentials.secretValueKeepExisting")
                : t("credentials.secretValuePlaceholder")
            }
            autoComplete="new-password"
            nativeInput
          />
        </div>
        <div className="flex items-center justify-end gap-2 pt-1">
          <Button
            size="sm"
            variant="ghost"
            onClick={() => {
              setAddSheetOpen(false);
              resetVaultForm();
            }}
          >
            {t("common.cancel")}
          </Button>
          <Button size="sm" loading={vaultSaving} onClick={addVaultEntry}>
            {editingVault ? t("common.save") : t("credentials.addSecret")}
          </Button>
        </div>
      </div>
    </DetailPanel>
  );

  const sheetProviderData = sheetProvider
    ? oauthProviders.find((p) => p.provider === sheetProvider)
    : undefined;

  function statusBadge(p: OAuthProvider) {
    const status = oauthStatus[p.provider];
    if (status === "connected")
      return (
        <Badge variant="success" size="sm">
          {t("credentials.oauth.status.connected")}
        </Badge>
      );
    if (!p.available)
      return (
        <Badge variant="warning" size="sm">
          {t("credentials.oauth.status.setupRequired")}
        </Badge>
      );
    if (status === "checking")
      return (
        <Badge variant="outline" size="sm">
          {t("credentials.oauth.status.checking")}
        </Badge>
      );
    return (
      <Badge variant="secondary" size="sm">
        {t("credentials.oauth.status.ready")}
      </Badge>
    );
  }

  const sp = sheetProviderData;
  const spConnected = sp ? oauthStatus[sp.provider] === "connected" : false;
  const spFlow = sp ? oauthFlow[sp.provider] : null;
  const providerSheet = sp ? (
    <DetailPanel>
      <DetailPanelHeader title={sp.provider} subtitle={statusBadge(sp)} />

      {sp.available && !spConnected && (sp.required_by?.length ?? 0) > 0 && (
        <div className="rounded-lg border border-info/36 bg-info/8 p-3 text-xs">
          <p className="font-medium text-foreground">
            {t("credentials.oauth.connectToEnable", {
              tools: sp.required_by?.join(", ") ?? "",
            })}
          </p>
          <p className="mt-1 text-muted-foreground">
            {t("credentials.oauth.unauthenticatedWarning")}
          </p>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-2">
        {sp.available && !spConnected && (
          <Button
            size="sm"
            loading={oauthFlowActive[sp.provider]}
            onClick={() => connectOAuth(sp.provider)}
          >
            {t("credentials.oauth.connect")}
          </Button>
        )}
        {spConnected && sp.available && (
          <Button
            size="sm"
            variant="destructive-outline"
            className="text-destructive hover:bg-destructive/10"
            onClick={() => disconnectOAuth(sp.provider)}
          >
            {t("credentials.oauth.disconnect")}
          </Button>
        )}
      </div>

      {spFlow && (
        <div className="rounded-lg border border-info/36 bg-info/8 p-3 text-xs">
          <p className="font-semibold">{t("credentials.oauth.authorizeStella")}</p>
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
              {t("credentials.oauth.code")}{" "}
              <span className="font-mono font-semibold text-foreground">{spFlow.user_code}</span>
            </p>
          )}
          <p className="mt-1 text-xs text-muted-foreground">
            {t("credentials.oauth.waitingAuthorization")}
          </p>
        </div>
      )}

      {isAdmin && (
        <div className="space-y-3 border-t border-border pt-4">
          <FormSectionTitle>{t("credentials.oauth.app")}</FormSectionTitle>
          <div className="space-y-1.5">
            <label className="text-xs font-medium text-muted-foreground">
              {t("credentials.oauth.clientId")}
            </label>
            <Input
              type="text"
              value={configValues[sp.provider]?.clientId ?? ""}
              onChange={(e) =>
                setConfigValues((prev) => ({
                  ...prev,
                  [sp.provider]: {
                    ...prev[sp.provider],
                    clientId: e.target.value,
                  },
                }))
              }
              placeholder={t("credentials.oauth.clientIdPlaceholder")}
              autoComplete="off"
              nativeInput
            />
          </div>
          <div className="space-y-1.5">
            <label className="text-xs font-medium text-muted-foreground">
              {t("credentials.oauth.clientSecret")}
            </label>
            <Input
              type="password"
              value={configValues[sp.provider]?.clientSecret ?? ""}
              onChange={(e) =>
                setConfigValues((prev) => ({
                  ...prev,
                  [sp.provider]: {
                    ...prev[sp.provider],
                    clientSecret: e.target.value,
                  },
                }))
              }
              placeholder={
                hasExistingSecret[sp.provider]
                  ? t("credentials.oauth.keepExistingSecret")
                  : sp.configured
                    ? t("credentials.oauth.configured")
                    : t("credentials.oauth.clientSecretPlaceholder")
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
                {t("credentials.oauth.reset")}
              </Button>
            )}
            <Button
              size="sm"
              loading={configSaving[sp.provider]}
              onClick={() => saveProviderConfig(sp.provider)}
            >
              {t("common.save")}
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
            <p className="text-sm text-muted-foreground">{t("credentials.oauth.noProviders")}</p>
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
                const requiredBy = p.required_by ?? [];
                const subtitle = !p.configured
                  ? t("credentials.oauth.appNotConfigured")
                  : connected
                    ? clientIdPreview
                      ? t("credentials.oauth.connectedWithClient", {
                          client: clientIdPreview,
                        })
                      : t("credentials.oauth.status.connected")
                    : requiredBy.length > 0
                      ? t("credentials.oauth.connectToEnable", {
                          tools: requiredBy.join(", "),
                        })
                      : t("credentials.oauth.notConnectedWithClient", {
                          client: clientIdPreview || t("credentials.oauth.configured"),
                        });

                const menu: RowAction[] = [];
                if (isAdmin)
                  menu.push({
                    label: t("credentials.oauth.editApp"),
                    onClick: () => setSheetProvider(p.provider),
                  });
                if (connected && p.available)
                  menu.push({
                    label: t("credentials.oauth.disconnect"),
                    destructive: true,
                    onClick: () => void disconnectOAuth(p.provider),
                  });

                let primary: React.ReactNode = undefined;
                if (needsSetup && isAdmin) {
                  primary = (
                    <Button size="sm" variant="ghost" onClick={() => setSheetProvider(p.provider)}>
                      {t("credentials.oauth.setUp")}
                    </Button>
                  );
                } else if (ready) {
                  primary = (
                    <Button
                      size="sm"
                      variant="ghost"
                      loading={oauthFlowActive[p.provider]}
                      onClick={() => {
                        setSheetProvider(p.provider);
                        void connectOAuth(p.provider);
                      }}
                    >
                      {t("credentials.oauth.connect")}
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
                    onClick={() => setSheetProvider(p.provider)}
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
            <Button variant="ghost" size="xs" onClick={openAddSheet} className="cursor-pointer">
              <Plus className="size-3.5" />
              {t("credentials.addSecret")}
            </Button>
          }
        >
          {vaultLoading && <p className="text-sm text-muted-foreground">{t("common.loading")}</p>}

          <div className="space-y-5">
            {vaultGroups.map((group) => {
              const color = SCOPE_COLOR[group.scope];
              return (
                <div key={group.scope} className="space-y-2">
                  <div className="flex items-center gap-2 px-1">
                    <span className={`size-2 shrink-0 rounded-full ${color.dot}`} />
                    <span className="text-xs font-semibold text-foreground">
                      {t(SCOPE_LABEL_KEY[group.scope])}
                    </span>
                    <span className="text-xs text-muted-foreground">{group.entries.length}</span>
                  </div>
                  <SettingsList>
                    {group.entries.map((entry) => {
                      const reserved = isReservedVaultKey(entry.name);
                      return (
                        <SettingsRow
                          key={`${entry.scope}:${entry.agent_id ?? ""}:${entry.name}`}
                          icon={
                            <span className={color.text}>
                              {reserved ? (
                                <Lock className="size-4" />
                              ) : (
                                <KeyRound className="size-4" />
                              )}
                            </span>
                          }
                          title={<span className="font-mono">{entry.name}</span>}
                          chip={
                            entry.agent_id ? (
                              <Badge variant="outline" size="sm">
                                {agentName(entry.agent_id)}
                              </Badge>
                            ) : reserved ? (
                              <Badge variant="secondary" size="sm">
                                {t("credentials.scope.reserved")}
                              </Badge>
                            ) : undefined
                          }
                          subtitle={t("credentials.updatedCreated", {
                            updated: formatTime(entry.updated_at),
                            created: formatTime(entry.created_at),
                          })}
                          menu={
                            reserved
                              ? []
                              : [
                                  {
                                    label: t("common.edit"),
                                    onClick: () => void openEditSheet(entry),
                                  },
                                  {
                                    label: t("common.delete"),
                                    destructive: true,
                                    onClick: () => void deleteVaultEntry(entry),
                                  },
                                ]
                          }
                          onClick={reserved ? undefined : () => void openEditSheet(entry)}
                        />
                      );
                    })}
                  </SettingsList>
                </div>
              );
            })}
          </div>

          {vaultGroups.length === 0 && !vaultLoading && (
            <p className="py-4 text-center text-sm text-muted-foreground">
              {t("credentials.noSecrets")}
            </p>
          )}
        </SettingsSection>
      </SettingsGridPage>

      <SettingsDetailSheet open={!!sheetProvider} onClose={() => setSheetProvider(null)}>
        {providerSheet}
      </SettingsDetailSheet>

      <SettingsDetailSheet
        open={addSheetOpen}
        onClose={() => {
          setAddSheetOpen(false);
          resetVaultForm();
        }}
      >
        {vaultAddPanel}
      </SettingsDetailSheet>

      <ToastContainer messages={toasts} />
    </>
  );
}
