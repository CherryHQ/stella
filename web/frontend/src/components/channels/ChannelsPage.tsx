import { useCallback, useEffect, useRef, useState } from "react";
import QRCode from "qrcode";
import { api } from "@/lib/api";
import type { Channel, Identity, Plugin } from "@/lib/types";

// ─── platform metadata ────────────────────────────────────────────────────────

type PlatformDefaults = Record<string, string | boolean>;

const platformMeta: Record<string, { label: string; defaults: PlatformDefaults }> = {
  telegram: {
    label: "Telegram",
    defaults: { token: "", channel_id: "", group_mode: "" },
  },
  qq: {
    label: "QQ",
    defaults: { app_id: "", app_secret: "", group_mode: "" },
  },
  feishu: {
    label: "Feishu",
    defaults: {
      app_id: "",
      app_secret: "",
      encrypt_key: "",
      verification_token: "",
      group_mode: "",
      tenant_key: "",
      auto_provision: false,
    },
  },
  weixin: { label: "Weixin", defaults: {} },
};

const channelTypes = Object.entries(platformMeta).map(([id, meta]) => ({ id, label: meta.label }));
const defaultChannelType = channelTypes[0]?.id || "";

function parseConfig(raw: string): Record<string, unknown> {
  try {
    return JSON.parse(raw || "{}");
  } catch {
    return {};
  }
}

function platformConfigDefaults(type: string): PlatformDefaults {
  return { ...(platformMeta[type]?.defaults || {}) };
}

function normalizeConfigValue(defaultValue: string | boolean, value: unknown): string | boolean {
  if (typeof defaultValue === "boolean") return Boolean(value);
  return (value as string) || "";
}

function serializePlatformConfig(type: string, data: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(
    Object.entries(platformConfigDefaults(type)).map(([key, defaultValue]) => [
      key,
      normalizeConfigValue(defaultValue, data[key]),
    ]),
  );
}

function hasConfig(type: string, data: Record<string, unknown>): boolean {
  return Object.values(serializePlatformConfig(type, data)).some((v) => {
    if (typeof v === "boolean") return v;
    return String(v).trim() !== "";
  });
}

// ─── types ────────────────────────────────────────────────────────────────────

interface NormalizedChannel extends Record<string, unknown> {
  id: string;
  type: string;
  label: string;
  agent_id: string;
  agent_name: string;
  enabled: boolean;
  _collapsed: boolean;
}

function normalizeChannel(ch: Channel): NormalizedChannel {
  const type = ch.type || ch.id;
  return {
    ...ch,
    type,
    agent_id: ch.agent_id || "",
    _collapsed: true,
    ...platformConfigDefaults(type),
    ...parseConfig(ch.config),
  };
}

function newInstanceDraft(type = defaultChannelType, id = ""): Record<string, unknown> {
  return { id, type, ...platformConfigDefaults(type) };
}

function channelConfig(ch: Record<string, unknown>): string {
  return JSON.stringify(serializePlatformConfig(ch.type as string, ch));
}

// ─── toast ────────────────────────────────────────────────────────────────────

interface Toast {
  message: string;
  kind: "success" | "error";
}

// ─── sub-components ───────────────────────────────────────────────────────────

function InstanceFields({
  ch,
  onChange,
}: {
  ch: Record<string, unknown>;
  onChange: (key: string, value: unknown) => void;
}) {
  const type = ch.type as string;
  const field = (key: string, label: string, inputType = "text", placeholder = "") => (
    <div className="form-control w-full">
      <label className="label">
        <span className="label-text font-mono font-medium text-sm">{label}</span>
      </label>
      <input
        type={inputType}
        value={(ch[key] as string) || ""}
        onChange={(e) => onChange(key, e.target.value)}
        placeholder={placeholder}
        className="input input-bordered w-full text-sm font-mono"
      />
    </div>
  );

  return (
    <div className="space-y-4">
      {type === "telegram" && (
        <div className="space-y-4">
          {field("token", "Bot Token", "password", "From @BotFather")}
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-x-8 gap-y-4">
            {field("channel_id", "Channel ID", "text", "Default channel")}
            <div className="form-control w-full">
              <label className="label">
                <span className="label-text font-mono font-medium text-sm">Group Mode</span>
              </label>
              <select
                value={(ch.group_mode as string) || ""}
                onChange={(e) => onChange("group_mode", e.target.value)}
                className="select select-bordered w-full text-sm"
              >
                <option value="">Default</option>
                <option value="mention">Mention</option>
                <option value="always">Always</option>
                <option value="disabled">Disabled</option>
              </select>
            </div>
          </div>
        </div>
      )}

      {type === "qq" && (
        <div className="space-y-4">
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-x-8 gap-y-4">
            {field("app_id", "App ID", "text", "QQ Bot App ID")}
            {field("app_secret", "App Secret", "password")}
            <div className="form-control w-full">
              <label className="label">
                <span className="label-text font-mono font-medium text-sm">Group Mode</span>
              </label>
              <select
                value={(ch.group_mode as string) || ""}
                onChange={(e) => onChange("group_mode", e.target.value)}
                className="select select-bordered w-full text-sm"
              >
                <option value="">Default (mention)</option>
                <option value="mention">Mention</option>
                <option value="always">Always</option>
                <option value="disabled">Disabled</option>
              </select>
            </div>
          </div>
        </div>
      )}

      {type === "feishu" && (
        <div className="space-y-4">
          <p className="text-xs text-secondary">
            Feishu is chat-only. Add a <code>lark-cli</code> skill yourself if you want Lark workspace automation.
          </p>
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-x-8 gap-y-4">
            {field("app_id", "App ID")}
            {field("app_secret", "App Secret", "password")}
            {field("encrypt_key", "Encrypt Key", "password", "optional")}
            {field("verification_token", "Verification Token", "password", "optional")}
            <div className="form-control w-full">
              <label className="label">
                <span className="label-text font-mono font-medium text-sm">Group Mode</span>
              </label>
              <select
                value={(ch.group_mode as string) || ""}
                onChange={(e) => onChange("group_mode", e.target.value)}
                className="select select-bordered w-full text-sm"
              >
                <option value="">Default (mention)</option>
                <option value="mention">Mention</option>
                <option value="always">Always</option>
                <option value="disabled">Disabled</option>
              </select>
            </div>
            {field("tenant_key", "Tenant Key", "text", "optional, auto-detected at startup")}
          </div>
          <div className="form-control">
            <label className="label cursor-pointer gap-3 justify-start">
              <input
                type="checkbox"
                checked={Boolean(ch.auto_provision)}
                onChange={(e) => onChange("auto_provision", e.target.checked)}
                className="toggle toggle-primary toggle-sm"
              />
              <span className="label-text text-sm">Auto-provision accounts for tenant members</span>
            </label>
          </div>
        </div>
      )}

      {type === "weixin" && (
        <p className="text-xs text-secondary">
          Weixin dedicated instances currently only expose notification settings here.
        </p>
      )}
    </div>
  );
}

// ─── main page ────────────────────────────────────────────────────────────────

export function ChannelsPage() {
  const [isAdmin, setIsAdmin] = useState(false);
  const [publicChannels, setPublicChannels] = useState<Channel[]>([]);
  const [linkedIdentities, setLinkedIdentities] = useState<Identity[]>([]);
  const [instances, setInstances] = useState<NormalizedChannel[]>([]);
  const [enabledChannelTypeIDs, setEnabledChannelTypeIDs] = useState<string[]>(
    channelTypes.map((t) => t.id),
  );
  const [loadingPlatforms, setLoadingPlatforms] = useState(false);
  const [loadingInstances, setLoadingInstances] = useState(false);

  const [linkCode, setLinkCode] = useState("");
  const [linkPlatform, setLinkPlatform] = useState("");
  const [generating, setGenerating] = useState(false);

  const [wxQrUrl, setWxQrUrl] = useState("");
  const [wxQrStatus, setWxQrStatus] = useState("");
  const wxQrCodeRef = useRef("");
  const [wxQrPolling, setWxQrPolling] = useState(false);
  const wxQrIntervalRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const [showNewInstanceForm, setShowNewInstanceForm] = useState(false);
  const [newChannel, setNewChannel] = useState<Record<string, unknown>>(newInstanceDraft());
  const [creatingInstance, setCreatingInstance] = useState(false);

  const [confirmMsg, setConfirmMsg] = useState("");
  const confirmActionRef = useRef<() => void>(() => {});

  const [toast, setToast] = useState<Toast | null>(null);
  const toastTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // ── helpers ──

  const showToast = useCallback((message: string, kind: "success" | "error" = "success") => {
    setToast({ message, kind });
    if (toastTimerRef.current) clearTimeout(toastTimerRef.current);
    toastTimerRef.current = setTimeout(() => setToast(null), 3000);
  }, []);

  const fallbackChannelType =
    channelTypes.find((t) => enabledChannelTypeIDs.includes(t.id))?.id || defaultChannelType;

  const resetNewChannel = useCallback(
    (type = fallbackChannelType, id = "") => {
      const nextType = enabledChannelTypeIDs.includes(type) ? type : fallbackChannelType;
      setNewChannel(newInstanceDraft(nextType, id));
    },
    [enabledChannelTypeIDs, fallbackChannelType],
  );

  const identityFor = useCallback(
    (platform: string): Identity | null =>
      linkedIdentities.find((i) => i.platform === platform) || null,
    [linkedIdentities],
  );

  const isLinked = useCallback((platform: string) => Boolean(identityFor(platform)), [identityFor]);

  const configForPlatform = useCallback(
    (type: string): NormalizedChannel | null =>
      instances.find((ch) => ch.type === type && ch.id === type) || null,
    [instances],
  );

  const dedicatedInstancesFor = useCallback(
    (type: string): NormalizedChannel[] => instances.filter((ch) => ch.type === type && ch.id !== type),
    [instances],
  );

  const isDefaultPlatformInstance = (ch: NormalizedChannel) => ch.id === ch.type;

  const instanceStatus = (ch: NormalizedChannel) =>
    hasConfig(ch.type, ch) ? "Configured" : "Needs config";

  const instanceStatusClass = (ch: NormalizedChannel) =>
    hasConfig(ch.type, ch) ? "badge-success" : "badge-ghost";

  const identityLabel = (identity: Identity | null) => {
    if (!identity) return "";
    const name = identity.name ? identity.name + " · " : "";
    return name + identity.external_id;
  };

  const platformDescription = (channel: Channel) => {
    if (!channel) return "";
    if (isLinked(channel.type)) return `Your ${channel.label} account is linked and ready to use with Anna.`;
    if (channel.type === "weixin") return "Link your Weixin account by scanning a QR code.";
    return `Link your ${channel.label} account once to chat with Anna on this platform.`;
  };

  const linkedAgentLabel = (channel: Channel) => {
    if (!channel?.agent_id) return "";
    return channel.agent_name
      ? `${channel.agent_name} (${channel.agent_id})`
      : channel.agent_id;
  };

  const platformLabel = (type: string) => platformMeta[type]?.label || type;

  // ── data loading ──

  const loadIdentities = useCallback(async () => {
    try {
      const data = await api<Identity[]>("GET", "/api/auth/profile/identities");
      setLinkedIdentities(data || []);
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }, [showToast]);

  const loadPublicChannels = useCallback(async () => {
    setLoadingPlatforms(true);
    try {
      const data = await api<Channel[]>("GET", "/api/channels/public");
      setPublicChannels(data || []);
    } catch (e) {
      showToast((e as Error).message, "error");
    } finally {
      setLoadingPlatforms(false);
    }
  }, [showToast]);

  const loadChannelPlugins = useCallback(async () => {
    try {
      const plugins = await api<Plugin[]>("GET", "/api/plugins");
      const enabled = (plugins || [])
        .filter((p) => p.kind === "channel" && p.enabled)
        .map((p) => p.name || String(p.id || "").replace(/^channel\//, ""));
      setEnabledChannelTypeIDs(enabled.length > 0 ? enabled : channelTypes.map((t) => t.id));
    } catch {
      setEnabledChannelTypeIDs(channelTypes.map((t) => t.id));
    }
  }, []);

  const loadInstances = useCallback(
    async (currentEnabledIDs: string[]) => {
      setLoadingInstances(true);
      try {
        const channels = await api<Channel[]>("GET", "/api/channels");
        const normalized = (channels || [])
          .map(normalizeChannel)
          .filter((ch) => currentEnabledIDs.includes(ch.type) && ch.enabled)
          .sort((a, b) => {
            const aDefault = a.id === a.type;
            const bDefault = b.id === b.type;
            if (aDefault !== bDefault) return aDefault ? -1 : 1;
            if (a.type !== b.type) return a.type.localeCompare(b.type);
            return a.id.localeCompare(b.id);
          });
        setInstances(normalized);
      } catch (e) {
        showToast((e as Error).message, "error");
      } finally {
        setLoadingInstances(false);
      }
    },
    [showToast],
  );

  // ── init ──

  useEffect(() => {
    const init = async () => {
      let admin = false;
      try {
        const me = await api<{ is_admin: boolean }>("GET", "/api/auth/me");
        admin = Boolean(me?.is_admin);
        setIsAdmin(admin);
      } catch {
        /* ignore */
      }

      if (admin) {
        await loadChannelPlugins();
        await Promise.all([
          loadPublicChannels(),
          loadIdentities(),
          (async () => {
            // loadChannelPlugins sets state async; we need the IDs for loadInstances
            try {
              const pluginList = await api<Plugin[]>("GET", "/api/plugins");
              const enabled = (pluginList || [])
                .filter((p) => p.kind === "channel" && p.enabled)
                .map((p) => p.name || String(p.id || "").replace(/^channel\//, ""));
              const ids = enabled.length > 0 ? enabled : channelTypes.map((t) => t.id);
              setEnabledChannelTypeIDs(ids);
              await loadInstances(ids);
            } catch {
              const ids = channelTypes.map((t) => t.id);
              setEnabledChannelTypeIDs(ids);
              await loadInstances(ids);
            }
          })(),
        ]);
      } else {
        await Promise.all([loadPublicChannels(), loadIdentities()]);
      }
    };
    init();
    return () => {
      if (toastTimerRef.current) clearTimeout(toastTimerRef.current);
      if (wxQrIntervalRef.current) clearInterval(wxQrIntervalRef.current);
    };
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  // ── link code ──

  const generateCode = async (platform: string) => {
    setGenerating(true);
    setLinkPlatform(platform);
    setLinkCode("");
    setWxQrUrl("");
    setWxQrStatus("");
    wxQrCodeRef.current = "";
    try {
      const result = await api<{ code: string }>("POST", "/api/auth/profile/link-code", { platform });
      setLinkCode(result.code);
    } catch (e) {
      showToast((e as Error).message, "error");
    } finally {
      setGenerating(false);
    }
  };

  const copyLinkCode = () => {
    navigator.clipboard.writeText("/link " + linkCode);
    showToast("Copied");
  };

  // ── weixin QR ──

  const stopWeixinQRPolling = () => {
    if (wxQrIntervalRef.current) {
      clearInterval(wxQrIntervalRef.current);
      wxQrIntervalRef.current = null;
    }
    setWxQrPolling(false);
  };

  const pollWeixinQRStatus = async (qrCode: string) => {
    if (!qrCode) return;
    try {
      const result = await api<{ status: string }>(
        "GET",
        "/api/channels/weixin/qr/status?qrcode=" + encodeURIComponent(qrCode),
      );
      if (result.status) setWxQrStatus(result.status);
      if (result.status === "confirmed") {
        stopWeixinQRPolling();
        setWxQrUrl("");
        showToast("Weixin account linked successfully");
        await loadIdentities();
      } else if (result.status === "expired") {
        stopWeixinQRPolling();
      }
    } catch (e) {
      console.error("QR status poll error:", e);
    }
  };

  const startWeixinQR = async () => {
    setLinkCode("");
    setWxQrUrl("");
    setWxQrStatus("");
    wxQrCodeRef.current = "";
    setWxQrPolling(true);
    if (wxQrIntervalRef.current) {
      clearInterval(wxQrIntervalRef.current);
      wxQrIntervalRef.current = null;
    }
    try {
      const result = await api<{ qrcode: string; qrcode_img_content: string }>(
        "POST",
        "/api/channels/weixin/qr",
      );
      const qrCode = result.qrcode || "";
      wxQrCodeRef.current = qrCode;
      const imgContent = result.qrcode_img_content || "";
      if (imgContent) {
        const dataUrl = await QRCode.toDataURL(imgContent, { width: 256, margin: 2 });
        setWxQrUrl(dataUrl);
      }
      setWxQrStatus("waiting");
      wxQrIntervalRef.current = setInterval(() => pollWeixinQRStatus(qrCode), 3000);
    } catch (e) {
      showToast("QR request failed: " + (e as Error).message, "error");
      setWxQrPolling(false);
    }
  };

  // ── identity management ──

  const unlinkIdentity = async (id: number | undefined) => {
    if (!id || !confirm("Unlink this identity?")) return;
    try {
      await api("DELETE", "/api/auth/profile/identities/" + id);
      showToast("Identity unlinked");
      await loadIdentities();
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  };

  // ── instance management ──

  const updateInstance = (id: string, updates: Record<string, unknown>) => {
    setInstances((prev) =>
      prev.map((ch) => (ch.id === id ? { ...ch, ...updates } : ch)),
    );
  };

  const saveInstance = async (ch: NormalizedChannel) => {
    try {
      const saved = await api<Channel>("PUT", "/api/channels/" + encodeURIComponent(ch.id), {
        type: ch.type,
        agent_id: ch.agent_id || "",
        config: channelConfig(ch),
      });
      updateInstance(ch.id, { ...normalizeChannel(saved), _collapsed: false });
      showToast(ch.id + " saved");
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  };

  const doDeleteChannel = async (id: string) => {
    const ch = instances.find((c) => c.id === id);
    if (ch && isDefaultPlatformInstance(ch)) {
      showToast("Default platform channels cannot be deleted", "error");
      return;
    }
    try {
      await api("DELETE", "/api/channels/" + encodeURIComponent(id));
      await loadInstances(enabledChannelTypeIDs);
      showToast(id + " deleted");
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  };

  const createChannel = async () => {
    const id = ((newChannel.id as string) || "").trim();
    if (!id || !newChannel.type) {
      showToast("ID and platform are required", "error");
      return;
    }
    if (id === newChannel.type) {
      showToast("Dedicated instance ID must not match the platform ID", "error");
      return;
    }
    setCreatingInstance(true);
    try {
      const saved = await api<Channel>("POST", "/api/channels", {
        id,
        type: newChannel.type,
        agent_id: "",
        config: channelConfig(newChannel),
      });
      setShowNewInstanceForm(false);
      resetNewChannel((saved.type as string) || fallbackChannelType);
      await loadInstances(enabledChannelTypeIDs);
      showToast(saved.id + " created");
    } catch (e) {
      showToast((e as Error).message, "error");
    } finally {
      setCreatingInstance(false);
    }
  };

  const toggleNewInstanceForm = (type: string) => {
    if (showNewInstanceForm && newChannel.type === type) {
      setShowNewInstanceForm(false);
      resetNewChannel(newChannel.type as string, "");
    } else {
      resetNewChannel(type);
      setShowNewInstanceForm(true);
    }
  };

  // ── render ──

  const isLoading = loadingPlatforms || (isAdmin && loadingInstances);

  return (
    <div>
      {/* Toast */}
      {toast && (
        <div className="fixed top-2 left-1/2 -translate-x-1/2 z-50">
          <div
            className={`alert text-sm font-mono shadow-lg ${toast.kind === "error" ? "alert-error" : "alert-success"}`}
          >
            {toast.message}
          </div>
        </div>
      )}

      {/* Page header */}
      <div className="mb-8">
        <h1 className="font-serif text-3xl md:text-4xl tracking-tight mb-2">Channels</h1>
        <p className="text-secondary text-sm max-w-lg">
          Link the platforms you use. The Plugins page controls which platforms appear here.
        </p>
      </div>

      <section className="border-t border-base-300 pt-8">
        <div className="flex items-start justify-between gap-4 mb-6 flex-wrap">
          <div>
            <h2 className="text-lg font-medium">Platforms</h2>
            <p className="text-sm text-secondary mt-1">
              Link your own account here. Admins can also configure the platform default and any dedicated
              instances in the same card.
            </p>
          </div>
          <span className="text-xs font-mono text-secondary">{linkedIdentities.length} linked</span>
        </div>

        {isLoading ? (
          <div className="flex justify-center py-8">
            <span className="loading loading-spinner loading-sm"></span>
          </div>
        ) : (
          <div className="grid grid-cols-1 xl:grid-cols-2 gap-4">
            {publicChannels.length === 0 ? (
              <div className="card bg-base-200 border border-dashed border-base-300 xl:col-span-2">
                <div className="card-body text-sm text-secondary">
                  <p>No platform channels are available right now.</p>
                  {isAdmin && (
                    <p>
                      Enable one or more channel plugins on the{" "}
                      <a href="/plugins" className="link link-primary">
                        Plugins
                      </a>{" "}
                      page.
                    </p>
                  )}
                </div>
              </div>
            ) : (
              publicChannels.map((channel) => (
                <PlatformCard
                  key={channel.id}
                  channel={channel}
                  isAdmin={isAdmin}
                  identity={identityFor(channel.type)}
                  linked={isLinked(channel.type)}
                  platformDescription={platformDescription(channel)}
                  linkedAgentLabel={linkedAgentLabel(channel)}
                  identityLabel={identityLabel}
                  generating={generating}
                  linkPlatform={linkPlatform}
                  wxQrPolling={wxQrPolling}
                  configInstance={configForPlatform(channel.type)}
                  dedicatedInstances={dedicatedInstancesFor(channel.type)}
                  showNewInstanceForm={showNewInstanceForm && newChannel.type === channel.type}
                  newChannel={newChannel}
                  creatingInstance={creatingInstance}
                  instanceStatus={instanceStatus}
                  instanceStatusClass={instanceStatusClass}
                  onGenerateCode={generateCode}
                  onStartWeixinQR={startWeixinQR}
                  onUnlink={(id) => unlinkIdentity(id)}
                  onToggleNewInstanceForm={() => toggleNewInstanceForm(channel.type)}
                  onNewChannelChange={(key, value) =>
                    setNewChannel((prev) => {
                      if (key === "type") return newInstanceDraft(value as string, prev.id as string);
                      return { ...prev, [key]: value };
                    })
                  }
                  onCreateChannel={createChannel}
                  onCancelNewInstance={() => {
                    setShowNewInstanceForm(false);
                    resetNewChannel(newChannel.type as string, "");
                  }}
                  onSaveInstance={saveInstance}
                  onDeleteInstance={(id) => {
                    confirmActionRef.current = () => doDeleteChannel(id);
                    setConfirmMsg(`Delete dedicated instance ${id}?`);
                  }}
                  onUpdateInstance={updateInstance}
                  onToggleInstanceCollapse={(id, collapsed) => updateInstance(id, { _collapsed: collapsed })}
                  onToggleConfigCollapse={(type) => {
                    const cfg = configForPlatform(type);
                    if (cfg) updateInstance(cfg.id, { _collapsed: !cfg._collapsed });
                  }}
                />
              ))
            )}
          </div>
        )}

        {/* Link code alert */}
        {linkCode && (
          <div className="alert shadow-md mt-4">
            <div>
              <p className="font-medium">
                Send this command to Anna on {platformLabel(linkPlatform)}:
              </p>
              <div className="flex items-center gap-2 mt-2 flex-wrap">
                <code className="font-mono text-lg font-bold bg-base-200 text-base-content px-3 py-1 rounded select-all">
                  /link {linkCode}
                </code>
                <button onClick={copyLinkCode} className="btn btn-ghost btn-xs">
                  copy
                </button>
              </div>
              <p className="text-xs text-secondary mt-2">Expires in 5 minutes.</p>
            </div>
          </div>
        )}

        {/* Weixin QR */}
        {wxQrUrl && (
          <div className="card bg-base-200 mt-4 border border-base-300">
            <div className="card-body py-4 items-center">
              <p className="text-sm font-medium mb-2">Scan with WeChat to link your account</p>
              <img src={wxQrUrl} alt="WeChat QR Code" className="w-48 h-48 border rounded" />
              <span
                className={`badge badge-sm mt-2 ${
                  wxQrStatus === "waiting"
                    ? "badge-warning"
                    : wxQrStatus === "scaned"
                      ? "badge-info"
                      : wxQrStatus === "confirmed"
                        ? "badge-success"
                        : wxQrStatus === "expired"
                          ? "badge-error"
                          : ""
                }`}
              >
                {wxQrStatus}
              </span>
              {wxQrStatus === "expired" && (
                <button onClick={startWeixinQR} className="btn btn-outline btn-xs mt-1">
                  Refresh
                </button>
              )}
            </div>
          </div>
        )}
      </section>

      {/* Confirm dialog */}
      {confirmMsg && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
          <div className="card bg-base-100 shadow-xl w-full max-w-sm">
            <div className="card-body">
              <p className="text-sm">{confirmMsg}</p>
              <div className="card-actions justify-end mt-4">
                <button onClick={() => setConfirmMsg("")} className="btn btn-ghost btn-sm">
                  Cancel
                </button>
                <button
                  onClick={() => {
                    confirmActionRef.current();
                    setConfirmMsg("");
                  }}
                  className="btn btn-error btn-sm"
                >
                  Delete
                </button>
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

// ─── PlatformCard ─────────────────────────────────────────────────────────────

interface PlatformCardProps {
  channel: Channel;
  isAdmin: boolean;
  identity: Identity | null;
  linked: boolean;
  platformDescription: string;
  linkedAgentLabel: string;
  identityLabel: (i: Identity | null) => string;
  generating: boolean;
  linkPlatform: string;
  wxQrPolling: boolean;
  configInstance: NormalizedChannel | null;
  dedicatedInstances: NormalizedChannel[];
  showNewInstanceForm: boolean;
  newChannel: Record<string, unknown>;
  creatingInstance: boolean;
  instanceStatus: (ch: NormalizedChannel) => string;
  instanceStatusClass: (ch: NormalizedChannel) => string;
  onGenerateCode: (platform: string) => void;
  onStartWeixinQR: () => void;
  onUnlink: (id: number | undefined) => void;
  onToggleNewInstanceForm: () => void;
  onNewChannelChange: (key: string, value: unknown) => void;
  onCreateChannel: () => void;
  onCancelNewInstance: () => void;
  onSaveInstance: (ch: NormalizedChannel) => void;
  onDeleteInstance: (id: string) => void;
  onUpdateInstance: (id: string, updates: Record<string, unknown>) => void;
  onToggleInstanceCollapse: (id: string, collapsed: boolean) => void;
  onToggleConfigCollapse: (type: string) => void;
}

function PlatformCard({
  channel,
  isAdmin,
  identity,
  linked,
  platformDescription,
  linkedAgentLabel,
  identityLabel,
  generating,
  linkPlatform,
  wxQrPolling,
  configInstance,
  dedicatedInstances,
  showNewInstanceForm,
  newChannel,
  creatingInstance,
  instanceStatus,
  instanceStatusClass,
  onGenerateCode,
  onStartWeixinQR,
  onUnlink,
  onToggleNewInstanceForm,
  onNewChannelChange,
  onCreateChannel,
  onCancelNewInstance,
  onSaveInstance,
  onDeleteInstance,
  onUpdateInstance,
  onToggleInstanceCollapse,
  onToggleConfigCollapse,
}: PlatformCardProps) {
  return (
    <div className="card bg-base-200 border border-base-300">
      <div className="card-body gap-6">
        {/* Header */}
        <div className="flex items-start justify-between gap-3 flex-wrap">
          <div>
            <div className="flex items-center gap-2 flex-wrap">
              <h3 className="font-medium text-base">{channel.label}</h3>
              <span className="badge badge-ghost badge-sm">platform</span>
              <span className={`badge badge-sm ${linked ? "badge-success" : "badge-ghost"}`}>
                {linked ? "linked" : "not linked"}
              </span>
              {isAdmin && configInstance && (
                <span className={`badge badge-sm ${instanceStatusClass(configInstance)}`}>
                  {instanceStatus(configInstance)}
                </span>
              )}
            </div>
            <p className="text-sm text-secondary mt-2">{platformDescription}</p>
          </div>
          {channel.agent_id && (
            <span className="badge badge-primary badge-sm">agent: {linkedAgentLabel}</span>
          )}
        </div>

        {/* My account */}
        <div className="space-y-2 text-sm">
          <p className="text-xs font-mono text-secondary uppercase tracking-wider">My account</p>
          {identity ? (
            <div>
              <p className="text-xs text-secondary mb-1">Linked identity</p>
              <p className="font-mono text-sm">{identityLabel(identity)}</p>
            </div>
          ) : (
            <p className="text-sm text-secondary">No account linked yet.</p>
          )}
        </div>

        {/* Actions */}
        <div className="card-actions justify-start">
          {!linked && channel.type !== "weixin" && (
            <button
              onClick={() => onGenerateCode(channel.type)}
              disabled={generating}
              className="btn btn-primary btn-sm"
            >
              {generating && linkPlatform === channel.type && (
                <span className="loading loading-spinner loading-xs"></span>
              )}
              Link {channel.label}
            </button>
          )}
          {!linked && channel.type === "weixin" && (
            <button onClick={onStartWeixinQR} disabled={wxQrPolling} className="btn btn-primary btn-sm">
              {wxQrPolling && <span className="loading loading-spinner loading-xs"></span>}
              Link Weixin
            </button>
          )}
          {linked && (
            <button
              onClick={() => onUnlink(identity?.id)}
              className="btn btn-ghost btn-sm text-error"
            >
              Unlink
            </button>
          )}
        </div>

        {/* Admin: default config */}
        {isAdmin && configInstance && (
          <div className="border-t border-base-300 pt-5 space-y-4">
            <div className="flex items-start justify-between gap-3 flex-wrap">
              <div>
                <p className="text-xs font-mono text-secondary uppercase tracking-wider">
                  Default channel config
                </p>
                <p className="text-sm text-secondary mt-1">
                  Shared bot or app credentials for this platform's default channel.
                </p>
              </div>
              <div className="flex items-center gap-2 flex-wrap">
                {configInstance.agent_id && (
                  <span className="badge badge-outline badge-sm">
                    agent: {configInstance.agent_id as string}
                  </span>
                )}
                <button
                  onClick={() => onToggleConfigCollapse(channel.type)}
                  className="btn btn-ghost btn-sm"
                >
                  {configInstance._collapsed ? "Configure" : "Collapse"}
                </button>
              </div>
            </div>
            {!configInstance._collapsed && (
              <div className="space-y-4">
                <InstanceFields
                  ch={configInstance}
                  onChange={(key, value) => onUpdateInstance(configInstance.id, { [key]: value })}
                />
                <div className="flex items-center justify-between gap-3 flex-wrap">
                  <p className="text-xs text-secondary">
                    This config controls the platform default. Agent selection belongs on the agent page.
                  </p>
                  <button onClick={() => onSaveInstance(configInstance)} className="btn btn-primary btn-sm">
                    Save default config
                  </button>
                </div>
              </div>
            )}
          </div>
        )}

        {/* Admin: dedicated instances */}
        {isAdmin && (
          <div className="border-t border-base-300 pt-5 space-y-4">
            <div className="flex items-start justify-between gap-3 flex-wrap">
              <div>
                <p className="text-xs font-mono text-secondary uppercase tracking-wider">
                  Dedicated instances
                </p>
                <p className="text-sm text-secondary mt-1">
                  Reusable per-platform instances with separate credentials.
                </p>
              </div>
              <div className="flex items-center gap-2 flex-wrap">
                <span className="text-xs font-mono text-secondary">
                  {dedicatedInstances.length} instances
                </span>
                <button onClick={onToggleNewInstanceForm} className="btn btn-primary btn-xs">
                  Add dedicated instance
                </button>
              </div>
            </div>

            {/* New instance form */}
            {showNewInstanceForm && (
              <div className="card bg-base-100 border border-base-300">
                <div className="card-body space-y-4">
                  <div className="form-control w-full">
                    <label className="label">
                      <span className="label-text font-mono font-medium text-sm">Instance ID</span>
                    </label>
                    <input
                      type="text"
                      value={(newChannel.id as string) || ""}
                      onChange={(e) => onNewChannelChange("id", e.target.value)}
                      placeholder="feishu-coder"
                      className="input input-bordered w-full text-sm font-mono"
                    />
                  </div>
                  <InstanceFields ch={newChannel} onChange={onNewChannelChange} />
                  <div className="card-actions justify-end">
                    <button onClick={onCancelNewInstance} className="btn btn-ghost btn-sm">
                      Cancel
                    </button>
                    <button
                      onClick={onCreateChannel}
                      disabled={creatingInstance || !newChannel.id || !newChannel.type}
                      className="btn btn-primary btn-sm"
                    >
                      {creatingInstance && <span className="loading loading-spinner loading-xs"></span>}
                      Create instance
                    </button>
                  </div>
                </div>
              </div>
            )}

            {/* Dedicated instance list */}
            <div className="space-y-3">
              {dedicatedInstances.map((ch) => (
                <div key={ch.id} className="card bg-base-100 border border-base-300">
                  <div className="card-body gap-4">
                    <div className="flex items-start justify-between gap-4 flex-wrap">
                      <div className="min-w-0">
                        <div className="flex items-center gap-2 flex-wrap">
                          <h4 className="font-medium text-sm font-mono">{ch.id}</h4>
                          <span className="badge badge-ghost badge-sm">dedicated</span>
                          <span className={`badge badge-sm ${instanceStatusClass(ch)}`}>
                            {instanceStatus(ch)}
                          </span>
                          {ch.agent_id && (
                            <span className="badge badge-outline badge-sm">
                              agent: {ch.agent_id as string}
                            </span>
                          )}
                        </div>
                        <p className="text-sm text-secondary mt-2">
                          Dedicated instance. Configure it here, then attach it from the agent page.
                        </p>
                      </div>
                      <div className="flex items-center gap-2">
                        <button
                          onClick={() => onToggleInstanceCollapse(ch.id, !ch._collapsed)}
                          className="btn btn-ghost btn-sm"
                        >
                          {ch._collapsed ? "Configure" : "Collapse"}
                        </button>
                        <button
                          onClick={() => onDeleteInstance(ch.id)}
                          className="btn btn-ghost btn-sm text-error"
                        >
                          Delete
                        </button>
                      </div>
                    </div>
                    {!ch._collapsed && (
                      <div className="space-y-4">
                        <InstanceFields
                          ch={ch}
                          onChange={(key, value) => onUpdateInstance(ch.id, { [key]: value })}
                        />
                        <div className="flex items-center justify-between gap-3 flex-wrap">
                          <p className="text-xs text-secondary">
                            This page stores the instance config only. Agent selection belongs on the agent
                            page.
                          </p>
                          <button onClick={() => onSaveInstance(ch)} className="btn btn-primary btn-sm">
                            Save config
                          </button>
                        </div>
                      </div>
                    )}
                  </div>
                </div>
              ))}

              {dedicatedInstances.length === 0 && !showNewInstanceForm && (
                <div className="rounded-lg border border-dashed border-base-300 px-4 py-5 text-sm text-secondary">
                  <p>No dedicated instances yet.</p>
                  <p className="mt-1">
                    Create one when you need separate credentials for a specific workflow or agent.
                  </p>
                </div>
              )}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
