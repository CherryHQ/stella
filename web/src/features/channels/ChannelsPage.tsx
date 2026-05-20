import { useCallback, useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { meQueryOptions } from "@/lib/queries/me";
import QRCode from "qrcode";
import { api } from "@/lib/api";
import type { Channel, Identity, Plugin } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { Spinner } from "@/components/ui/spinner";
import {
  Dialog,
  DialogPopup,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from "@/components/ui/dialog";
import { siTelegram, siQq, siWechat } from "simple-icons";
import { useI18n } from "@/lib/i18n";
import { SettingsDetailLayout } from "@/features/settings/SettingsDetailLayout";

function BrandIcon({ path, className = "size-4 shrink-0" }: { path: string; className?: string }) {
  return (
    <svg viewBox="0 0 24 24" className={className} fill="currentColor" aria-hidden="true">
      <path d={path} />
    </svg>
  );
}

// ─── platform metadata ────────────────────────────────────────────────────────

type PlatformDefaults = Record<string, string | boolean>;

const platformMeta: Record<string, { label: string; defaults: PlatformDefaults; icon?: string }> = {
  telegram: {
    label: "Telegram",
    defaults: { token: "", channel_id: "", group_mode: "" },
    icon: siTelegram.path,
  },
  qq: {
    label: "QQ",
    defaults: { app_id: "", app_secret: "", group_mode: "" },
    icon: siQq.path,
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
  weixin: { label: "Weixin", defaults: {}, icon: siWechat.path },
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
  return { ...platformMeta[type]?.defaults };
}

function normalizeConfigValue(defaultValue: string | boolean, value: unknown): string | boolean {
  if (typeof defaultValue === "boolean") return Boolean(value);
  return (value as string) || "";
}

function serializePlatformConfig(
  type: string,
  data: Record<string, unknown>,
): Record<string, unknown> {
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
}

function normalizeChannel(ch: Channel): NormalizedChannel {
  const type = ch.type || ch.id;
  return {
    ...ch,
    type,
    agent_id: ch.agent_id || "",
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

const selectClassName =
  "h-9 w-full rounded-lg border border-input bg-background px-3 text-sm shadow-xs/5 outline-none sm:h-8 sm:text-sm";

function InstanceFields({
  ch,
  onChange,
}: {
  ch: Record<string, unknown>;
  onChange: (key: string, value: unknown) => void;
}) {
  const type = ch.type as string;
  const field = (key: string, label: string, inputType = "text", placeholder = "") => (
    <div className="w-full space-y-1.5">
      <label className="text-sm font-medium font-mono">{label}</label>
      <Input
        nativeInput
        type={inputType}
        value={(ch[key] as string) || ""}
        onChange={(e) => onChange(key, e.target.value)}
        placeholder={placeholder}
        className="w-full text-sm font-mono"
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
            <div className="w-full space-y-1.5">
              <label className="text-sm font-medium font-mono">Group Mode</label>
              <select
                value={(ch.group_mode as string) || ""}
                onChange={(e) => onChange("group_mode", e.target.value)}
                className={selectClassName}
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
            <div className="w-full space-y-1.5">
              <label className="text-sm font-medium font-mono">Group Mode</label>
              <select
                value={(ch.group_mode as string) || ""}
                onChange={(e) => onChange("group_mode", e.target.value)}
                className={selectClassName}
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
          <p className="text-xs text-muted-foreground">
            Feishu is chat-only. Add a <code>lark-cli</code> skill yourself if you want Lark
            workspace automation.
          </p>
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-x-8 gap-y-4">
            {field("app_id", "App ID")}
            {field("app_secret", "App Secret", "password")}
            {field("encrypt_key", "Encrypt Key", "password", "optional")}
            {field("verification_token", "Verification Token", "password", "optional")}
            <div className="w-full space-y-1.5">
              <label className="text-sm font-medium font-mono">Group Mode</label>
              <select
                value={(ch.group_mode as string) || ""}
                onChange={(e) => onChange("group_mode", e.target.value)}
                className={selectClassName}
              >
                <option value="">Default (mention)</option>
                <option value="mention">Mention</option>
                <option value="always">Always</option>
                <option value="disabled">Disabled</option>
              </select>
            </div>
            {field("tenant_key", "Tenant Key", "text", "optional, auto-detected at startup")}
          </div>
          <div className="flex items-center gap-3">
            <Switch
              checked={Boolean(ch.auto_provision)}
              onCheckedChange={(v) => onChange("auto_provision", v)}
            />
            <span className="text-sm">Auto-provision accounts for tenant members</span>
          </div>
        </div>
      )}

      {type === "weixin" && (
        <p className="text-xs text-muted-foreground">
          Weixin dedicated instances currently only expose notification settings here.
        </p>
      )}
    </div>
  );
}

// ─── ChannelDetail ────────────────────────────────────────────────────────────

interface ChannelDetailProps {
  channel: NormalizedChannel;
  identity: Identity | null;
  generating: boolean;
  linkPlatform: string;
  linkCode: string;
  wxQrUrl: string;
  wxQrStatus: string;
  wxQrPolling: boolean;
  onUpdate: (key: string, value: unknown) => void;
  onSave: (ch: NormalizedChannel) => void;
  onDelete: (id: string) => void;
  onGenerateCode: (platform: string) => void;
  onStartWeixinQR: () => void;
  onUnlink: (id: number | undefined) => void;
  onCopyLinkCode: () => void;
  wxQrStatusVariant: (status: string) => "warning" | "info" | "success" | "error" | "secondary";
  onRefreshWxQr: () => void;
}

function ChannelDetail({
  channel: initialChannel,
  identity,
  generating,
  linkPlatform,
  linkCode,
  wxQrUrl,
  wxQrStatus,
  wxQrPolling,
  onUpdate,
  onSave,
  onDelete,
  onGenerateCode,
  onStartWeixinQR,
  onUnlink,
  onCopyLinkCode,
  wxQrStatusVariant,
  onRefreshWxQr,
}: ChannelDetailProps) {
  const { t } = useI18n();
  const [channel, setChannel] = useState<NormalizedChannel>(initialChannel);
  const [confirmDeleteOpen, setConfirmDeleteOpen] = useState(false);

  useEffect(() => {
    setChannel(initialChannel);
    setConfirmDeleteOpen(false);
  }, [initialChannel]);

  const updateField = (key: string, value: unknown) => {
    setChannel((prev) => ({ ...prev, [key]: value }));
    onUpdate(key, value);
  };

  const isDefaultInstance = channel.id === channel.type;
  const platformLabel = platformMeta[channel.type]?.label || channel.type;

  return (
    <div className="flex flex-col h-full">
      {/* Scrollable body */}
      <div className="flex-1 overflow-y-auto p-6 space-y-6">
        {/* Panel header */}
        <div className="flex items-start justify-between gap-4">
          <div>
            <h2 className="font-serif text-xl tracking-tight flex items-center gap-2">
              {platformMeta[channel.type]?.icon && (
                <BrandIcon path={platformMeta[channel.type].icon!} className="size-5 shrink-0" />
              )}
              {platformLabel}
            </h2>
            <p className="text-xs font-mono text-muted-foreground mt-1">{channel.id}</p>
          </div>
          <div className="flex items-center gap-3 shrink-0">
            <Switch
              checked={Boolean(channel.enabled)}
              onCheckedChange={(checked) => updateField("enabled", checked)}
            />
            <span className="text-sm">Enabled</span>
          </div>
        </div>

        {/* Config section */}
        {Object.keys(platformConfigDefaults(channel.type)).length > 0 && (
          <div className="space-y-4">
            <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
              Configuration
            </p>
            <InstanceFields ch={channel} onChange={(key, value) => updateField(key, value)} />
            {hasConfig(channel.type, channel) && (
              <p className="text-xs text-muted-foreground">
                This page stores the channel config only. Agent selection belongs on the agent page.
              </p>
            )}
          </div>
        )}

        {/* Identity / account section */}
        <div className="space-y-3">
          <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
            My account
          </p>
          {identity ? (
            <div className="space-y-2">
              <p className="text-xs text-muted-foreground">Linked identity</p>
              <p className="font-mono text-sm">
                {identity.name ? identity.name + " · " : ""}
                {identity.external_id}
              </p>
              <Button
                onClick={() => onUnlink(identity.id)}
                variant="ghost"
                size="sm"
                className="text-destructive-foreground"
              >
                Unlink
              </Button>
            </div>
          ) : (
            <div className="space-y-2">
              <p className="text-sm text-muted-foreground">No account linked yet.</p>
              {channel.type !== "weixin" && (
                <Button
                  onClick={() => onGenerateCode(channel.type)}
                  disabled={generating}
                  loading={generating && linkPlatform === channel.type}
                  size="sm"
                >
                  Link {platformLabel}
                </Button>
              )}
              {channel.type === "weixin" && (
                <Button onClick={onStartWeixinQR} loading={wxQrPolling} size="sm">
                  Link Weixin
                </Button>
              )}
            </div>
          )}

          {/* Link code */}
          {linkCode && linkPlatform === channel.type && (
            <div className="rounded-lg border border-border bg-card p-4 space-y-2">
              <p className="text-sm font-medium">Send this command to Stella on {platformLabel}:</p>
              <div className="flex items-center gap-2 flex-wrap">
                <code className="font-mono text-lg font-bold bg-muted text-foreground px-3 py-1 rounded select-all">
                  /link {linkCode}
                </code>
                <Button onClick={onCopyLinkCode} variant="ghost" size="xs">
                  copy
                </Button>
              </div>
              <p className="text-xs text-muted-foreground">Expires in 5 minutes.</p>
            </div>
          )}

          {/* Weixin QR */}
          {wxQrUrl && channel.type === "weixin" && (
            <div className="rounded-xl border border-border bg-muted p-6 flex flex-col items-center">
              <p className="text-sm font-medium mb-2">Scan with WeChat to link your account</p>
              <img src={wxQrUrl} alt="WeChat QR Code" className="w-48 h-48 border rounded" />
              <Badge size="sm" variant={wxQrStatusVariant(wxQrStatus)} className="mt-2">
                {wxQrStatus}
              </Badge>
              {wxQrStatus === "expired" && (
                <Button onClick={onRefreshWxQr} variant="outline" size="xs" className="mt-1">
                  Refresh
                </Button>
              )}
            </div>
          )}
        </div>
      </div>

      {/* Sticky footer */}
      <div className="shrink-0 border-t border-border px-6 py-3 flex items-center justify-between gap-3 bg-background">
        <Button onClick={() => onSave(channel)} variant="default" size="sm">
          {t("common.save")}
        </Button>
        {!isDefaultInstance && (
          <Button
            onClick={() => setConfirmDeleteOpen(true)}
            variant="ghost"
            size="sm"
            className="text-muted-foreground hover:text-destructive"
          >
            {t("common.delete")}
          </Button>
        )}
      </div>

      {/* Confirm delete dialog */}
      <Dialog open={confirmDeleteOpen} onOpenChange={setConfirmDeleteOpen}>
        <DialogPopup showCloseButton={false}>
          <DialogHeader>
            <DialogTitle>Delete channel</DialogTitle>
          </DialogHeader>
          <div className="px-6 pb-2">
            <p className="text-sm">Delete channel {channel.id}?</p>
          </div>
          <DialogFooter>
            <Button onClick={() => setConfirmDeleteOpen(false)} variant="ghost" size="sm">
              {t("common.cancel")}
            </Button>
            <Button
              onClick={() => {
                setConfirmDeleteOpen(false);
                onDelete(channel.id);
              }}
              variant="destructive"
              size="sm"
            >
              {t("common.delete")}
            </Button>
          </DialogFooter>
        </DialogPopup>
      </Dialog>
    </div>
  );
}

// ─── NewChannelForm ───────────────────────────────────────────────────────────

interface NewChannelFormProps {
  enabledChannelTypeIDs: string[];
  fallbackChannelType: string;
  onAdd: (channel: Record<string, unknown>) => Promise<void>;
  onCancel: () => void;
  creating: boolean;
}

function NewChannelForm({
  enabledChannelTypeIDs,
  fallbackChannelType,
  onAdd,
  onCancel,
  creating,
}: NewChannelFormProps) {
  const { t } = useI18n();
  const availableTypes = channelTypes.filter((ct) => enabledChannelTypeIDs.includes(ct.id));
  const [draft, setDraft] = useState<Record<string, unknown>>(
    newInstanceDraft(fallbackChannelType, ""),
  );

  const updateField = (key: string, value: unknown) => {
    if (key === "type") {
      setDraft(newInstanceDraft(value as string, draft.id as string));
    } else {
      setDraft((prev) => ({ ...prev, [key]: value }));
    }
  };

  const canSubmit = !creating && !!draft.id && !!draft.type;

  return (
    <div className="flex flex-col h-full">
      <div className="flex-1 overflow-y-auto p-6 space-y-6">
        <div>
          <h2 className="font-serif text-xl tracking-tight">New Channel</h2>
          <p className="text-sm text-muted-foreground mt-1">
            Connect Stella to a messaging platform.
          </p>
        </div>

        <div className="space-y-4">
          <div className="w-full space-y-1.5">
            <label className="text-sm font-medium">Platform</label>
            <select
              value={(draft.type as string) || ""}
              onChange={(e) => updateField("type", e.target.value)}
              className={selectClassName}
            >
              <option value="" disabled>
                Select platform…
              </option>
              {availableTypes.map((ct) => (
                <option key={ct.id} value={ct.id}>
                  {ct.label}
                </option>
              ))}
            </select>
          </div>

          <div className="w-full space-y-1.5">
            <label className="text-sm font-medium font-mono">Channel ID</label>
            <Input
              nativeInput
              type="text"
              value={(draft.id as string) || ""}
              onChange={(e) => updateField("id", e.target.value)}
              placeholder="e.g. feishu-coder"
              className="w-full text-sm font-mono"
            />
            <p className="text-xs text-muted-foreground">
              Must not match the platform ID (e.g. not "telegram" for a Telegram channel).
            </p>
          </div>

          {!!draft.type && (
            <div className="space-y-4">
              <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
                Configuration
              </p>
              <InstanceFields ch={draft} onChange={updateField} />
            </div>
          )}
        </div>
      </div>

      <div className="shrink-0 border-t border-border px-6 py-3 flex items-center justify-between gap-3 bg-background">
        <Button onClick={() => onAdd(draft)} disabled={!canSubmit} loading={creating} size="sm">
          Add Channel
        </Button>
        <Button onClick={onCancel} variant="ghost" size="sm">
          {t("common.cancel")}
        </Button>
      </div>
    </div>
  );
}

// ─── PublicChannelDetail ──────────────────────────────────────────────────────

interface PublicChannelDetailProps {
  channel: Channel;
  identity: Identity | null;
  linked: boolean;
  generating: boolean;
  linkPlatform: string;
  linkCode: string;
  wxQrUrl: string;
  wxQrStatus: string;
  wxQrPolling: boolean;
  onGenerateCode: (platform: string) => void;
  onStartWeixinQR: () => void;
  onUnlink: (id: number | undefined) => void;
  onCopyLinkCode: () => void;
  wxQrStatusVariant: (status: string) => "warning" | "info" | "success" | "error" | "secondary";
  onRefreshWxQr: () => void;
}

function PublicChannelDetail({
  channel,
  identity,
  linked,
  generating,
  linkPlatform,
  linkCode,
  wxQrUrl,
  wxQrStatus,
  wxQrPolling,
  onGenerateCode,
  onStartWeixinQR,
  onUnlink,
  onCopyLinkCode,
  wxQrStatusVariant,
  onRefreshWxQr,
}: PublicChannelDetailProps) {
  const platformLabel = platformMeta[channel.type]?.label || channel.label || channel.type;

  return (
    <div className="flex flex-col h-full">
      <div className="flex-1 overflow-y-auto p-6 space-y-6">
        <div className="flex items-start justify-between gap-4">
          <div>
            <h2 className="font-serif text-xl tracking-tight">{platformLabel}</h2>
            <div className="flex items-center gap-2 mt-1">
              <Badge size="sm" variant={linked ? "success" : "secondary"}>
                {linked ? "linked" : "not linked"}
              </Badge>
            </div>
          </div>
        </div>

        {/* My account */}
        <div className="space-y-3">
          <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
            My account
          </p>
          {identity ? (
            <div className="space-y-2">
              <p className="text-xs text-muted-foreground">Linked identity</p>
              <p className="font-mono text-sm">
                {identity.name ? identity.name + " · " : ""}
                {identity.external_id}
              </p>
              <Button
                onClick={() => onUnlink(identity.id)}
                variant="ghost"
                size="sm"
                className="text-destructive-foreground"
              >
                Unlink
              </Button>
            </div>
          ) : (
            <div className="space-y-2">
              <p className="text-sm text-muted-foreground">
                {channel.type === "weixin"
                  ? "Link your Weixin account by scanning a QR code."
                  : `Link your ${platformLabel} account once to chat with Stella on this platform.`}
              </p>
              {channel.type !== "weixin" && (
                <Button
                  onClick={() => onGenerateCode(channel.type)}
                  disabled={generating}
                  loading={generating && linkPlatform === channel.type}
                  size="sm"
                >
                  Link {platformLabel}
                </Button>
              )}
              {channel.type === "weixin" && (
                <Button onClick={onStartWeixinQR} loading={wxQrPolling} size="sm">
                  Link Weixin
                </Button>
              )}
            </div>
          )}

          {/* Link code */}
          {linkCode && linkPlatform === channel.type && (
            <div className="rounded-lg border border-border bg-card p-4 space-y-2">
              <p className="text-sm font-medium">Send this command to Stella on {platformLabel}:</p>
              <div className="flex items-center gap-2 flex-wrap">
                <code className="font-mono text-lg font-bold bg-muted text-foreground px-3 py-1 rounded select-all">
                  /link {linkCode}
                </code>
                <Button onClick={onCopyLinkCode} variant="ghost" size="xs">
                  copy
                </Button>
              </div>
              <p className="text-xs text-muted-foreground">Expires in 5 minutes.</p>
            </div>
          )}

          {/* Weixin QR */}
          {wxQrUrl && channel.type === "weixin" && (
            <div className="rounded-xl border border-border bg-muted p-6 flex flex-col items-center">
              <p className="text-sm font-medium mb-2">Scan with WeChat to link your account</p>
              <img src={wxQrUrl} alt="WeChat QR Code" className="w-48 h-48 border rounded" />
              <Badge size="sm" variant={wxQrStatusVariant(wxQrStatus)} className="mt-2">
                {wxQrStatus}
              </Badge>
              {wxQrStatus === "expired" && (
                <Button onClick={onRefreshWxQr} variant="outline" size="xs" className="mt-1">
                  Refresh
                </Button>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

// ─── main page ────────────────────────────────────────────────────────────────

export function ChannelsPage() {
  const { t } = useI18n();
  const { data: me } = useQuery(meQueryOptions);
  const isAdmin = me?.is_admin ?? false;
  const [publicChannels, setPublicChannels] = useState<Channel[]>([]);
  const [linkedIdentities, setLinkedIdentities] = useState<Identity[]>([]);
  const [instances, setInstances] = useState<NormalizedChannel[]>([]);
  const [enabledChannelTypeIDs, setEnabledChannelTypeIDs] = useState<string[]>(
    channelTypes.map((t) => t.id),
  );
  const [loadingPlatforms, setLoadingPlatforms] = useState(false);
  const [loadingInstances, setLoadingInstances] = useState(false);

  // Selection state
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [creatingNew, setCreatingNew] = useState(false);
  // For non-admin: selected public channel type
  const [selectedPublicType, setSelectedPublicType] = useState<string | null>(null);

  const [linkCode, setLinkCode] = useState("");
  const [linkPlatform, setLinkPlatform] = useState("");
  const [generating, setGenerating] = useState(false);

  const [wxQrUrl, setWxQrUrl] = useState("");
  const [wxQrStatus, setWxQrStatus] = useState("");
  const wxQrCodeRef = useRef("");
  const [wxQrPolling, setWxQrPolling] = useState(false);
  const wxQrIntervalRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const [creatingInstance, setCreatingInstance] = useState(false);

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

  const identityFor = useCallback(
    (platform: string): Identity | null =>
      linkedIdentities.find((i) => i.platform === platform) || null,
    [linkedIdentities],
  );

  const isLinked = useCallback((platform: string) => Boolean(identityFor(platform)), [identityFor]);

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
          .filter((ch) => currentEnabledIDs.includes(ch.type))
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
      if (isAdmin) {
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
    void init();
    return () => {
      if (toastTimerRef.current) clearTimeout(toastTimerRef.current);
      if (wxQrIntervalRef.current) clearInterval(wxQrIntervalRef.current);
    };
  }, [isAdmin, loadChannelPlugins, loadPublicChannels, loadIdentities, loadInstances]); // eslint-disable-line react-hooks/exhaustive-deps

  // Clear selection if selected instance is removed
  useEffect(() => {
    if (selectedId && !instances.find((ch) => ch.id === selectedId)) {
      setSelectedId(null);
    }
  }, [instances, selectedId]);

  // ── link code ──

  const generateCode = async (platform: string) => {
    setGenerating(true);
    setLinkPlatform(platform);
    setLinkCode("");
    setWxQrUrl("");
    setWxQrStatus("");
    wxQrCodeRef.current = "";
    try {
      const result = await api<{ code: string }>("POST", "/api/auth/profile/link-code", {
        platform,
      });
      setLinkCode(result.code);
    } catch (e) {
      showToast((e as Error).message, "error");
    } finally {
      setGenerating(false);
    }
  };

  const copyLinkCode = () => {
    void navigator.clipboard.writeText("/link " + linkCode);
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

  const updateInstance = (id: string, key: string, value: unknown) => {
    setInstances((prev) => prev.map((ch) => (ch.id === id ? { ...ch, [key]: value } : ch)));
  };

  const saveInstance = async (ch: NormalizedChannel) => {
    try {
      const saved = await api<Channel>("PUT", "/api/channels/" + encodeURIComponent(ch.id), {
        type: ch.type,
        agent_id: ch.agent_id || "",
        config: channelConfig(ch),
      });
      const normalized = normalizeChannel(saved);
      setInstances((prev) => prev.map((c) => (c.id === ch.id ? normalized : c)));
      showToast(ch.id + " saved");
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  };

  const doDeleteChannel = async (id: string) => {
    const ch = instances.find((c) => c.id === id);
    if (ch && ch.id === ch.type) {
      showToast("Default platform channels cannot be deleted", "error");
      return;
    }
    try {
      await api("DELETE", "/api/channels/" + encodeURIComponent(id));
      setSelectedId(null);
      await loadInstances(enabledChannelTypeIDs);
      showToast(id + " deleted");
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  };

  const createChannel = async (draft: Record<string, unknown>) => {
    const id = ((draft.id as string) || "").trim();
    if (!id || !draft.type) {
      showToast("ID and platform are required", "error");
      return;
    }
    if (id === draft.type) {
      showToast("Dedicated instance ID must not match the platform ID", "error");
      return;
    }
    setCreatingInstance(true);
    try {
      const saved = await api<Channel>("POST", "/api/channels", {
        id,
        type: draft.type,
        agent_id: "",
        config: channelConfig(draft),
      });
      setCreatingNew(false);
      await loadInstances(enabledChannelTypeIDs);
      setSelectedId(saved.id);
      showToast(saved.id + " created");
    } catch (e) {
      showToast((e as Error).message, "error");
    } finally {
      setCreatingInstance(false);
    }
  };

  // ── render ──

  const isLoading = loadingPlatforms || (isAdmin && loadingInstances);

  const wxQrStatusVariant = (
    status: string,
  ): "warning" | "info" | "success" | "error" | "secondary" => {
    if (status === "waiting") return "warning";
    if (status === "scaned") return "info";
    if (status === "confirmed") return "success";
    if (status === "expired") return "error";
    return "secondary";
  };

  // ── admin view ──

  if (isAdmin) {
    const selectedChannel = selectedId ? instances.find((ch) => ch.id === selectedId) : null;

    const listHeader = (
      <div className="flex items-center justify-between px-3 py-3 border-b border-border">
        <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
          Channels
        </span>
        <Button
          onClick={() => {
            setCreatingNew(true);
            setSelectedId(null);
          }}
          variant="ghost"
          size="xs"
        >
          New Channel
        </Button>
      </div>
    );

    const list = isLoading ? (
      <div className="flex justify-center py-8">
        <Spinner className="size-4" />
      </div>
    ) : (
      <div>
        {instances.map((ch) => {
          const isSelected = !creatingNew && selectedId === ch.id;
          const platformLabel = platformMeta[ch.type]?.label || ch.type;
          return (
            <button
              key={ch.id}
              onClick={() => {
                setSelectedId(ch.id);
                setCreatingNew(false);
              }}
              className={`w-full text-left px-3 py-2.5 flex items-center gap-2 hover:bg-muted/50 transition-colors ${
                isSelected ? "bg-primary/8" : ""
              }`}
            >
              {platformMeta[ch.type]?.icon ? (
                <BrandIcon
                  path={platformMeta[ch.type].icon!}
                  className="size-4 shrink-0 text-muted-foreground"
                />
              ) : (
                <span
                  className={`shrink-0 w-1.5 h-1.5 rounded-full ${
                    ch.enabled ? "bg-green-500" : "bg-muted-foreground/40"
                  }`}
                />
              )}
              <div className="min-w-0 flex-1">
                <p className="text-sm font-medium leading-tight truncate">{platformLabel}</p>
                <p className="text-[11px] font-mono text-muted-foreground truncate">{ch.id}</p>
              </div>
            </button>
          );
        })}
      </div>
    );

    let detail: React.ReactNode = undefined;
    if (creatingNew) {
      detail = (
        <NewChannelForm
          enabledChannelTypeIDs={enabledChannelTypeIDs}
          fallbackChannelType={fallbackChannelType}
          onAdd={createChannel}
          onCancel={() => setCreatingNew(false)}
          creating={creatingInstance}
        />
      );
    } else if (selectedChannel) {
      detail = (
        <ChannelDetail
          key={selectedChannel.id}
          channel={selectedChannel}
          identity={identityFor(selectedChannel.type)}
          generating={generating}
          linkPlatform={linkPlatform}
          linkCode={linkCode}
          wxQrUrl={wxQrUrl}
          wxQrStatus={wxQrStatus}
          wxQrPolling={wxQrPolling}
          onUpdate={(key, value) => updateInstance(selectedChannel.id, key, value)}
          onSave={saveInstance}
          onDelete={doDeleteChannel}
          onGenerateCode={generateCode}
          onStartWeixinQR={startWeixinQR}
          onUnlink={unlinkIdentity}
          onCopyLinkCode={copyLinkCode}
          wxQrStatusVariant={wxQrStatusVariant}
          onRefreshWxQr={startWeixinQR}
        />
      );
    }

    const emptyState = (
      <p className="text-sm text-muted-foreground">
        No channels configured. Add one to connect a messaging platform.
      </p>
    );

    return (
      <div className="h-full">
        <SettingsDetailLayout
          listHeader={listHeader}
          list={list}
          detail={detail}
          emptyState={instances.length === 0 && !creatingNew ? emptyState : undefined}
        />
        {/* Toast */}
        {toast && (
          <div className="fixed top-2 left-1/2 -translate-x-1/2 z-50">
            <div
              className={`rounded-lg border px-4 py-2 text-sm font-mono shadow-lg ${
                toast.kind === "error"
                  ? "border-destructive/50 bg-destructive/10 text-destructive-foreground"
                  : "border-success/50 bg-success/10 text-success-foreground"
              }`}
            >
              {toast.message}
            </div>
          </div>
        )}
      </div>
    );
  }

  // ── non-admin view ──

  const selectedPublicChannel = selectedPublicType
    ? publicChannels.find((ch) => ch.type === selectedPublicType)
    : null;

  const listHeader = (
    <div className="flex items-center justify-between px-3 py-3 border-b border-border">
      <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
        Channels
      </span>
    </div>
  );

  const list = isLoading ? (
    <div className="flex justify-center py-8">
      <Spinner className="size-4" />
    </div>
  ) : (
    <div>
      {publicChannels.map((ch) => {
        const isSelected = selectedPublicType === ch.type;
        const linked = isLinked(ch.type);
        const platformLabel = platformMeta[ch.type]?.label || ch.label || ch.type;
        return (
          <button
            key={ch.type}
            onClick={() => setSelectedPublicType(ch.type)}
            className={`w-full text-left px-3 py-2.5 flex items-center gap-2 hover:bg-muted/50 transition-colors ${
              isSelected ? "bg-primary/8" : ""
            }`}
          >
            {platformMeta[ch.type]?.icon ? (
              <BrandIcon
                path={platformMeta[ch.type].icon!}
                className="size-4 shrink-0 text-muted-foreground"
              />
            ) : (
              <span
                className={`shrink-0 w-1.5 h-1.5 rounded-full ${
                  linked ? "bg-green-500" : "bg-muted-foreground/40"
                }`}
              />
            )}
            <div className="min-w-0 flex-1">
              <p className="text-sm font-medium leading-tight truncate">{platformLabel}</p>
              {linked && (
                <p className="text-[11px] font-mono text-muted-foreground truncate">linked</p>
              )}
            </div>
          </button>
        );
      })}
    </div>
  );

  let detail: React.ReactNode = undefined;
  if (selectedPublicChannel) {
    detail = (
      <PublicChannelDetail
        key={selectedPublicChannel.type}
        channel={selectedPublicChannel}
        identity={identityFor(selectedPublicChannel.type)}
        linked={isLinked(selectedPublicChannel.type)}
        generating={generating}
        linkPlatform={linkPlatform}
        linkCode={linkCode}
        wxQrUrl={wxQrUrl}
        wxQrStatus={wxQrStatus}
        wxQrPolling={wxQrPolling}
        onGenerateCode={generateCode}
        onStartWeixinQR={startWeixinQR}
        onUnlink={unlinkIdentity}
        onCopyLinkCode={copyLinkCode}
        wxQrStatusVariant={wxQrStatusVariant}
        onRefreshWxQr={startWeixinQR}
      />
    );
  }

  const emptyState = (
    <p className="text-sm text-muted-foreground">
      {publicChannels.length === 0
        ? "No channels available. An admin needs to enable channel plugins."
        : t("channels.title")}
    </p>
  );

  return (
    <div className="h-full">
      <SettingsDetailLayout
        listHeader={listHeader}
        list={list}
        detail={detail}
        emptyState={!selectedPublicChannel ? emptyState : undefined}
      />
      {/* Toast */}
      {toast && (
        <div className="fixed top-2 left-1/2 -translate-x-1/2 z-50">
          <div
            className={`rounded-lg border px-4 py-2 text-sm font-mono shadow-lg ${
              toast.kind === "error"
                ? "border-destructive/50 bg-destructive/10 text-destructive-foreground"
                : "border-success/50 bg-success/10 text-success-foreground"
            }`}
          >
            {toast.message}
          </div>
        </div>
      )}
    </div>
  );
}
