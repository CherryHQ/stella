import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  deleteWebhookEndpoint,
  getWebhookEndpoint,
  issueWebhookEndpoint,
  rotateWebhookEndpoint,
} from "@/lib/api-client/sdk.gen";
import type {
  ComponentsWebhookEndpoint,
  ComponentsWebhookEndpointSecret,
  ComponentsWebhookProvider,
} from "@/lib/api-client/types.gen";
import { apiErrorMessage } from "@/lib/api-error";
import { fetchAllAuthUsers } from "@/lib/auth-users";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogPopup,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  AlertDialog,
  AlertDialogClose,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogPopup,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectItem,
  SelectPopup,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { formatTime } from "@/lib/time";
import { useI18n } from "@/lib/i18n";
import type { User } from "@/lib/types";

type Provider = ComponentsWebhookProvider;
type Endpoint = ComponentsWebhookEndpoint;
export type EndpointSecret = ComponentsWebhookEndpointSecret;

export interface WebhookEndpointChannel {
  id: string;
  provider?: unknown;
  github_events?: unknown;
  github_repositories?: unknown;
}

function stringList(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === "string")
    : [];
}

function commaSeparatedList(value: string): string[] {
  return [
    ...new Set(
      value
        .split(",")
        .map((item) => item.trim())
        .filter(Boolean),
    ),
  ];
}

function userName(user: User): string {
  return user.name || user.email || user.id;
}

function endpointQueryOptions(channelId: string) {
  return {
    queryKey: ["webhook-endpoint", channelId],
    queryFn: async (): Promise<Endpoint | null> => {
      const { data, response } = await getWebhookEndpoint({ path: { id: channelId } });
      if (response?.status === 404) return null;
      if (!data) throw new Error("Unable to load webhook endpoint");
      return data;
    },
  };
}

export function WebhookEndpointPanel({
  channel,
  onPersistConfig,
  onSecret,
  onToast,
}: {
  channel: WebhookEndpointChannel;
  onPersistConfig: (config: {
    provider: Provider;
    github_events: string[];
    github_repositories: string[];
  }) => Promise<boolean>;
  onSecret: (secret: EndpointSecret) => void;
  onToast: (message: string, kind?: "success" | "error") => void;
}) {
  const { t } = useI18n();
  const endpointQuery = useQuery(endpointQueryOptions(channel.id));
  const endpoint = endpointQuery.data;
  const [activateOpen, setActivateOpen] = useState(false);
  const [rotateOpen, setRotateOpen] = useState(false);
  const [revokeOpen, setRevokeOpen] = useState(false);
  const [users, setUsers] = useState<User[]>([]);
  const [usersLoading, setUsersLoading] = useState(false);
  const [ownerUserID, setOwnerUserID] = useState("");
  const [provider, setProvider] = useState<Provider>(
    channel.provider === "github" ? "github" : "generic",
  );
  const [events, setEvents] = useState(stringList(channel.github_events).join(", "));
  const [repositories, setRepositories] = useState(
    stringList(channel.github_repositories).join(", "),
  );
  const [actionError, setActionError] = useState("");
  const [issuing, setIssuing] = useState(false);
  const [rotating, setRotating] = useState(false);
  const [revoking, setRevoking] = useState(false);

  useEffect(() => {
    if (!activateOpen) return;
    let cancelled = false;
    setUsersLoading(true);
    setActionError("");
    void fetchAllAuthUsers()
      .then((result) => {
        if (cancelled) return;
        const activeUsers = result.filter((user) => user.is_active);
        setUsers(activeUsers);
        setOwnerUserID((current) => current || activeUsers[0]?.id || "");
      })
      .catch((error) => {
        if (!cancelled) setActionError(apiErrorMessage(error, t("channels.webhookUsersFailed")));
      })
      .finally(() => {
        if (!cancelled) setUsersLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [activateOpen, t]);

  const resetActivateForm = () => {
    setActionError("");
    setProvider(channel.provider === "github" ? "github" : "generic");
    setEvents(stringList(channel.github_events).join(", "));
    setRepositories(stringList(channel.github_repositories).join(", "));
  };

  const issue = async () => {
    const githubEvents = commaSeparatedList(events);
    const githubRepositories = commaSeparatedList(repositories);
    if (!ownerUserID) {
      setActionError(t("channels.webhookOwnerRequired"));
      return;
    }
    if (provider === "github" && (!githubEvents.length || !githubRepositories.length)) {
      setActionError(t("channels.webhookGitHubAllowlistRequired"));
      return;
    }

    setIssuing(true);
    setActionError("");
    try {
      const persisted = await onPersistConfig({
        provider,
        github_events: provider === "github" ? githubEvents : [],
        github_repositories: provider === "github" ? githubRepositories : [],
      });
      if (!persisted) return;
      const { data } = await issueWebhookEndpoint({
        path: { id: channel.id },
        body: { owner_user_id: ownerUserID, provider },
        throwOnError: true,
      });
      if (!data) throw new Error("Unable to issue webhook endpoint");
      onSecret(data);
      setActivateOpen(false);
      await endpointQuery.refetch();
      onToast(t("channels.webhookActivated"));
    } catch (error) {
      setActionError(apiErrorMessage(error, t("channels.webhookActivateFailed")));
    } finally {
      setIssuing(false);
    }
  };

  const rotate = async () => {
    setRotating(true);
    try {
      const { data } = await rotateWebhookEndpoint({
        path: { id: channel.id },
        throwOnError: true,
      });
      if (!data) throw new Error("Unable to rotate webhook endpoint");
      onSecret(data);
      setRotateOpen(false);
      await endpointQuery.refetch();
      onToast(t("channels.webhookRotated"));
    } catch (error) {
      onToast(apiErrorMessage(error, t("channels.webhookRotateFailed")), "error");
    } finally {
      setRotating(false);
    }
  };

  const revoke = async () => {
    setRevoking(true);
    try {
      await deleteWebhookEndpoint({ path: { id: channel.id }, throwOnError: true });
      setRevokeOpen(false);
      await endpointQuery.refetch();
      onToast(t("channels.webhookRevoked"));
    } catch (error) {
      onToast(apiErrorMessage(error, t("channels.webhookRevokeFailed")), "error");
    } finally {
      setRevoking(false);
    }
  };

  if (endpointQuery.isLoading) {
    return (
      <div className="flex gap-2">
        <Spinner className="size-4" />
        <span>{t("channels.webhookLoading")}</span>
      </div>
    );
  }

  if (endpointQuery.isError) {
    return <p>{t("channels.webhookLoadFailed")}</p>;
  }

  return (
    <>
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <h3>{t("channels.webhookEndpoint")}</h3>
          {endpoint ? (
            <span>{t("channels.webhookActive")}</span>
          ) : (
            <span>{t("channels.webhookInactive")}</span>
          )}
        </div>

        {endpoint ? (
          <>
            <dl className="grid gap-2">
              <div className="grid gap-1">
                <dt>{t("channels.webhookOwner")}</dt>
                <dd>{endpoint.owner_user_id}</dd>
              </div>
              <div className="grid gap-1">
                <dt>{t("channels.webhookProvider")}</dt>
                <dd>
                  {endpoint.provider === "github" ? "GitHub" : t("channels.webhookProviderGeneric")}
                </dd>
              </div>
              <div className="grid gap-1">
                <dt>{t("channels.webhookTokenLast4")}</dt>
                <dd>{endpoint.token_last4}</dd>
              </div>
              <div className="grid gap-1">
                <dt>{t("channels.webhookCreated")}</dt>
                <dd>{formatTime(endpoint.created_at)}</dd>
              </div>
              {endpoint.rotated_at && (
                <div className="grid gap-1">
                  <dt>{t("channels.webhookRotatedAt")}</dt>
                  <dd>{formatTime(endpoint.rotated_at)}</dd>
                </div>
              )}
              {endpoint.provider === "github" && (
                <>
                  <div className="grid gap-1">
                    <dt>{t("channels.webhookGitHubEvents")}</dt>
                    <dd>{endpoint.github_events.join(", ")}</dd>
                  </div>
                  <div className="grid gap-1">
                    <dt>{t("channels.webhookGitHubRepositories")}</dt>
                    <dd>{endpoint.github_repositories.join(", ")}</dd>
                  </div>
                </>
              )}
            </dl>
            <p>{t("channels.webhookRebindLocked")}</p>
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" size="sm" onClick={() => setRotateOpen(true)}>
                {t("channels.webhookRotate")}
              </Button>
              <Button variant="destructive" size="sm" onClick={() => setRevokeOpen(true)}>
                {t("channels.webhookRevoke")}
              </Button>
            </div>
          </>
        ) : (
          <>
            <p>{t("channels.webhookInactiveDescription")}</p>
            <div>
              <Button
                size="sm"
                onClick={() => {
                  resetActivateForm();
                  setActivateOpen(true);
                }}
              >
                {t("channels.webhookActivate")}
              </Button>
            </div>
          </>
        )}
      </div>

      <Dialog open={activateOpen} onOpenChange={setActivateOpen}>
        <DialogPopup>
          <DialogHeader>
            <DialogTitle>{t("channels.webhookActivate")}</DialogTitle>
            <DialogDescription>{t("channels.webhookActivateDescription")}</DialogDescription>
          </DialogHeader>
          <DialogPanel>
            <div className="flex flex-col gap-4">
              <Field>
                <FieldLabel>{t("channels.webhookOwner")}</FieldLabel>
                <Select
                  value={ownerUserID || null}
                  disabled={usersLoading || users.length === 0}
                  onValueChange={(value) => setOwnerUserID((value as string | null) ?? "")}
                >
                  <SelectTrigger>
                    <SelectValue placeholder={t("channels.webhookSelectOwner")}>
                      {(value) =>
                        value
                          ? userName(
                              users.find((user) => user.id === value) ?? ({ id: value } as User),
                            )
                          : null
                      }
                    </SelectValue>
                  </SelectTrigger>
                  <SelectPopup>
                    {users.map((user) => (
                      <SelectItem key={user.id} value={user.id}>
                        {userName(user)}
                      </SelectItem>
                    ))}
                  </SelectPopup>
                </Select>
                <FieldDescription>{t("channels.webhookOwnerDescription")}</FieldDescription>
              </Field>

              <Field>
                <FieldLabel>{t("channels.webhookProvider")}</FieldLabel>
                <Select value={provider} onValueChange={(value) => setProvider(value as Provider)}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectPopup>
                    <SelectItem value="generic">{t("channels.webhookProviderGeneric")}</SelectItem>
                    <SelectItem value="github">GitHub</SelectItem>
                  </SelectPopup>
                </Select>
                <FieldDescription>{t("channels.webhookProviderDescription")}</FieldDescription>
              </Field>

              {provider === "github" && (
                <>
                  <Field>
                    <FieldLabel>{t("channels.webhookGitHubEvents")}</FieldLabel>
                    <Input
                      nativeInput
                      value={events}
                      onChange={(event) => setEvents((event.target as HTMLInputElement).value)}
                      placeholder="push, pull_request"
                    />
                    <FieldDescription>
                      {t("channels.webhookGitHubEventsDescription")}
                    </FieldDescription>
                  </Field>
                  <Field>
                    <FieldLabel>{t("channels.webhookGitHubRepositories")}</FieldLabel>
                    <Input
                      nativeInput
                      value={repositories}
                      onChange={(event) =>
                        setRepositories((event.target as HTMLInputElement).value)
                      }
                      placeholder="owner/repository"
                    />
                    <FieldDescription>
                      {t("channels.webhookGitHubRepositoriesDescription")}
                    </FieldDescription>
                  </Field>
                </>
              )}
              {actionError && <FieldError>{actionError}</FieldError>}
            </div>
          </DialogPanel>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setActivateOpen(false)} disabled={issuing}>
              {t("common.cancel")}
            </Button>
            <Button onClick={() => void issue()} loading={issuing}>
              {t("channels.webhookActivate")}
            </Button>
          </DialogFooter>
        </DialogPopup>
      </Dialog>

      <AlertDialog open={rotateOpen} onOpenChange={setRotateOpen}>
        <AlertDialogPopup>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("channels.webhookRotate")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("channels.webhookRotateDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogClose render={<Button variant="ghost" disabled={rotating} />}>
              {t("common.cancel")}
            </AlertDialogClose>
            <Button onClick={() => void rotate()} loading={rotating}>
              {t("channels.webhookRotate")}
            </Button>
          </AlertDialogFooter>
        </AlertDialogPopup>
      </AlertDialog>

      <AlertDialog open={revokeOpen} onOpenChange={setRevokeOpen}>
        <AlertDialogPopup>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("channels.webhookRevoke")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("channels.webhookRevokeDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogClose render={<Button variant="ghost" disabled={revoking} />}>
              {t("common.cancel")}
            </AlertDialogClose>
            <Button variant="destructive" onClick={() => void revoke()} loading={revoking}>
              {t("channels.webhookRevoke")}
            </Button>
          </AlertDialogFooter>
        </AlertDialogPopup>
      </AlertDialog>
    </>
  );
}
