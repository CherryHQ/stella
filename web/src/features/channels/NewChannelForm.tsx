import { useCallback, useEffect, useRef, useState } from "react";
import { ChevronLeft } from "lucide-react";
import QRCode from "qrcode";
import {
  beginFeishuRegistration,
  beginWeixinRegistration,
  pollFeishuRegistration,
  pollWeixinRegistration,
} from "@/lib/api-client/sdk.gen";
import type { Agent, Channel } from "@/lib/types";
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
import { Spinner } from "@/components/ui/spinner";
import {
  DetailPanel,
  DetailPanelHeader,
  FormSectionTitle,
} from "@/features/settings/SettingsDetailPanel";
import { useI18n } from "@/lib/i18n";
import {
  ChannelConfigFields,
  channelTypes,
  newInstanceDraft,
  normalizeChannel,
  serializePlatformConfig,
  suggestChannelName,
  type NormalizedChannel,
} from "./ChannelFields";

type Translate = ReturnType<typeof useI18n>["t"];

/**
 * The rules a create request must satisfy before it is worth sending, in one
 * place so the settings page and the agent profile's create sheet reject the
 * same drafts with the same words. Returns the message to show, or null.
 */
export function newChannelDraftError(draft: Record<string, unknown>, t: Translate): string | null {
  if (!draft.type) return t("channels.platformRequired");
  if ((draft.type === "feishu" || draft.type === "weixin") && !draft.agent_id) {
    return t("channels.scanNeedsAgent");
  }
  return null;
}

export interface NewChannelFormProps {
  fallbackChannelType: string;
  /** Preselected bound agent when creation starts from an agent's profile. */
  initialAgentId?: string;
  /**
   * Creation started from an agent: the binding is a fact of the entry point,
   * not a choice, so the picker is replaced by a one-line statement of it.
   */
  lockAgent?: boolean;
  agents: Agent[];
  channels: NormalizedChannel[];
  onAdd: (channel: Record<string, unknown>) => Promise<void>;
  onRegistered: (channel: NormalizedChannel) => Promise<void>;
  onCancel: () => void;
  creating: boolean;
}

/**
 * The one create-a-channel form. It is mounted both as the detail pane of
 * `/settings/channels` and inside the agent profile's create sheet, so the QR
 * registration flow it owns covers this surface (`absolute inset-0`) instead of
 * opening a Dialog: an overlay inside the Sheet would be nested, which
 * `web-ui.md` forbids, and covering keeps the typed fields alive behind it.
 */
export function NewChannelForm({
  fallbackChannelType,
  initialAgentId = "",
  lockAgent = false,
  agents,
  channels,
  onAdd,
  onRegistered,
  onCancel,
  creating,
}: NewChannelFormProps) {
  const { t } = useI18n();
  const [draft, setDraft] = useState<Record<string, unknown>>(() => ({
    ...newInstanceDraft(fallbackChannelType),
    agent_id: initialAgentId,
  }));
  // The suggested name follows the platform until the user makes it theirs.
  const [nameIsSuggested, setNameIsSuggested] = useState(true);
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

  const closeScan = () => {
    stopScanPolling();
    setScanOpen(false);
  };

  const updateField = (key: string, value: unknown) => {
    if (key === "type") {
      const type = value as string;
      // Switching platform resets the credential fields but keeps the chosen
      // agent — it is platform-independent — and re-suggests the name unless the
      // user already wrote their own.
      setDraft({
        ...newInstanceDraft(
          type,
          nameIsSuggested ? suggestChannelName(type) : ((draft.name as string) ?? ""),
        ),
        agent_id: draft.agent_id,
      });
    } else if (key === "agent_id" && lockAgent) {
      // The entry point owns the binding here.
    } else {
      if (key === "name") setNameIsSuggested(false);
      setDraft((prev) => ({ ...prev, [key]: value }));
    }
  };

  const pollFeishuScan = useCallback(
    async (deviceCode: string, intervalSeconds: number) => {
      const agentId = ((draft.agent_id as string) || "").trim();
      if (!deviceCode || !agentId) return;
      try {
        // No channel_id: the server mints it, the same way a plain create does.
        const { data: result } = await pollFeishuRegistration({
          body: {
            device_code: deviceCode,
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
    const agentId = ((draft.agent_id as string) || "").trim();
    if (!qrCode || !agentId) return;
    try {
      // WeChat is singleton-only; the server pins the id to "weixin".
      const { data: result } = await pollWeixinRegistration({
        body: {
          qrcode: qrCode,
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
    if (!channelName) {
      setScanError(t("channels.scanNeedsName"));
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
    if (!channelName) {
      setScanError(t("channels.scanNeedsName"));
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
    ((draft.name as string) || "").trim() && ((draft.agent_id as string) || "").trim(),
  );

  const canSubmit = !creating && !!draft.type;

  const isWeixin = draft.type === "weixin";

  return (
    <div className="relative h-full min-h-0">
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
            <Select
              value={(draft.type as string) || null}
              onValueChange={(value) => updateField("type", (value as string | null) ?? "")}
            >
              <SelectTrigger className="w-full">
                <SelectValue placeholder={t("channels.selectPlatform")}>
                  {(value) =>
                    value ? (channelTypes.find((ct) => ct.id === value)?.label ?? value) : null
                  }
                </SelectValue>
              </SelectTrigger>
              <SelectPopup>
                {channelTypes.map((ct) => (
                  <SelectItem key={ct.id} value={ct.id}>
                    {ct.label}
                  </SelectItem>
                ))}
              </SelectPopup>
            </Select>
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
            <p className="text-xs text-muted-foreground">{t("channels.nameDesc")}</p>
          </div>

          {isWeixin && (
            <p className="text-xs text-muted-foreground">{t("channels.weixinSingletonDesc")}</p>
          )}

          {lockAgent ? (
            <p className="text-xs text-muted-foreground">{t("channels.boundToThisAgent")}</p>
          ) : (
            requiresBoundAgent && (
              <div className="w-full space-y-1.5">
                <label className="text-sm font-medium">{t("channels.boundAgent")}</label>
                <Select
                  value={(draft.agent_id as string) || null}
                  onValueChange={(value) => updateField("agent_id", (value as string | null) ?? "")}
                >
                  <SelectTrigger className="w-full">
                    <SelectValue placeholder={t("channels.selectAgent")}>
                      {(value) =>
                        value
                          ? (availableAgents.find((agent) => agent.id === value)?.name ??
                            (value as string))
                          : null
                      }
                    </SelectValue>
                  </SelectTrigger>
                  <SelectPopup>
                    {availableAgents.map((agent) => (
                      <SelectItem key={agent.id} value={agent.id}>
                        {agent.name || agent.id}
                      </SelectItem>
                    ))}
                  </SelectPopup>
                </Select>
                <p className="text-xs text-muted-foreground">{t("channels.boundAgentDesc")}</p>
                {availableAgents.length === 0 && (
                  <p className="text-xs text-muted-foreground">{t("channels.noAvailableAgents")}</p>
                )}
              </div>
            )
          )}

          {!!draft.type && (
            <div className="space-y-4">
              <FormSectionTitle>{t("channels.configuration")}</FormSectionTitle>
              {requiresBoundAgent && (
                <div className="space-y-2">
                  <Button
                    type="button"
                    onClick={isWeixin ? startWeixinScan : startFeishuScan}
                    loading={scanning}
                    disabled={scanning || !canStartRegistrationScan}
                    size="sm"
                  >
                    {isWeixin ? t("channels.scanCreateWeixin") : t("channels.scanCreateFeishu")}
                  </Button>
                  <p className="text-xs text-muted-foreground">
                    {isWeixin
                      ? t("channels.scanCreateWeixinDesc")
                      : t("channels.scanCreateFeishuDesc")}
                  </p>
                </div>
              )}
              {requiresBoundAgent ? (
                <details className="space-y-4">
                  <summary className="cursor-pointer text-sm font-medium">
                    {isWeixin ? t("channels.manualWeixinSetup") : t("channels.manualFeishuSetup")}
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
      </DetailPanel>

      {scanOpen && (
        <div className="absolute inset-0 z-10 flex flex-col bg-background">
          <div className="flex shrink-0 items-center gap-2 border-b p-4">
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={t("common.back")}
              onClick={closeScan}
            >
              <ChevronLeft size={16} />
            </Button>
            <div className="min-w-0 flex-1">
              <p className="text-sm font-semibold">
                {isWeixin ? t("channels.scanWeixinTitle") : t("channels.scanFeishuTitle")}
              </p>
              <p className="text-xs text-muted-foreground">
                {isWeixin ? t("channels.scanWeixinDesc") : t("channels.scanFeishuDesc")}
              </p>
            </div>
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto p-5">
            <div className="flex flex-col items-center gap-4 text-center">
              {scanQrUrl && (
                <img
                  src={scanQrUrl}
                  alt={isWeixin ? t("channels.scanWeixinQrAlt") : t("channels.scanFeishuQrAlt")}
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
          </div>
          <div className="flex shrink-0 items-center justify-end gap-2 border-t p-4">
            <Button type="button" variant="ghost" onClick={closeScan}>
              {t("common.cancel")}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
