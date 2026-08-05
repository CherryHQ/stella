import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { meQueryOptions } from "@/lib/queries/me";
import QRCode from "qrcode";
import {
  beginFeishuRegistration,
  beginWeixinRegistration,
  createChannel as createChannelRequest,
  deleteChannel,
  listAgents,
  listChannels,
  listProfileIdentities,
  listPublicChannels,
  pollFeishuRegistration,
  pollWeixinRegistration,
  unlinkProfileIdentity,
  updateChannel,
} from "@/lib/api-client/sdk.gen";
import type { ComponentsPublicChannel } from "@/lib/api-client/types.gen";
import type { Agent, Channel, Identity } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Spinner } from "@/components/ui/spinner";
import {
  Dialog,
  DialogClose,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogPopup,
  DialogTitle,
} from "@/components/ui/dialog";
import { useI18n } from "@/lib/i18n";
import { useToast, ToastContainer } from "@/hooks/use-toast";
import {
  DetailPanel,
  DetailPanelHeader,
  FormSectionTitle,
} from "@/features/settings/SettingsDetailPanel";
import {
  SettingsCard,
  SettingsCardSection,
  SettingsDetailSheet,
  SettingsGridPage,
} from "@/features/settings/SettingsCardGrid";
import { ConfirmDialog } from "@/features/settings/ConfirmDialog";
import { Plus } from "lucide-react";
import { PlatformIcon, platformLabel } from "@/components/PlatformIcon";
import {
  ChannelConfigFields,
  ChannelFields,
  channelConfig,
  channelTypes,
  defaultChannelType,
  hasConfig,
  newInstanceDraft,
  normalizeChannel,
  serializePlatformConfig,
  type NormalizedChannel,
} from "./ChannelFields";
import { useAccountLink, weixinQrStatusVariant } from "./use-account-link";

// ─── sub-components ───────────────────────────────────────────────────────────

const selectClassName =
  "h-9 w-full rounded-lg border border-input bg-background px-3 text-sm outline-none sm:h-8 sm:text-sm";

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
  const label = platformLabel(channel.type);

  return (
    <DetailPanel
      onSave={() => onSave(channel)}
      onDelete={!isDefaultInstance ? () => setConfirmDeleteOpen(true) : undefined}
      saveLabel={t("common.save")}
      deleteLabel={t("common.delete")}
    >
      <DetailPanelHeader
        title={channel.name || label}
        subtitle={<p className="text-xs font-mono text-muted-foreground">{channel.type}</p>}
      />

      <ChannelFields channel={channel} onChange={updateField} />
      {hasConfig(channel.type, channel) && (
        <p className="text-xs text-muted-foreground">{t("channels.configOnlyNote")}</p>
      )}

      {/* Identity / account section. */}
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
                Link {label}
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
            <p className="text-sm font-medium">Send this command to Stella on {label}:</p>
            <div className="flex items-center gap-2 flex-wrap">
              <code className="font-mono text-lg font-semibold bg-muted text-foreground px-3 py-1 rounded select-all">
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
        title={t("channels.deleteChannel")}
        message={t("channels.deleteChannelMsg", { id: channel.id })}
        onConfirm={() => onDelete(channel.id)}
      />
    </DetailPanel>
  );
}

// ─── NewChannelForm ───────────────────────────────────────────────────────────

interface NewChannelFormProps {
  fallbackChannelType: string;
  agents: Agent[];
  channels: NormalizedChannel[];
  onAdd: (channel: Record<string, unknown>) => Promise<void>;
  onRegistered: (channel: NormalizedChannel) => Promise<void>;
  onCancel: () => void;
  creating: boolean;
}

function NewChannelForm({
  fallbackChannelType,
  agents,
  channels,
  onAdd,
  onRegistered,
  onCancel,
  creating,
}: NewChannelFormProps) {
  const { t } = useI18n();
  const [draft, setDraft] = useState<Record<string, unknown>>(
    newInstanceDraft(fallbackChannelType, ""),
  );
  const [scanOpen, setScanOpen] = useState(false);
  const [scanQrUrl, setScanQrUrl] = useState("");
  const [scanStatus, setScanStatus] = useState("");
  const [scanError, setScanError] = useState("");
  const [scanning, setScanning] = useState(false);
  const scanIntervalRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const weixinScanRefreshesRef = useRef(0);

  const stopScanPolling = useCallback(() => {
    if (scanIntervalRef.current) {
      clearInterval(scanIntervalRef.current);
      scanIntervalRef.current = null;
    }
    setScanning(false);
  }, []);

  useEffect(
    () => () => {
      if (scanIntervalRef.current) clearInterval(scanIntervalRef.current);
    },
    [],
  );

  const updateField = (key: string, value: unknown) => {
    if (key === "type") {
      setDraft(newInstanceDraft(value as string, draft.id as string));
    } else if (key !== "id" || draft.type !== "weixin") {
      setDraft((prev) => ({ ...prev, [key]: value }));
    }
  };

  const pollFeishuScan = useCallback(
    async (deviceCode: string, intervalSeconds: number) => {
      const channelId = ((draft.id as string) || "").trim();
      const agentId = ((draft.agent_id as string) || "").trim();
      if (!deviceCode || !channelId || !agentId) return;
      try {
        const { data: result } = await pollFeishuRegistration({
          body: {
            device_code: deviceCode,
            channel_id: channelId,
            agent_id: agentId,
            name: (draft.name as string) || "Feishu",
            enabled: true,
            config: serializePlatformConfig("feishu", draft),
          },
          throwOnError: true,
        });
        setScanStatus(result.status);
        if (result.status === "slow_down") {
          stopScanPolling();
          scanIntervalRef.current = setInterval(
            () => void pollFeishuScan(deviceCode, result.interval || intervalSeconds + 5),
            (result.interval || intervalSeconds + 5) * 1000,
          );
          setScanning(true);
        }
        if (result.status === "created" && result.channel) {
          stopScanPolling();
          setScanOpen(false);
          await onRegistered(normalizeChannel(result.channel as Channel));
        }
      } catch (e) {
        stopScanPolling();
        setScanError((e as Error).message);
      }
    },
    [draft, onRegistered, stopScanPolling],
  );

  async function pollWeixinScan(qrCode: string) {
    const channelId = ((draft.id as string) || "").trim();
    const agentId = ((draft.agent_id as string) || "").trim();
    if (!qrCode || !channelId || !agentId) return;
    try {
      const { data: result } = await pollWeixinRegistration({
        body: {
          qrcode: qrCode,
          channel_id: channelId,
          agent_id: agentId,
          name: (draft.name as string) || "WeChat",
          config: serializePlatformConfig("weixin", draft),
        },
        throwOnError: true,
      });
      setScanStatus(result.status);
      if (result.status === "expired") {
        stopScanPolling();
        if (weixinScanRefreshesRef.current < 3) {
          weixinScanRefreshesRef.current += 1;
          await beginWeixinScanPolling();
        } else {
          setScanError(t("channels.scanExpired"));
        }
      }
      if (result.status === "created" && result.channel) {
        stopScanPolling();
        setScanOpen(false);
        await onRegistered(normalizeChannel(result.channel as Channel));
      }
    } catch (e) {
      stopScanPolling();
      setScanError((e as Error).message);
    }
  }

  async function beginWeixinScanPolling() {
    setScanQrUrl("");
    setScanStatus("waiting");
    setScanError("");
    stopScanPolling();
    setScanning(true);
    try {
      const { data: result } = await beginWeixinRegistration({
        throwOnError: true,
      });
      setScanQrUrl(await QRCode.toDataURL(result.qr_image_url, { width: 256, margin: 2 }));
      const intervalSeconds = result.poll_interval || 2;
      scanIntervalRef.current = setInterval(
        () => void pollWeixinScan(result.qrcode),
        intervalSeconds * 1000,
      );
      setScanning(true);
    } catch (e) {
      setScanError((e as Error).message);
      setScanning(false);
    }
  }

  const startFeishuScan = async () => {
    const channelName = ((draft.name as string) || "").trim();
    const channelId = ((draft.id as string) || "").trim();
    if (!channelName) {
      setScanError(t("channels.scanNeedsName"));
      setScanOpen(true);
      return;
    }
    if (!channelId) {
      setScanError(t("channels.scanNeedsId"));
      setScanOpen(true);
      return;
    }
    if (!draft.agent_id) {
      setScanError(t("channels.scanNeedsAgent"));
      setScanOpen(true);
      return;
    }
    setScanOpen(true);
    setScanQrUrl("");
    setScanStatus("waiting");
    setScanError("");
    stopScanPolling();
    setScanning(true);
    try {
      const { data: result } = await beginFeishuRegistration({
        body: {},
        throwOnError: true,
      });
      setScanQrUrl(await QRCode.toDataURL(result.qr_url, { width: 256, margin: 2 }));
      const intervalSeconds = result.interval || 5;
      scanIntervalRef.current = setInterval(
        () => void pollFeishuScan(result.device_code, intervalSeconds),
        intervalSeconds * 1000,
      );
      setScanning(true);
    } catch (e) {
      setScanError((e as Error).message);
      setScanning(false);
    }
  };

  const startWeixinScan = async () => {
    const channelName = ((draft.name as string) || "").trim();
    const channelId = ((draft.id as string) || "").trim();
    if (!channelName) {
      setScanError(t("channels.scanNeedsName"));
      setScanOpen(true);
      return;
    }
    if (!channelId) {
      setScanError(t("channels.scanNeedsId"));
      setScanOpen(true);
      return;
    }
    if (!draft.agent_id) {
      setScanError(t("channels.scanNeedsAgent"));
      setScanOpen(true);
      return;
    }
    setScanOpen(true);
    weixinScanRefreshesRef.current = 0;
    await beginWeixinScanPolling();
  };

  const requiresBoundAgent = draft.type === "feishu" || draft.type === "weixin";

  const availableAgents = agents.filter(
    (agent) =>
      agent.enabled !== false &&
      !channels.some((channel) => channel.type === draft.type && channel.agent_id === agent.id),
  );

  const canStartRegistrationScan = Boolean(
    ((draft.name as string) || "").trim() &&
    ((draft.id as string) || "").trim() &&
    ((draft.agent_id as string) || "").trim(),
  );

  const canSubmit = !creating && !!draft.id && !!draft.type;

  return (
    <DetailPanel
      onSave={() => onAdd(draft)}
      onCancel={onCancel}
      saveLabel={t("channels.addChannel")}
      cancelLabel={t("common.cancel")}
      isSaving={creating}
      isSavingLabel={t("channels.adding")}
      canSave={canSubmit}
    >
      <DetailPanelHeader
        title={t("channels.newChannel")}
        subtitle={<p className="text-sm text-muted-foreground">{t("channels.connectDesc")}</p>}
      />

      <div className="space-y-4">
        <div className="w-full space-y-1.5">
          <label className="text-sm font-medium">{t("channels.platform")}</label>
          <select
            value={(draft.type as string) || ""}
            onChange={(e) => updateField("type", e.target.value)}
            className={selectClassName}
          >
            <option value="" disabled>
              {t("channels.selectPlatform")}
            </option>
            {channelTypes.map((ct) => (
              <option key={ct.id} value={ct.id}>
                {ct.label}
              </option>
            ))}
          </select>
        </div>

        <div className="w-full space-y-1.5">
          <label className="text-sm font-medium">{t("common.name")}</label>
          <Input
            nativeInput
            type="text"
            value={(draft.name as string) || ""}
            onChange={(e) => updateField("name", e.target.value)}
            placeholder={t("channels.namePlaceholder")}
            className="w-full text-sm"
          />
        </div>

        <div className="w-full space-y-1.5">
          <label className="text-sm font-medium font-mono">{t("channels.channelId")}</label>
          <Input
            nativeInput
            type="text"
            value={(draft.id as string) || ""}
            onChange={(e) => updateField("id", e.target.value)}
            placeholder={t("channels.channelIdPlaceholder")}
            disabled={draft.type === "weixin"}
            className="w-full text-sm font-mono"
          />
          <p className="text-xs text-muted-foreground">
            {draft.type === "weixin"
              ? t("channels.weixinChannelIdDesc")
              : t("channels.channelIdDesc")}
          </p>
        </div>

        {requiresBoundAgent && (
          <div className="w-full space-y-1.5">
            <label className="text-sm font-medium">{t("channels.boundAgent")}</label>
            <select
              value={(draft.agent_id as string) || ""}
              onChange={(e) => updateField("agent_id", e.target.value)}
              className={selectClassName}
            >
              <option value="">{t("channels.selectAgent")}</option>
              {availableAgents.map((agent) => (
                <option key={agent.id} value={agent.id}>
                  {agent.name || agent.id}
                </option>
              ))}
            </select>
            <p className="text-xs text-muted-foreground">{t("channels.boundAgentDesc")}</p>
            {availableAgents.length === 0 && (
              <p className="text-xs text-muted-foreground">{t("channels.noAvailableAgents")}</p>
            )}
          </div>
        )}

        {!!draft.type && (
          <div className="space-y-4">
            <FormSectionTitle>{t("channels.configuration")}</FormSectionTitle>
            {(draft.type === "feishu" || draft.type === "weixin") && (
              <div className="space-y-2">
                <Button
                  type="button"
                  onClick={draft.type === "weixin" ? startWeixinScan : startFeishuScan}
                  loading={scanning}
                  disabled={scanning || !canStartRegistrationScan}
                  size="sm"
                >
                  {draft.type === "weixin"
                    ? t("channels.scanCreateWeixin")
                    : t("channels.scanCreateFeishu")}
                </Button>
                <p className="text-xs text-muted-foreground">
                  {draft.type === "weixin"
                    ? t("channels.scanCreateWeixinDesc")
                    : t("channels.scanCreateFeishuDesc")}
                </p>
              </div>
            )}
            {draft.type === "feishu" || draft.type === "weixin" ? (
              <details className="space-y-4">
                <summary className="cursor-pointer text-sm font-medium">
                  {draft.type === "weixin"
                    ? t("channels.manualWeixinSetup")
                    : t("channels.manualFeishuSetup")}
                </summary>
                <div className="pt-4">
                  <ChannelConfigFields channel={draft} onChange={updateField} />
                </div>
              </details>
            ) : (
              <ChannelConfigFields channel={draft} onChange={updateField} />
            )}
          </div>
        )}
      </div>

      <Dialog open={scanOpen} onOpenChange={setScanOpen}>
        <DialogPopup>
          <DialogHeader>
            <DialogTitle>
              {draft.type === "weixin"
                ? t("channels.scanWeixinTitle")
                : t("channels.scanFeishuTitle")}
            </DialogTitle>
            <DialogDescription>
              {draft.type === "weixin"
                ? t("channels.scanWeixinDesc")
                : t("channels.scanFeishuDesc")}
            </DialogDescription>
          </DialogHeader>
          <DialogPanel>
            <div className="flex flex-col items-center gap-4 text-center">
              {scanQrUrl && (
                <img
                  src={scanQrUrl}
                  alt={
                    draft.type === "weixin"
                      ? t("channels.scanWeixinQrAlt")
                      : t("channels.scanFeishuQrAlt")
                  }
                  className="size-48 max-w-full"
                />
              )}
              {!scanQrUrl && !scanError && <Spinner className="size-4" />}
              <Badge
                size="sm"
                variant={scanError ? "error" : scanStatus === "created" ? "success" : "warning"}
                className="max-w-full whitespace-normal"
              >
                {scanError || scanStatus || t("channels.waiting")}
              </Badge>
            </div>
          </DialogPanel>
          <DialogFooter>
            <DialogClose
              render={<Button type="button" variant="ghost" onClick={stopScanPolling} />}
            >
              {t("common.cancel")}
            </DialogClose>
          </DialogFooter>
        </DialogPopup>
      </Dialog>
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
  const label = platformLabel(channel.type, channel.label);

  return (
    <DetailPanel>
      <DetailPanelHeader
        title={label}
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
                : `Link your ${label} account once to chat with Stella on this platform.`}
            </p>
            {channel.type !== "weixin" && (
              <Button
                onClick={() => onGenerateCode(channel.type)}
                disabled={generating}
                loading={generating && linkPlatform === channel.type}
                size="sm"
              >
                Link {label}
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
            <p className="text-sm font-medium">Send this command to Stella on {label}:</p>
            <div className="flex items-center gap-2 flex-wrap">
              <code className="font-mono text-lg font-semibold bg-muted text-foreground px-3 py-1 rounded select-all">
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
  const { t } = useI18n();
  const { data: me } = useQuery(meQueryOptions);
  const isAdmin = me?.is_admin ?? false;
  const navigate = useNavigate();
  const params = useParams({ strict: false }) as { channelId?: string };
  const channelId = params.channelId;

  const [publicChannels, setPublicChannels] = useState<ComponentsPublicChannel[]>([]);
  const [linkedIdentities, setLinkedIdentities] = useState<Identity[]>([]);
  const [instances, setInstances] = useState<NormalizedChannel[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [loadingInstances, setLoadingInstances] = useState(false);

  const [creatingInstance, setCreatingInstance] = useState(false);

  const { toasts, showToast } = useToast();

  // ── derived state ──

  const isCreating = isAdmin && channelId === "new";

  const selectedChannel = useMemo(
    () =>
      isAdmin && channelId && channelId !== "new"
        ? instances.find((ch) => ch.id === channelId)
        : undefined,
    [isAdmin, channelId, instances],
  );

  const selectedPublicChannel = useMemo(
    () => (!isAdmin && channelId ? publicChannels.find((ch) => ch.type === channelId) : undefined),
    [isAdmin, channelId, publicChannels],
  );

  // ── helpers ──

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

  const loadAgents = useCallback(async () => {
    try {
      const { data } = await listAgents({
        query: { include_all: true },
        throwOnError: true,
      });
      setAgents((data?.agents as Agent[]) ?? []);
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
      void Promise.all([loadPublicChannels(), loadIdentities(), loadInstances(), loadAgents()]);
    } else {
      void Promise.all([loadPublicChannels(), loadIdentities()]);
    }
  }, [isAdmin, loadPublicChannels, loadIdentities, loadInstances, loadAgents]);

  // ── account linking ──

  const link = useAccountLink({ notify: showToast, onLinked: () => void loadIdentities() });

  // ── identity management ──

  const unlinkIdentity = async (id: string | undefined) => {
    if (!id || !confirm("Unlink this identity?")) return;
    try {
      await unlinkProfileIdentity({
        path: { id: String(id) },
        throwOnError: true,
      });
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
      void navigate({ to: "/settings/channels" });
      await loadInstances();
      showToast(id + " deleted");
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  };

  const finishRegisteredChannel = async (channel: NormalizedChannel) => {
    await loadInstances();
    void navigate({
      to: "/settings/channels/$channelId",
      params: { channelId: channel.id },
    });
    showToast(channel.id + " created");
  };

  const createNewChannel = async (draft: Record<string, unknown>) => {
    const id = ((draft.id as string) || "").trim();
    if (!id || !draft.type) {
      showToast("ID and platform are required", "error");
      return;
    }
    if (id === draft.type && draft.type !== "weixin") {
      showToast("Dedicated instance ID must not match the platform ID", "error");
      return;
    }
    if ((draft.type === "feishu" || draft.type === "weixin") && !draft.agent_id) {
      showToast(t("channels.scanNeedsAgent"), "error");
      return;
    }
    setCreatingInstance(true);
    try {
      const { data: saved } = await createChannelRequest({
        body: {
          id,
          name: (draft.name as string) || "",
          type: draft.type as string,
          agent_id: (draft.agent_id as string) || "",
          config: channelConfig(draft),
        },
        throwOnError: true,
      });
      await loadInstances();
      void navigate({
        to: "/settings/channels/$channelId",
        params: { channelId: saved.id },
      });
      showToast(saved.id + " created");
    } catch (e) {
      showToast((e as Error).message, "error");
    } finally {
      setCreatingInstance(false);
    }
  };

  // ── render ──

  const isLoading = isAdmin && loadingInstances;

  // ── build detail pane ──

  let detail: React.ReactNode = undefined;

  if (isAdmin) {
    if (isCreating) {
      detail = (
        <NewChannelForm
          fallbackChannelType={defaultChannelType}
          agents={agents}
          channels={instances}
          onAdd={createNewChannel}
          onRegistered={finishRegisteredChannel}
          onCancel={() => void navigate({ to: "/settings/channels" })}
          creating={creatingInstance}
        />
      );
    } else if (selectedChannel) {
      detail = (
        <ChannelDetail
          key={selectedChannel.id}
          channel={selectedChannel}
          identity={identityFor(selectedChannel.type)}
          generating={link.generating}
          linkPlatform={link.platform}
          linkCode={link.code}
          wxQrUrl={link.qrUrl}
          wxQrStatus={link.qrStatus}
          wxQrPolling={link.qrPolling}
          onUpdate={(key, value) => updateInstance(selectedChannel.id, key, value)}
          onSave={saveInstance}
          onDelete={doDeleteChannel}
          onGenerateCode={(platform) => void link.generateCode(platform)}
          onStartWeixinQR={() => void link.startQr()}
          onUnlink={unlinkIdentity}
          onCopyLinkCode={link.copyCode}
          wxQrStatusVariant={weixinQrStatusVariant}
          onRefreshWxQr={() => void link.startQr()}
        />
      );
    }
  } else if (selectedPublicChannel) {
    detail = (
      <PublicChannelDetail
        key={selectedPublicChannel.type}
        channel={selectedPublicChannel}
        identity={identityFor(selectedPublicChannel.type)}
        linked={isLinked(selectedPublicChannel.type)}
        generating={link.generating}
        linkPlatform={link.platform}
        linkCode={link.code}
        wxQrUrl={link.qrUrl}
        wxQrStatus={link.qrStatus}
        wxQrPolling={link.qrPolling}
        onGenerateCode={(platform) => void link.generateCode(platform)}
        onStartWeixinQR={() => void link.startQr()}
        onUnlink={unlinkIdentity}
        onCopyLinkCode={link.copyCode}
        wxQrStatusVariant={weixinQrStatusVariant}
        onRefreshWxQr={() => void link.startQr()}
      />
    );
  }

  // ── build card grid ──

  const sheetOpen = isCreating || !!selectedChannel || !!selectedPublicChannel;
  const closeSheet = () => void navigate({ to: "/settings/channels" });

  // Admin instances, grouped by platform (the instances list is already sorted
  // default-instance-first within each type).
  const adminGroups = Object.values(
    instances.reduce<Record<string, { type: string; label: string; items: NormalizedChannel[] }>>(
      (acc, ch) => {
        (acc[ch.type] ??= {
          type: ch.type,
          label: platformLabel(ch.type),
          items: [],
        }).items.push(ch);
        return acc;
      },
      {},
    ),
  ).sort((a, b) => a.label.localeCompare(b.label));

  return (
    <>
      <SettingsGridPage
        title={t("channels.title")}
        action={
          isAdmin ? (
            <Button
              render={<Link to="/settings/channels/$channelId" params={{ channelId: "new" }} />}
              variant="outline"
              size="sm"
            >
              <Plus className="size-4" />
              {t("channels.addChannel")}
            </Button>
          ) : undefined
        }
      >
        {isAdmin ? (
          isLoading ? (
            <div className="flex justify-center py-8">
              <Spinner className="size-4" />
            </div>
          ) : (
            adminGroups.map((group) => (
              <SettingsCardSection
                key={group.type}
                icon={<PlatformIcon type={group.type} />}
                title={group.label}
                count={group.items.length}
              >
                {group.items.map((ch) => {
                  const label = platformLabel(ch.type);
                  const isDefault = ch.id === ch.type;
                  return (
                    <SettingsCard
                      key={ch.id}
                      icon={<PlatformIcon type={ch.type} />}
                      title={ch.name || label}
                      badge={
                        isDefault ? (
                          <Badge variant="secondary" size="sm">
                            default
                          </Badge>
                        ) : undefined
                      }
                      active={channelId === ch.id}
                      to="/settings/channels/$channelId"
                      params={{ channelId: ch.id }}
                      footer={
                        <>
                          <span
                            className={`size-1.5 shrink-0 rounded-full ${
                              ch.enabled ? "bg-chart-3" : "bg-muted-foreground"
                            }`}
                          />
                          <span className="font-mono text-xs text-muted-foreground">{ch.id}</span>
                        </>
                      }
                    />
                  );
                })}
              </SettingsCardSection>
            ))
          )
        ) : (
          <SettingsCardSection title={t("channels.title")} count={publicChannels.length}>
            {publicChannels.map((ch) => {
              const label = platformLabel(ch.type, ch.label);
              const linked = isLinked(ch.type);
              return (
                <SettingsCard
                  key={ch.type}
                  icon={<PlatformIcon type={ch.type} />}
                  title={label}
                  active={channelId === ch.type}
                  to="/settings/channels/$channelId"
                  params={{ channelId: ch.type }}
                  footer={
                    <Badge size="sm" variant={linked ? "success" : "secondary"}>
                      {linked ? "linked" : "not linked"}
                    </Badge>
                  }
                />
              );
            })}
          </SettingsCardSection>
        )}
      </SettingsGridPage>

      <SettingsDetailSheet open={sheetOpen} onClose={closeSheet}>
        {detail}
      </SettingsDetailSheet>

      <ToastContainer messages={toasts} />
    </>
  );
}
