import { useCallback, useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  deleteOAuthProviderConfig,
  deleteScopedVaultEntry as deleteVaultEntryRequest,
  disconnectOAuth as disconnectOAuthRequest,
  getScopedVaultEntry,
  listAgents,
  listScopedVaultEntries,
  pollOAuthFlow,
  setOAuthProviderConfig,
  setScopedVaultEntry,
  startOAuthFlow,
} from "@/lib/api-client/sdk.gen";
import {
  oauthProviderConfigOptions,
  oauthProvidersQueryKey,
  oauthProvidersQueryOptions,
} from "@/lib/queries/oauth";
import { formatTime } from "@/lib/time";
import type { Agent, OAuthFlow, OAuthProvider, VaultEntry } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Fieldset, FieldsetLegend } from "@/components/ui/fieldset";
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
import { ScopeEditor } from "@/features/credentials/ScopeEditor";
import { ConfirmDialog } from "@/features/settings/ConfirmDialog";
import {
  SettingsDetailSheet,
  SettingsGridPage,
  SettingsList,
  SettingsRow,
  SettingsSection,
} from "@/features/settings/SettingsCardGrid";
import type { RowAction } from "@/features/settings/SettingsCardGrid";
import { DetailPanel, DetailPanelHeader } from "@/features/settings/SettingsDetailPanel";
import { KeyRound, Lock, Plug, Plus } from "lucide-react";
import { siGithub, siX } from "simple-icons";
import feishuIcon from "@/assets/auth/feishu.svg";

// Brand marks carried by simple-icons, resolved by slug. Adding a simple-icons
// brand is one named import + one entry here; unknown slugs fall through to the
// generic glyph.
const SIMPLE_ICONS: Record<string, { path: string }> = {
  github: siGithub,
  x: siX,
};

// Brands simple-icons does not carry, rendered from bundled assets. Feishu and
// its international brand Lark share one mark.
const ASSET_ICONS: Record<string, string> = {
  feishu: feishuIcon,
  lark: feishuIcon,
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

// ProviderIcon resolves a brand mark from the provider's icon string (set in
// the provider YAML): `simpleicons:<slug>` for marks simple-icons carries,
// `asset:<name>` for bundled brand SVGs it does not (Feishu/Lark). Falls back to
// a bundled asset keyed by provider id, then to a generic plug glyph.
function ProviderIcon({
  provider,
  icon,
  label,
}: {
  provider: string;
  icon?: string;
  label: string;
}) {
  const [family, name] = (icon ?? "").split(":");
  if (family === "simpleicons") {
    const brand = SIMPLE_ICONS[name?.toLowerCase()];
    if (brand) {
      return (
        <svg viewBox="0 0 24 24" className="size-4" fill="currentColor" aria-label={label}>
          <path d={brand.path} />
        </svg>
      );
    }
  }
  const asset =
    (family === "asset" ? ASSET_ICONS[name?.toLowerCase()] : undefined) ??
    ASSET_ICONS[provider.toLowerCase()];
  if (asset) return <img src={asset} alt="" className="size-4" />;
  return <Plug className="size-4" />;
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

  const queryClient = useQueryClient();
  // The provider list (with per-user connection/reconnect state) is server
  // cache: one query drives the whole OAuth section. Connect/disconnect/save
  // invalidate it instead of hand-refetching.
  const { data: oauthProviders = [], isLoading: oauthLoading } = useQuery(
    oauthProvidersQueryOptions,
  );
  // Flow progress and the last failure reason are ephemeral connect-flow UI,
  // not server cache, so they stay local.
  const [oauthFlow, setOauthFlow] = useState<Record<string, OAuthFlow | null>>({});
  const [oauthFlowActive, setOauthFlowActive] = useState<Record<string, boolean>>({});
  const [oauthFlowError, setOauthFlowError] = useState<Record<string, string | null>>({});

  const [sheetProvider, setSheetProvider] = useState<string | null>(null);
  const [configValues, setConfigValues] = useState<
    Record<string, { clientId: string; clientSecret: string; redirectUrl: string }>
  >({});
  const [configSaving, setConfigSaving] = useState<Record<string, boolean>>({});
  const [hasExistingSecret, setHasExistingSecret] = useState<Record<string, boolean>>({});
  // Scope override editing is a separate model from the credential inputs: the
  // ScopeEditor mutates the working list frequently, and we keep the saved and
  // default baselines to drive the diff bar and reset without a second fetch.
  const [scopeDraft, setScopeDraft] = useState<Record<string, string[]>>({});
  const [scopeMeta, setScopeMeta] = useState<
    Record<string, { saved: string[]; defaults: string[] }>
  >({});

  // One pending confirmation at a time; ConfirmDialog is controlled by this.
  const [confirm, setConfirm] = useState<{
    title: string;
    message: string;
    confirmLabel?: string;
    onConfirm: () => void;
  } | null>(null);

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

  const invalidateProviders = useCallback(
    () => queryClient.invalidateQueries({ queryKey: oauthProvidersQueryKey }),
    [queryClient],
  );

  // Admin credentials + scope override for the open sheet only. The form edits
  // live in local state (configValues/scopeDraft), seeded from this query when
  // it loads; the query stays the source of truth for the saved baseline.
  const { data: providerConfig } = useQuery(
    oauthProviderConfigOptions(sheetProvider, isAdmin && !!sheetProvider),
  );

  useEffect(() => {
    if (!sheetProvider || !providerConfig) return;
    const provider = sheetProvider;
    setConfigValues((prev) => ({
      ...prev,
      [provider]: {
        clientId: providerConfig.client_id,
        clientSecret: "",
        redirectUrl: providerConfig.redirect_url ?? "",
      },
    }));
    setHasExistingSecret((prev) => ({
      ...prev,
      [provider]: providerConfig.client_secret === "***",
    }));
    const saved = providerConfig.scopes ?? [];
    const defaults = providerConfig.default_scopes ?? [];
    setScopeDraft((prev) => ({ ...prev, [provider]: saved }));
    setScopeMeta((prev) => ({ ...prev, [provider]: { saved, defaults } }));
  }, [sheetProvider, providerConfig]);

  useEffect(() => {
    const init = async () => {
      const agentList = await loadAgents();
      await loadVaultEntries(agentList);
    };
    void init();
    const pollAbort = pollAbortRef.current;
    return () => {
      for (const key of Object.keys(pollAbort)) {
        pollAbort[key] = true;
      }
    };
  }, [loadVaultEntries, loadAgents]);

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

  const invalidateProviderConfig = useCallback(
    (provider: string) =>
      queryClient.invalidateQueries({ queryKey: ["oauth-provider-config", provider] }),
    [queryClient],
  );

  const pollUntilDone = useCallback(
    async (provider: string, flowID: string) => {
      pollAbortRef.current[provider] = false;
      while (!pollAbortRef.current[provider]) {
        await new Promise((r) => setTimeout(r, 3000));
        if (pollAbortRef.current[provider]) break;
        let status: { state: string; error?: string } | null = null;
        try {
          const { data } = await pollOAuthFlow({
            path: { provider, flowId: flowID },
            throwOnError: true,
          });
          status = data as { state: string; error?: string };
        } catch {
          break;
        }
        if (!status || status.state !== "pending") {
          if (status?.state === "authorized")
            showToast(t("credentials.oauth.connectedSuccess", { provider }));
          else if (status) {
            // Surface the server-provided failure reason inline (and as a toast).
            setOauthFlowError((prev) => ({
              ...prev,
              [provider]:
                status.error ||
                t("credentials.oauth.authorizationState", { provider, state: status.state }),
            }));
            showToast(
              status.error ||
                t("credentials.oauth.authorizationState", {
                  provider,
                  state: status.state,
                }),
              "error",
            );
          }
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
      setOauthFlowError((prev) => ({ ...prev, [provider]: null }));
      try {
        const { data } = await startOAuthFlow({
          path: { provider },
          throwOnError: true,
        });
        const flow = data as OAuthFlow;
        setOauthFlow((prev) => ({ ...prev, [provider]: flow }));
        await pollUntilDone(provider, flow.flow_id);
      } catch (e) {
        const msg = e instanceof Error ? e.message : t("credentials.oauth.error");
        setOauthFlowError((prev) => ({ ...prev, [provider]: msg }));
        showToast(msg, "error");
      } finally {
        setOauthFlowActive((prev) => ({ ...prev, [provider]: false }));
        setOauthFlow((prev) => ({ ...prev, [provider]: null }));
        await invalidateProviders();
      }
    },
    [pollUntilDone, showToast, invalidateProviders, t],
  );

  const disconnectOAuth = useCallback(
    async (provider: string) => {
      try {
        await disconnectOAuthRequest({
          path: { provider },
          throwOnError: true,
        });
        showToast(t("credentials.oauth.disconnected", { provider }));
        await invalidateProviders();
      } catch (e) {
        showToast(
          e instanceof Error ? e.message : t("credentials.oauth.disconnectFailed"),
          "error",
        );
      }
    },
    [showToast, invalidateProviders, t],
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
            scopes: scopeDraft[provider] ?? [],
          },
          throwOnError: true,
        });
        showToast(t("credentials.oauth.configSaved", { provider }));
        await Promise.all([invalidateProviders(), invalidateProviderConfig(provider)]);
      } catch (e) {
        showToast(
          e instanceof Error ? e.message : t("credentials.oauth.configSaveFailed"),
          "error",
        );
      } finally {
        setConfigSaving((prev) => ({ ...prev, [provider]: false }));
      }
    },
    [configValues, scopeDraft, showToast, invalidateProviders, invalidateProviderConfig, t],
  );

  const deleteProviderConfig = useCallback(
    async (provider: string) => {
      try {
        await deleteOAuthProviderConfig({
          path: { id: provider },
          throwOnError: true,
        });
        showToast(t("credentials.oauth.configReset", { provider }));
        await Promise.all([invalidateProviders(), invalidateProviderConfig(provider)]);
      } catch (e) {
        showToast(
          e instanceof Error ? e.message : t("credentials.oauth.configResetFailed"),
          "error",
        );
      }
    },
    [showToast, invalidateProviders, invalidateProviderConfig, t],
  );

  // Destructive actions route through one controlled ConfirmDialog rather than
  // the native browser prompt, so the modal matches the rest of the UI.
  const confirmDeleteVaultEntry = useCallback(
    (entry: VaultEntry) =>
      setConfirm({
        title: t("credentials.deleteSecretTitle"),
        message: t("credentials.deleteSecretConfirm", { name: entry.name }),
        onConfirm: () => void deleteVaultEntry(entry),
      }),
    [deleteVaultEntry, t],
  );
  const confirmDisconnectOAuth = useCallback(
    (provider: string) =>
      setConfirm({
        title: t("credentials.oauth.disconnectTitle"),
        message: t("credentials.oauth.disconnectConfirm", { provider }),
        confirmLabel: t("credentials.oauth.disconnect"),
        onConfirm: () => void disconnectOAuth(provider),
      }),
    [disconnectOAuth, t],
  );
  const confirmResetProviderConfig = useCallback(
    (provider: string) =>
      setConfirm({
        title: t("credentials.oauth.resetTitle"),
        message: t("credentials.oauth.resetConfirm", { provider }),
        confirmLabel: t("credentials.oauth.reset"),
        onConfirm: () => void deleteProviderConfig(provider),
      }),
    [deleteProviderConfig, t],
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
    if (p.connected) {
      // A connected-but-stale credential needs the user to re-authorize.
      if (p.needs_reconnect)
        return (
          <Badge variant="warning" size="sm">
            {t("credentials.oauth.status.reconnect")}
          </Badge>
        );
      return (
        <Badge variant="success" size="sm">
          {t("credentials.oauth.status.connected")}
        </Badge>
      );
    }
    if (!p.available)
      return (
        <Badge variant="warning" size="sm">
          {t("credentials.oauth.status.setupRequired")}
        </Badge>
      );
    if (oauthLoading)
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

  // Scopes the connected token lacks vs. what the connect flow now requests.
  // granted_scopes absent means "unknown" (pre-capture token), so we only claim
  // a concrete gap when the grant is known.
  function missingScopes(p: OAuthProvider): string[] {
    if (!p.granted_scopes) return [];
    const granted = new Set(p.granted_scopes);
    return (p.requested_scopes ?? []).filter((s) => !granted.has(s));
  }

  const sp = sheetProviderData;
  const spConnected = sp?.connected ?? false;
  const spFlow = sp ? oauthFlow[sp.provider] : null;
  const spFlowError = sp ? oauthFlowError[sp.provider] : null;
  const spMeta = sp ? scopeMeta[sp.provider] : undefined;
  const spMissingScopes = sp ? missingScopes(sp) : [];
  const providerSheet = sp ? (
    <DetailPanel
      onSave={isAdmin ? () => saveProviderConfig(sp.provider) : undefined}
      isSaving={isAdmin ? configSaving[sp.provider] : undefined}
      saveLabel={t("common.save")}
      isSavingLabel={t("common.saving")}
      onDelete={
        isAdmin && sp.configured ? () => confirmResetProviderConfig(sp.provider) : undefined
      }
      deleteLabel={t("credentials.oauth.reset")}
    >
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

      {spConnected && sp.needs_reconnect && (
        <div className="space-y-2 rounded-lg border border-warning/36 bg-warning/8 p-3 text-xs">
          <p className="font-medium text-foreground">
            {sp.reconnect_reason === "missing_scopes" && spMissingScopes.length > 0
              ? t("credentials.oauth.reconnectMissingScopes", {
                  count: spMissingScopes.length,
                })
              : sp.reconnect_reason === "credentials_rotated"
                ? t("credentials.oauth.reconnectRotated")
                : t("credentials.oauth.reconnectGeneric")}
          </p>
          {sp.reconnect_reason === "missing_scopes" && spMissingScopes.length > 0 && (
            <ul className="flex flex-wrap gap-1">
              {spMissingScopes.map((s) => (
                <li key={s}>
                  <Badge variant="warning" size="sm" className="font-mono">
                    {s}
                  </Badge>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      {spConnected && (sp.access_expires_at || sp.refresh_expires_at) && (
        <dl className="space-y-1 rounded-lg border border-border bg-muted/40 p-3 text-xs">
          {sp.access_expires_at && (
            <div className="flex items-center justify-between gap-3">
              <dt className="text-muted-foreground">{t("credentials.oauth.accessExpires")}</dt>
              <dd className="font-mono text-foreground">{formatTime(sp.access_expires_at)}</dd>
            </div>
          )}
          {sp.refresh_expires_at && (
            <div className="flex items-center justify-between gap-3">
              <dt className="text-muted-foreground">{t("credentials.oauth.refreshExpires")}</dt>
              <dd className="font-mono text-foreground">{formatTime(sp.refresh_expires_at)}</dd>
            </div>
          )}
        </dl>
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
        {spConnected && sp.available && sp.needs_reconnect && (
          <Button
            size="sm"
            loading={oauthFlowActive[sp.provider]}
            onClick={() => connectOAuth(sp.provider)}
          >
            {t("credentials.oauth.reconnect")}
          </Button>
        )}
        {spConnected && sp.available && (
          <Button
            size="sm"
            variant="destructive-outline"
            className="text-destructive hover:bg-destructive/10"
            onClick={() => confirmDisconnectOAuth(sp.provider)}
          >
            {t("credentials.oauth.disconnect")}
          </Button>
        )}
      </div>

      {spFlowError && !spFlow && (
        <div className="rounded-lg border border-destructive/36 bg-destructive/8 p-3 text-xs">
          <p className="font-medium text-destructive-foreground">
            {t("credentials.oauth.flowFailed")}
          </p>
          <p className="mt-1 break-words text-muted-foreground">{spFlowError}</p>
        </div>
      )}

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
        <Fieldset className="space-y-3 border-t border-border pt-4">
          <FieldsetLegend>{t("credentials.oauth.app")}</FieldsetLegend>
          <Field>
            <FieldLabel>{t("credentials.oauth.clientId")}</FieldLabel>
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
          </Field>
          <Field>
            <FieldLabel>{t("credentials.oauth.clientSecret")}</FieldLabel>
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
          </Field>
          <Field>
            <FieldLabel>{t("credentials.oauth.scopes.title")}</FieldLabel>
            <ScopeEditor
              value={scopeDraft[sp.provider] ?? []}
              saved={spMeta?.saved ?? []}
              defaults={spMeta?.defaults ?? []}
              onChange={(next) => setScopeDraft((prev) => ({ ...prev, [sp.provider]: next }))}
            />
            <FieldDescription>{t("credentials.oauth.scopes.saveHint")}</FieldDescription>
          </Field>
        </Fieldset>
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
                const connected = p.connected;
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
                    onClick: () => confirmDisconnectOAuth(p.provider),
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
                    icon={<ProviderIcon provider={p.provider} icon={p.icon} label={p.provider} />}
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
                                    onClick: () => confirmDeleteVaultEntry(entry),
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

      <ConfirmDialog
        open={confirm !== null}
        onOpenChange={(open) => {
          if (!open) setConfirm(null);
        }}
        title={confirm?.title ?? ""}
        message={confirm?.message ?? ""}
        confirmLabel={confirm?.confirmLabel}
        onConfirm={() => confirm?.onConfirm()}
      />

      <ToastContainer messages={toasts} />
    </>
  );
}
