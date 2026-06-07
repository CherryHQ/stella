import { useCallback, useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { meQueryOptions } from "@/lib/queries/me";
import QRCode from "qrcode";
import {
  createChannel as createChannelRequest,
  deleteChannel,
  generateLinkCode,
  listChannels,
  listProfileIdentities,
  listPublicChannels,
  pollWeixinQrStatus,
  startWeixinQr,
  unlinkProfileIdentity,
  updateChannel,
} from "@/lib/api-client/sdk.gen";
import type { ComponentsPublicChannel } from "@/lib/api-client/types.gen";
import type { Channel, Identity } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { Spinner } from "@/components/ui/spinner";
import { siTelegram, siQq, siWechat } from "simple-icons";
import { useI18n } from "@/lib/i18n";
import { useToast, ToastContainer } from "@/hooks/use-toast";
import { SettingsPageHeader } from "@/features/settings/SettingsPageHeader";
import { SettingsEmptyState } from "@/features/settings/SettingsEmptyState";
import {
  DetailPanel,
  DetailPanelHeader,
  FormSectionTitle,
} from "@/features/settings/SettingsDetailPanel";
import { ConfirmDialog } from "@/features/settings/ConfirmDialog";
import { ArrowLeft, Plus } from "lucide-react";

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
  name: string;
  type: string;
  label?: string;
  agent_id: string;
  agent_name?: string;
  enabled: boolean;
}

function normalizeChannel(ch: Channel): NormalizedChannel {
  const type = ch.type || ch.id;
  return {
    ...ch,
    name: ch.name || "",
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
  onUnlink: (id: string | undefined) => void;
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
    <DetailPanel
      onSave={() => onSave(channel)}
      onDelete={!isDefaultInstance ? () => setConfirmDeleteOpen(true) : undefined}
      saveLabel={t("common.save")}
      deleteLabel={t("common.delete")}
    >
      <DetailPanelHeader
        title={
          <span className="flex items-center gap-2">
            {platformMeta[channel.type]?.icon && (
              <BrandIcon path={platformMeta[channel.type].icon!} className="size-5 shrink-0" />
            )}
            {channel.name || platformLabel}
          </span>
        }
        subtitle={<p className="text-xs font-mono text-muted-foreground">{channel.type}</p>}
        action={
          <>
            <Switch
              checked={Boolean(channel.enabled)}
              onCheckedChange={(checked) => updateField("enabled", checked)}
            />
            <span className="text-sm">Enabled</span>
          </>
        }
      />

      <div className="space-y-1.5">
        <label className="text-sm font-medium">Name</label>
        <Input
          nativeInput
          type="text"
          value={(channel.name as string) || ""}
          onChange={(e) => updateField("name", (e.target as HTMLInputElement).value)}
          placeholder={platformLabel}
          className="w-full text-sm"
        />
      </div>

      {/* Config section */}
      {Object.keys(platformConfigDefaults(channel.type)).length > 0 && (
        <div className="space-y-4">
          <FormSectionTitle>Configuration</FormSectionTitle>
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
        <FormSectionTitle>My account</FormSectionTitle>
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

      <ConfirmDialog
        open={confirmDeleteOpen}
        onOpenChange={setConfirmDeleteOpen}
        title="Delete channel"
        message={`Delete channel ${channel.id}?`}
        onConfirm={() => onDelete(channel.id)}
      />
    </DetailPanel>
  );
}

// ─── NewChannelForm ───────────────────────────────────────────────────────────

interface NewChannelFormProps {
  fallbackChannelType: string;
  onAdd: (channel: Record<string, unknown>) => Promise<void>;
  onCancel: () => void;
  creating: boolean;
}

function NewChannelForm({ fallbackChannelType, onAdd, onCancel, creating }: NewChannelFormProps) {
  const { t } = useI18n();
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
    <DetailPanel
      onSave={() => onAdd(draft)}
      onCancel={onCancel}
      saveLabel="Add Channel"
      cancelLabel={t("common.cancel")}
      isSaving={creating}
      isSavingLabel="Adding..."
      canSave={canSubmit}
    >
      <DetailPanelHeader
        title="New Channel"
        subtitle={
          <p className="text-sm text-muted-foreground">Connect Stella to a messaging platform.</p>
        }
      />

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
            {channelTypes.map((ct) => (
              <option key={ct.id} value={ct.id}>
                {ct.label}
              </option>
            ))}
          </select>
        </div>

        <div className="w-full space-y-1.5">
          <label className="text-sm font-medium">Name</label>
          <Input
            nativeInput
            type="text"
            value={(draft.name as string) || ""}
            onChange={(e) => updateField("name", e.target.value)}
            placeholder="e.g. Feishu Coder"
            className="w-full text-sm"
          />
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
            Must not match the platform ID (e.g. not &quot;telegram&quot; for a Telegram channel).
          </p>
        </div>

        {!!draft.type && (
          <div className="space-y-4">
            <FormSectionTitle>Configuration</FormSectionTitle>
            <InstanceFields ch={draft} onChange={updateField} />
          </div>
        )}
      </div>
    </DetailPanel>
  );
}

// ─── PublicChannelDetail ──────────────────────────────────────────────────────

interface PublicChannelDetailProps {
  channel: ComponentsPublicChannel;
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
  onUnlink: (id: string | undefined) => void;
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
    <DetailPanel>
      <DetailPanelHeader
        title={platformLabel}
        subtitle={
          <Badge size="sm" variant={linked ? "success" : "secondary"}>
            {linked ? "linked" : "not linked"}
          </Badge>
        }
      />

      {/* My account */}
      <div className="space-y-3">
        <FormSectionTitle>My account</FormSectionTitle>
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
    </DetailPanel>
  );
}

// ─── main page ────────────────────────────────────────────────────────────────

export function ChannelsPage() {
  const { data: me } = useQuery(meQueryOptions);
  const isAdmin = me?.is_admin ?? false;
  const [publicChannels, setPublicChannels] = useState<ComponentsPublicChannel[]>([]);
  const [linkedIdentities, setLinkedIdentities] = useState<Identity[]>([]);
  const [instances, setInstances] = useState<NormalizedChannel[]>([]);
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

  const { toasts, showToast } = useToast();

  // ── helpers ──

  const fallbackChannelType = defaultChannelType;

  const identityFor = useCallback(
    (platform: string): Identity | null =>
      linkedIdentities.find((i) => i.platform === platform) || null,
    [linkedIdentities],
  );

  const isLinked = useCallback((platform: string) => Boolean(identityFor(platform)), [identityFor]);

  // ── data loading ──

  const loadIdentities = useCallback(async () => {
    try {
      const { data } = await listProfileIdentities({ throwOnError: true });
      setLinkedIdentities((data?.identities as Identity[]) ?? []);
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }, [showToast]);

  const loadPublicChannels = useCallback(async () => {
    try {
      const { data } = await listPublicChannels({ throwOnError: true });
      setPublicChannels(data?.channels ?? []);
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }, [showToast]);

  const loadInstances = useCallback(async () => {
    setLoadingInstances(true);
    try {
      const { data } = await listChannels({ throwOnError: true });
      const channels = data?.channels ?? [];
      const normalized = (channels || []).map(normalizeChannel).sort((a, b) => {
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
  }, [showToast]);

  // ── init ──

  useEffect(() => {
    if (isAdmin) {
      void Promise.all([loadPublicChannels(), loadIdentities(), loadInstances()]);
    } else {
      void Promise.all([loadPublicChannels(), loadIdentities()]);
    }
    return () => {
      if (wxQrIntervalRef.current) clearInterval(wxQrIntervalRef.current);
    };
  }, [isAdmin, loadPublicChannels, loadIdentities, loadInstances]);

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
      const { data: result } = await generateLinkCode({
        body: { platform },
        throwOnError: true,
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
      const { data: result } = await pollWeixinQrStatus({
        query: { qrcode: qrCode },
        throwOnError: true,
      });
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
      const { data: result } = await startWeixinQr({ throwOnError: true });
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

  const unlinkIdentity = async (id: string | undefined) => {
    if (!id || !confirm("Unlink this identity?")) return;
    try {
      await unlinkProfileIdentity({ path: { id: String(id) }, throwOnError: true });
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
      const { data: saved } = await updateChannel({
        path: { id: ch.id },
        body: {
          name: ch.name || "",
          type: ch.type,
          agent_id: ch.agent_id || "",
          enabled: ch.enabled,
          config: channelConfig(ch),
        },
        throwOnError: true,
      });
      const normalized = normalizeChannel(saved as Channel);
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
      await deleteChannel({ path: { id: String(id) }, throwOnError: true });
      setSelectedId(null);
      await loadInstances();
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
      const { data: saved } = await createChannelRequest({
        body: {
          id,
          name: (draft.name as string) || "",
          type: draft.type as string,
          agent_id: "",
          config: channelConfig(draft),
        },
        throwOnError: true,
      });
      setCreatingNew(false);
      await loadInstances();
      setSelectedId(saved.id);
      showToast(saved.id + " created");
    } catch (e) {
      showToast((e as Error).message, "error");
    } finally {
      setCreatingInstance(false);
    }
  };

  // ── render ──

  const isLoading = isAdmin && loadingInstances;

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

    // Grouping computation for admin view
    const grouped = instances.reduce<Record<string, NormalizedChannel[]>>((acc, inst) => {
      const type = inst.type || "other";
      if (!acc[type]) acc[type] = [];
      acc[type].push(inst);
      return acc;
    }, {});

    const groupedPlatforms = Object.entries(grouped)
      .map(([type, platformInstances]) => {
        const meta = platformMeta[type];
        return {
          type,
          label: meta?.label || type,
          icon: meta?.icon,
          instances: platformInstances,
        };
      })
      .sort((a, b) => a.label.localeCompare(b.label));

    let detail: React.ReactNode = undefined;
    if (creatingNew) {
      detail = (
        <NewChannelForm
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

    const hasActiveEditor = creatingNew || !!selectedChannel;

    return (
      <div className="h-full overflow-y-auto bg-background">
        <div className="mx-auto max-w-3xl p-6 sm:p-8 lg:p-10">
          {hasActiveEditor ? (
            <div className="space-y-4">
              <button
                onClick={() => {
                  setSelectedId(null);
                  setCreatingNew(false);
                }}
                className="group inline-flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors font-medium cursor-pointer"
              >
                <ArrowLeft className="size-3.5 transition-transform group-hover:-translate-x-0.5" />
                Back to Channels
              </button>
              <div className="bg-card border border-border/40 rounded-2xl overflow-hidden shadow-sm">
                {detail}
              </div>
            </div>
          ) : (
            <div className="space-y-8">
              <SettingsPageHeader
                title="Channels"
                description="Configure messaging channel integrations for Stella."
                action={
                  <Button
                    onClick={() => {
                      setCreatingNew(true);
                      setSelectedId(null);
                    }}
                    variant="premium"
                    size="sm"
                    className="group flex items-center gap-1.5"
                  >
                    <Plus className="size-4 shrink-0 group-hover:rotate-90 transition-transform duration-150" />
                    Add channel
                  </Button>
                }
              />

              {isLoading ? (
                <div className="flex justify-center py-8">
                  <Spinner className="size-4" />
                </div>
              ) : instances.length === 0 ? (
                <SettingsEmptyState
                  message="No channels configured"
                  description="Add a channel to connect your messaging platform."
                  action={
                    <Button
                      onClick={() => {
                        setCreatingNew(true);
                        setSelectedId(null);
                      }}
                      variant="premium-outline"
                      size="sm"
                      className="group flex items-center gap-1.5"
                    >
                      <Plus className="size-4 shrink-0 group-hover:rotate-90 transition-transform duration-150" />
                      Add channel
                    </Button>
                  }
                />
              ) : (
                <div className="space-y-8">
                  {groupedPlatforms.map((platform) => (
                    <div key={platform.type} className="space-y-4">
                      <div className="flex items-center gap-2 border-b border-border/40 pb-2">
                        {platform.icon ? (
                          <BrandIcon
                            path={platform.icon}
                            className="size-4 shrink-0 text-muted-foreground"
                          />
                        ) : (
                          <span className="shrink-0 w-1.5 h-1.5 rounded-full bg-muted-foreground/45" />
                        )}
                        <h4 className="text-xs font-semibold text-muted-foreground">
                          {platform.label}
                        </h4>
                        <Badge variant="secondary" className="text-[10px] py-0 px-1.5 rounded-md">
                          {platform.instances.length}
                        </Badge>
                      </div>
                      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                        {platform.instances.map((ch) => {
                          const platformLabel = platformMeta[ch.type]?.label || ch.type;
                          const iconPath = platformMeta[ch.type]?.icon;
                          return (
                            <div
                              key={ch.id}
                              onClick={() => {
                                setSelectedId(ch.id);
                                setCreatingNew(false);
                              }}
                              className="group relative flex flex-col justify-between rounded-2xl border border-border/40 bg-card p-5 transition-all hover:border-border/80 hover:shadow-sm cursor-pointer"
                            >
                              <div className="space-y-3">
                                <div className="flex items-center justify-between gap-3">
                                  <div className="flex items-center gap-2.5 min-w-0">
                                    {iconPath ? (
                                      <BrandIcon
                                        path={iconPath}
                                        className="size-4 shrink-0 text-muted-foreground"
                                      />
                                    ) : (
                                      <span
                                        className={`shrink-0 w-1.5 h-1.5 rounded-full ${
                                          ch.enabled ? "bg-green-500" : "bg-muted-foreground/40"
                                        }`}
                                      />
                                    )}
                                    <h3 className="text-sm font-semibold text-foreground truncate">
                                      {ch.name || platformLabel}
                                    </h3>
                                  </div>
                                  <Badge
                                    variant="outline"
                                    className="font-mono text-[10px] uppercase shrink-0"
                                  >
                                    {ch.type}
                                  </Badge>
                                </div>
                              </div>
                              <div className="mt-4 flex items-center justify-between">
                                <div className="flex items-center gap-1.5">
                                  <span
                                    className={`shrink-0 w-1.5 h-1.5 rounded-full ${
                                      ch.enabled ? "bg-green-500" : "bg-muted-foreground/40"
                                    }`}
                                  />
                                  <span className="text-xs text-muted-foreground">
                                    {ch.enabled ? "Active" : "Disabled"}
                                  </span>
                                </div>
                                <span className="text-xs font-medium text-primary opacity-0 group-hover:opacity-100 transition-opacity">
                                  Configure →
                                </span>
                              </div>
                            </div>
                          );
                        })}
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </div>
          )}
        </div>
        <ToastContainer messages={toasts} />
      </div>
    );
  }

  // ── non-admin view ──

  const selectedPublicChannel = selectedPublicType
    ? publicChannels.find((ch) => ch.type === selectedPublicType)
    : null;

  // Grouping computation for non-admin view
  const groupedPublic = publicChannels.reduce<Record<string, ComponentsPublicChannel[]>>(
    (acc, ch) => {
      const type = ch.type || "other";
      if (!acc[type]) acc[type] = [];
      acc[type].push(ch);
      return acc;
    },
    {},
  );

  const groupedPublicPlatforms = Object.entries(groupedPublic)
    .map(([type, platformChannels]) => {
      const meta = platformMeta[type];
      return {
        type,
        label: meta?.label || type,
        icon: meta?.icon,
        channels: platformChannels,
      };
    })
    .sort((a, b) => a.label.localeCompare(b.label));

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

  const hasActiveEditor = !!selectedPublicChannel;

  return (
    <div className="h-full overflow-y-auto bg-background">
      <div className="mx-auto max-w-3xl p-6 sm:p-8 lg:p-10">
        {hasActiveEditor ? (
          <div className="space-y-4">
            <button
              onClick={() => {
                setSelectedPublicType(null);
              }}
              className="group inline-flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors font-medium cursor-pointer"
            >
              <ArrowLeft className="size-3.5 transition-transform group-hover:-translate-x-0.5" />
              Back to Channels
            </button>
            <div className="bg-card border border-border/40 rounded-2xl overflow-hidden shadow-sm">
              {detail}
            </div>
          </div>
        ) : (
          <div className="space-y-8">
            <SettingsPageHeader
              title="Channels"
              description="Link your third-party messaging platform accounts to Stella."
            />

            {isLoading ? (
              <div className="flex justify-center py-8">
                <Spinner className="size-4" />
              </div>
            ) : publicChannels.length === 0 ? (
              <SettingsEmptyState
                message="No channels available"
                description="An admin needs to enable channel plugins first."
              />
            ) : (
              <div className="space-y-8">
                {groupedPublicPlatforms.map((platform) => (
                  <div key={platform.type} className="space-y-4">
                    <div className="flex items-center gap-2 border-b border-border/40 pb-2">
                      {platform.icon ? (
                        <BrandIcon
                          path={platform.icon}
                          className="size-4 shrink-0 text-muted-foreground"
                        />
                      ) : (
                        <span className="shrink-0 w-1.5 h-1.5 rounded-full bg-muted-foreground/45" />
                      )}
                      <h4 className="text-xs font-semibold text-muted-foreground">
                        {platform.label}
                      </h4>
                      <Badge variant="secondary" className="text-[10px] py-0 px-1.5 rounded-md">
                        {platform.channels.length}
                      </Badge>
                    </div>
                    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                      {platform.channels.map((ch) => {
                        const linked = isLinked(ch.type);
                        const platformLabel = platformMeta[ch.type]?.label || ch.label || ch.type;
                        const iconPath = platformMeta[ch.type]?.icon;
                        return (
                          <div
                            key={ch.type}
                            onClick={() => {
                              setSelectedPublicType(ch.type);
                            }}
                            className="group relative flex flex-col justify-between rounded-2xl border border-border/40 bg-card p-5 transition-all hover:border-border/80 hover:shadow-sm cursor-pointer"
                          >
                            <div className="space-y-3">
                              <div className="flex items-center justify-between gap-3">
                                <div className="flex items-center gap-2.5 min-w-0">
                                  {iconPath ? (
                                    <BrandIcon
                                      path={iconPath}
                                      className="size-4 shrink-0 text-muted-foreground"
                                    />
                                  ) : (
                                    <span
                                      className={`shrink-0 w-1.5 h-1.5 rounded-full ${
                                        linked ? "bg-green-500" : "bg-muted-foreground/40"
                                      }`}
                                    />
                                  )}
                                  <h3 className="text-sm font-semibold text-foreground truncate">
                                    {platformLabel}
                                  </h3>
                                </div>
                              </div>
                            </div>
                            <div className="mt-4 flex items-center justify-between">
                              <div className="flex items-center gap-1.5">
                                <span
                                  className={`shrink-0 w-1.5 h-1.5 rounded-full ${
                                    linked ? "bg-green-500" : "bg-muted-foreground/40"
                                  }`}
                                />
                                <span className="text-xs text-muted-foreground">
                                  {linked ? "Linked" : "Not linked"}
                                </span>
                              </div>
                              <span className="text-xs font-medium text-primary opacity-0 group-hover:opacity-100 transition-opacity">
                                Link Account →
                              </span>
                            </div>
                          </div>
                        );
                      })}
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        )}
      </div>
      <ToastContainer messages={toasts} />
    </div>
  );
}
