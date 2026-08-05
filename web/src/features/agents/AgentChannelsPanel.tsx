import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertDialog,
  AlertDialogClose,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogPopup,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { PlatformIcon, platformLabel } from "@/components/PlatformIcon";
import { updateChannel, unlinkProfileIdentity } from "@/lib/api-client/sdk.gen";
import type { ComponentsPublicChannel } from "@/lib/api-client/types.gen";
import { apiErrorMessage } from "@/lib/api-error";
import { agentsQueryOptions } from "@/lib/queries/agents";
import {
  channelsQueryOptions,
  profileIdentitiesQueryOptions,
  publicChannelsQueryOptions,
} from "@/lib/queries/channels";
import { meQueryOptions } from "@/lib/queries/me";
import type { Channel, Identity } from "@/lib/types";
import { ToastContainer, useToast } from "@/hooks/use-toast";
import { useI18n } from "@/lib/i18n";
import {
  LINK_CODE_PLATFORMS,
  QR_PLATFORM,
  useAccountLink,
  weixinQrStatusVariant,
} from "@/features/channels/use-account-link";
import { ProfilePanelSection, ProfileSectionMessage } from "./ProfilePanelSection";

const QR_STATUS_KEY = {
  waiting: "channels.qrWaiting",
  scaned: "channels.qrScanned",
  confirmed: "channels.qrConfirmed",
  expired: "channels.qrExpired",
} as const;

interface Props {
  agentId: string;
}

/**
 * How this agent is reached from chat platforms, in the two shapes the feature
 * actually has: every user links their own account on a platform (the identity
 * is per platform, not per channel), while an admin may hand a whole channel to
 * one agent. Both read the same channel list so the binding a user sees is the
 * one an admin just wrote.
 */
export function AgentChannelsPanel({ agentId }: Props) {
  const { t } = useI18n();
  const { toasts, showToast } = useToast();
  const queryClient = useQueryClient();
  const { data: me } = useQuery(meQueryOptions);
  const isAdmin = me?.is_admin ?? false;

  const publicChannels = useQuery(publicChannelsQueryOptions);
  const identities = useQuery(profileIdentitiesQueryOptions);
  const adminChannels = useQuery({ ...channelsQueryOptions, enabled: isAdmin });
  const { data: agents = [] } = useQuery(agentsQueryOptions);

  const [pendingUnlink, setPendingUnlink] = useState<Identity | null>(null);
  const [pendingRebind, setPendingRebind] = useState<Channel | null>(null);

  const invalidateIdentities = () =>
    void queryClient.invalidateQueries({ queryKey: ["profile-identities"] });

  const link = useAccountLink({ notify: showToast, onLinked: invalidateIdentities });

  const unlink = useMutation({
    mutationFn: (identity: Identity) =>
      unlinkProfileIdentity({ path: { id: identity.id }, throwOnError: true }),
    onSuccess: async () => {
      showToast(t("agents.channels.unlinked"));
      link.reset();
      await queryClient.invalidateQueries({ queryKey: ["profile-identities"] });
    },
    onError: (error) =>
      showToast(apiErrorMessage(error, t("agents.channels.unlinkFailed")), "error"),
  });

  // Binding is a partial PATCH on purpose: sending `config` here would make the
  // channels tab a second writer of credentials it never read.
  const bind = useMutation({
    mutationFn: ({ channel, target }: { channel: Channel; target: string }) =>
      updateChannel({ path: { id: channel.id }, body: { agent_id: target }, throwOnError: true }),
    onSuccess: async (_data, variables) => {
      showToast(variables.target ? t("agents.channels.bound") : t("agents.channels.unbound"));
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["channels"] }),
        queryClient.invalidateQueries({ queryKey: ["public-channels"] }),
      ]);
    },
    onError: (error) => showToast(apiErrorMessage(error, t("agents.channels.bindFailed")), "error"),
  });

  const identityFor = (platform: string) =>
    (identities.data ?? []).find((identity) => identity.platform === platform) ?? null;

  const agentName = (id: string) => agents.find((agent) => agent.id === id)?.name || id;

  // One group per platform: an account is linked per platform, so the link
  // controls belong to the group while the channels below it carry the routing.
  const platforms = useMemo(() => {
    const groups = new Map<
      string,
      { type: string; label: string; channels: ComponentsPublicChannel[] }
    >();
    for (const channel of publicChannels.data ?? []) {
      const group = groups.get(channel.type) ?? {
        type: channel.type,
        label: platformLabel(channel.type, channel.label),
        channels: [],
      };
      group.channels.push(channel);
      groups.set(channel.type, group);
    }
    return [...groups.values()].sort((a, b) => a.label.localeCompare(b.label));
  }, [publicChannels.data]);

  const bindableChannels = useMemo(
    () =>
      [...(adminChannels.data ?? [])]
        .filter((channel) => channel.enabled)
        .sort((a, b) => a.id.localeCompare(b.id)),
    [adminChannels.data],
  );

  const routingLine = (channel: ComponentsPublicChannel) => {
    if (channel.agent_id === agentId) return t("agents.channels.servesThisAgent");
    if (channel.agent_id)
      return t("agents.channels.servesAgent", { name: channel.agent_name || channel.agent_id });
    return t("agents.channels.routesToDefault");
  };

  return (
    <div className="flex flex-col gap-6">
      <ToastContainer messages={toasts} />

      <ProfilePanelSection
        title={t("agents.channels.accessTitle")}
        description={t("agents.channels.accessDesc")}
        count={platforms.length}
      >
        <div className="flex flex-col gap-2">
          {publicChannels.isLoading ? (
            <ProfileSectionMessage>{t("agents.channels.loading")}</ProfileSectionMessage>
          ) : platforms.length === 0 ? (
            <ProfileSectionMessage>{t("agents.channels.noEnabled")}</ProfileSectionMessage>
          ) : (
            platforms.map((group) => {
              const identity = identityFor(group.type);
              const canLinkCode = LINK_CODE_PLATFORMS.has(group.type);
              const canScan = group.type === QR_PLATFORM;
              const pending = link.platform === group.type;
              return (
                <div
                  key={group.type}
                  className="flex flex-col gap-2 rounded-lg border border-border p-3"
                >
                  <div className="flex items-start justify-between gap-3">
                    <div className="flex min-w-0 flex-col gap-1">
                      <div className="flex min-w-0 items-center gap-2">
                        <span className="shrink-0 text-muted-foreground">
                          <PlatformIcon type={group.type} />
                        </span>
                        <span className="truncate text-sm font-semibold text-foreground">
                          {group.label}
                        </span>
                        <Badge variant={identity ? "success" : "outline"}>
                          {identity ? t("channels.linked") : t("channels.notLinked")}
                        </Badge>
                      </div>
                      {identity && (
                        <p className="truncate font-mono text-xs text-muted-foreground">
                          {identity.name ? `${identity.name} · ` : ""}
                          {identity.external_id}
                        </p>
                      )}
                      {group.channels.map((channel) => (
                        <p key={channel.id} className="truncate text-xs text-muted-foreground">
                          {group.channels.length > 1 ? `${channel.id} · ` : ""}
                          {routingLine(channel)}
                        </p>
                      ))}
                    </div>
                    <div className="flex shrink-0 items-center gap-1">
                      {identity ? (
                        <Button
                          variant="ghost"
                          size="sm"
                          disabled={unlink.isPending}
                          onClick={() => setPendingUnlink(identity)}
                        >
                          {t("agents.channels.unlink")}
                        </Button>
                      ) : canLinkCode ? (
                        <Button
                          variant="outline"
                          size="sm"
                          loading={link.generating && pending}
                          onClick={() => void link.generateCode(group.type)}
                        >
                          {t("agents.channels.linkAccount")}
                        </Button>
                      ) : canScan ? (
                        <Button
                          variant="outline"
                          size="sm"
                          loading={link.qrPolling && pending}
                          onClick={() => void link.startQr()}
                        >
                          {t("agents.channels.linkAccount")}
                        </Button>
                      ) : null}
                    </div>
                  </div>

                  {pending && link.code && (
                    <div className="flex flex-col gap-2 rounded-lg bg-muted p-3">
                      <p className="text-xs text-muted-foreground">
                        {t("agents.channels.linkHint", { platform: group.label })}
                      </p>
                      <div className="flex flex-wrap items-center gap-2">
                        <code className="select-all rounded bg-background px-2 py-1 font-mono text-sm text-foreground">
                          /link {link.code}
                        </code>
                        <Button variant="ghost" size="xs" onClick={link.copyCode}>
                          {t("common.copy")}
                        </Button>
                      </div>
                      <p className="text-xs text-muted-foreground">
                        {t("agents.channels.linkExpires")}
                      </p>
                    </div>
                  )}

                  {pending && link.qrUrl && (
                    <div className="flex flex-col items-center gap-2 rounded-lg bg-muted p-3">
                      <p className="text-xs text-muted-foreground">
                        {t("agents.channels.scanHint")}
                      </p>
                      <img
                        src={link.qrUrl}
                        alt={t("agents.channels.qrAlt")}
                        className="size-48 rounded-lg"
                      />
                      <Badge variant={weixinQrStatusVariant(link.qrStatus)}>
                        {t(
                          QR_STATUS_KEY[link.qrStatus as keyof typeof QR_STATUS_KEY] ??
                            "channels.qrWaiting",
                        )}
                      </Badge>
                      {link.qrStatus === "expired" && (
                        <Button variant="outline" size="xs" onClick={() => void link.startQr()}>
                          {t("common.refresh")}
                        </Button>
                      )}
                    </div>
                  )}
                </div>
              );
            })
          )}
        </div>
      </ProfilePanelSection>

      {isAdmin && (
        <ProfilePanelSection
          title={t("agents.channels.bindingTitle")}
          description={t("agents.channels.bindingDesc")}
          count={bindableChannels.length}
        >
          <div className="flex flex-col gap-2">
            {adminChannels.isLoading ? (
              <ProfileSectionMessage>{t("agents.channels.loading")}</ProfileSectionMessage>
            ) : bindableChannels.length === 0 ? (
              <ProfileSectionMessage>{t("agents.channels.noEnabled")}</ProfileSectionMessage>
            ) : (
              bindableChannels.map((channel) => {
                const boundHere = channel.agent_id === agentId;
                const boundElsewhere = !!channel.agent_id && !boundHere;
                const busy = bind.isPending && bind.variables?.channel.id === channel.id;
                // A row written before `type` existed carries its platform in
                // the id, the same fallback the backend applies.
                const type = channel.type || channel.id;
                return (
                  <div
                    key={channel.id}
                    className="flex items-start justify-between gap-3 rounded-lg border border-border p-3"
                  >
                    <div className="flex min-w-0 flex-col gap-1">
                      <div className="flex min-w-0 items-center gap-2">
                        <span className="shrink-0 text-muted-foreground">
                          <PlatformIcon type={type} />
                        </span>
                        <span className="truncate text-sm font-semibold text-foreground">
                          {channel.name || platformLabel(type)}
                        </span>
                        {boundHere && (
                          <Badge variant="success">{t("agents.channels.servesThisAgent")}</Badge>
                        )}
                        {boundElsewhere && (
                          <Badge variant="outline">
                            {t("agents.channels.servesAgent", {
                              name: agentName(channel.agent_id ?? ""),
                            })}
                          </Badge>
                        )}
                      </div>
                      <p className="truncate font-mono text-xs text-muted-foreground">
                        {channel.id}
                      </p>
                    </div>
                    <div className="flex shrink-0 items-center gap-1">
                      <Button
                        variant={boundHere ? "ghost" : "outline"}
                        size="sm"
                        disabled={busy}
                        onClick={() => {
                          if (boundHere) {
                            bind.mutate({ channel, target: "" });
                          } else if (boundElsewhere) {
                            setPendingRebind(channel);
                          } else {
                            bind.mutate({ channel, target: agentId });
                          }
                        }}
                      >
                        {boundHere ? t("agents.channels.unbind") : t("agents.channels.bind")}
                      </Button>
                    </div>
                  </div>
                );
              })
            )}
          </div>
        </ProfilePanelSection>
      )}

      {/* Both confirmations live at page level: an overlay nested inside another
          overlay is a bug (see web-ui.md). */}
      <AlertDialog open={!!pendingUnlink} onOpenChange={(open) => !open && setPendingUnlink(null)}>
        <AlertDialogPopup>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("agents.channels.unlinkTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("agents.channels.unlinkConfirm", {
                platform: platformLabel(pendingUnlink?.platform ?? ""),
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogClose render={<Button variant="ghost" />}>
              {t("common.cancel")}
            </AlertDialogClose>
            <Button
              variant="destructive"
              onClick={() => {
                const target = pendingUnlink;
                setPendingUnlink(null);
                if (target) unlink.mutate(target);
              }}
            >
              {t("agents.channels.unlink")}
            </Button>
          </AlertDialogFooter>
        </AlertDialogPopup>
      </AlertDialog>

      <AlertDialog open={!!pendingRebind} onOpenChange={(open) => !open && setPendingRebind(null)}>
        <AlertDialogPopup>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("agents.channels.rebindTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("agents.channels.rebindConfirm", {
                channel: pendingRebind?.name || pendingRebind?.id || "",
                agent: agentName(pendingRebind?.agent_id ?? ""),
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogClose render={<Button variant="ghost" />}>
              {t("common.cancel")}
            </AlertDialogClose>
            <Button
              onClick={() => {
                const target = pendingRebind;
                setPendingRebind(null);
                if (target) bind.mutate({ channel: target, target: agentId });
              }}
            >
              {t("agents.channels.bind")}
            </Button>
          </AlertDialogFooter>
        </AlertDialogPopup>
      </AlertDialog>
    </div>
  );
}
