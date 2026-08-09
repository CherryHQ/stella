import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { meQueryOptions } from "@/lib/queries/me";
import {
  createChannel as createChannelRequest,
  deleteChannel,
  listAgents,
  listChannels,
  listProfileIdentities,
  unlinkProfileIdentity,
  updateChannel,
} from "@/lib/api-client/sdk.gen";
import type { Agent, Channel, Identity } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Spinner } from "@/components/ui/spinner";
import { useI18n } from "@/lib/i18n";
import { useToast } from "@/hooks/use-toast";
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
  ChannelFields,
  channelConfig,
  defaultChannelType,
  hasConfig,
  normalizeChannel,
  type NormalizedChannel,
} from "./ChannelFields";
import { NewChannelForm, newChannelDraftError } from "./NewChannelForm";
import { bindableAgents } from "./channel-access";
import { useAccountLink, weixinQrStatusVariant } from "./use-account-link";

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
  /**
   * Ask the page to confirm the delete. The confirmation is an overlay and this
   * detail renders inside a Sheet, so the page owns it — nesting overlays is a
   * bug (`web-ui.md`).
   */
  onRequestDelete: (ch: NormalizedChannel) => void;
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
  onRequestDelete,
  onGenerateCode,
  onStartWeixinQR,
  onUnlink,
  onCopyLinkCode,
  wxQrStatusVariant,
  onRefreshWxQr,
}: ChannelDetailProps) {
  const { t } = useI18n();
  const [channel, setChannel] = useState<NormalizedChannel>(initialChannel);

  useEffect(() => {
    setChannel(initialChannel);
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
      onDelete={!isDefaultInstance ? () => onRequestDelete(channel) : undefined}
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
    </DetailPanel>
  );
}

// ─── main page ────────────────────────────────────────────────────────────────

export function ChannelsPage() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const params = useParams({ strict: false }) as { channelId?: string };
  const channelId = params.channelId;
  // Creation opened from an agent's profile already knows the agent.
  const search = useSearch({ strict: false }) as { agent?: string };
  const initialAgentId = search.agent ?? "";

  const { data: me } = useQuery(meQueryOptions);

  const [linkedIdentities, setLinkedIdentities] = useState<Identity[]>([]);
  const [instances, setInstances] = useState<NormalizedChannel[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [loadingInstances, setLoadingInstances] = useState(false);

  const [creatingInstance, setCreatingInstance] = useState(false);
  // The delete confirmation lives here, not in the detail: the detail renders
  // inside the Sheet and an overlay may not nest inside another (`web-ui.md`).
  const [pendingDelete, setPendingDelete] = useState<NormalizedChannel | null>(null);

  const { showToast } = useToast();

  // ── derived state ──

  const isCreating = channelId === "new";

  const selectedChannel = useMemo(
    () =>
      channelId && channelId !== "new" ? instances.find((ch) => ch.id === channelId) : undefined,
    [channelId, instances],
  );

  // ── helpers ──

  const identityFor = useCallback(
    (platform: string): Identity | null =>
      linkedIdentities.find((i) => i.platform === platform) || null,
    [linkedIdentities],
  );

  // ── data loading ──

  const loadIdentities = useCallback(async () => {
    try {
      const { data } = await listProfileIdentities({ throwOnError: true });
      setLinkedIdentities((data?.identities as Identity[]) ?? []);
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

  // The picker offers only what the API would accept; a readable-but-foreign
  // agent would just fail the bind.
  const pickableAgents = useMemo(() => bindableAgents(agents, me), [agents, me]);

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
    void Promise.all([loadIdentities(), loadInstances(), loadAgents()]);
  }, [loadIdentities, loadInstances, loadAgents]);

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
    const invalid = newChannelDraftError(draft, t);
    if (invalid) {
      showToast(invalid, "error");
      return;
    }
    setCreatingInstance(true);
    try {
      const { data: saved } = await createChannelRequest({
        // No id: the server mints it (and pins weixin to its singleton id).
        body: {
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

  const isLoading = loadingInstances;

  // ── build detail pane ──

  let detail: React.ReactNode = undefined;

  if (isCreating) {
    detail = (
      <NewChannelForm
        fallbackChannelType={defaultChannelType}
        initialAgentId={initialAgentId}
        agents={pickableAgents}
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
        onRequestDelete={setPendingDelete}
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

  const sheetOpen = isCreating || !!selectedChannel;
  const closeSheet = () => void navigate({ to: "/settings/channels" });

  // Manageable instances, grouped by platform (the instances list is already
  // sorted default-instance-first within each type).
  const channelGroups = Object.values(
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
          <Button
            render={<Link to="/settings/channels/$channelId" params={{ channelId: "new" }} />}
            variant="outline"
            size="sm"
          >
            <Plus className="size-4" />
            {t("channels.addChannel")}
          </Button>
        }
      >
        {isLoading ? (
          <div className="flex justify-center py-8">
            <Spinner className="size-4" />
          </div>
        ) : (
          channelGroups.map((group) => (
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
                            ch.enabled ? "bg-success" : "bg-muted-foreground"
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
        )}
      </SettingsGridPage>

      <SettingsDetailSheet open={sheetOpen} onClose={closeSheet}>
        {detail}
      </SettingsDetailSheet>

      <ConfirmDialog
        open={!!pendingDelete}
        onOpenChange={(open) => !open && setPendingDelete(null)}
        title={t("channels.deleteChannel")}
        message={pendingDelete ? t("channels.deleteChannelMsg", { id: pendingDelete.id }) : ""}
        onConfirm={() => {
          if (pendingDelete) void doDeleteChannel(pendingDelete.id);
          setPendingDelete(null);
        }}
      />
    </>
  );
}
