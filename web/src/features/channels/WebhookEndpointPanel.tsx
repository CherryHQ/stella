import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createWebhookEndpoint,
  deleteWebhookEndpoint,
  getWebhookEndpoint,
  rotateWebhookEndpoint,
} from "@/lib/api-client/sdk.gen";
import type { WebhookEndpoint } from "@/lib/api-client/types.gen";
import { apiErrorMessage } from "@/lib/api-error";
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
import { FieldError } from "@/components/ui/field";
import { Spinner } from "@/components/ui/spinner";
import { FormSectionTitle } from "@/features/settings/SettingsDetailPanel";
import { formatTime } from "@/lib/time";
import { useI18n } from "@/lib/i18n";
import { useToast, ToastContainer } from "@/hooks/use-toast";

function endpointQueryOptions(channelId: string) {
  return {
    queryKey: ["webhook-endpoint", channelId] as const,
    queryFn: async (): Promise<WebhookEndpoint | null> => {
      const { data, response } = await getWebhookEndpoint({
        path: { channelId },
      });
      if (response?.status === 404) return null;
      if (!data) throw new Error("Unable to load webhook endpoint");
      return data;
    },
  };
}

// WebhookEndpointPanel manages the singleton capability endpoint of a webhook
// channel: activation is one Create request, rotation echoes the opaque etag,
// and provider/owner are display-only once active (revoke/recreate to change
// them). The one-time capability URL is disclosed once, in a dialog, and cannot
// be recovered afterward.
export function WebhookEndpointPanel({ channelId }: { channelId: string }) {
  const { t } = useI18n();
  const { toasts, showToast } = useToast();
  const onToast = showToast;
  const queryClient = useQueryClient();
  const endpointQuery = useQuery(endpointQueryOptions(channelId));
  const endpoint = endpointQuery.data;

  const [activateOpen, setActivateOpen] = useState(false);
  const [rotateOpen, setRotateOpen] = useState(false);
  const [revokeOpen, setRevokeOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState("");
  const [oneTimeURL, setOneTimeURL] = useState("");
  const [urlCopied, setUrlCopied] = useState(false);

  const invalidate = () =>
    queryClient.invalidateQueries({
      queryKey: ["webhook-endpoint", channelId],
    });

  const openActivate = () => {
    setActionError("");
    setActivateOpen(true);
  };

  const activate = async () => {
    setBusy(true);
    setActionError("");
    try {
      const { data } = await createWebhookEndpoint({
        path: { channelId },
        body: { provider: "generic" },
        throwOnError: true,
      });
      setActivateOpen(false);
      await invalidate();
      onToast(t("channels.endpointActivated"), "success");
      if (data?.url) discloseURL(data.url);
    } catch (err) {
      setActionError(apiErrorMessage(err, t("channels.endpointActivateFailed")));
    } finally {
      setBusy(false);
    }
  };

  const rotate = async () => {
    if (!endpoint) return;
    setBusy(true);
    try {
      const { data } = await rotateWebhookEndpoint({
        path: { channelId },
        body: { etag: endpoint.etag },
        throwOnError: true,
      });
      setRotateOpen(false);
      await invalidate();
      onToast(t("channels.endpointRotated"), "success");
      if (data?.url) discloseURL(data.url);
    } catch (err) {
      setRotateOpen(false);
      onToast(apiErrorMessage(err, t("channels.endpointRotateFailed")), "error");
    } finally {
      setBusy(false);
    }
  };

  const revoke = async () => {
    setBusy(true);
    try {
      await deleteWebhookEndpoint({ path: { channelId }, throwOnError: true });
      setRevokeOpen(false);
      await invalidate();
      onToast(t("channels.endpointRevoked"), "success");
    } catch (err) {
      setRevokeOpen(false);
      onToast(apiErrorMessage(err, t("channels.endpointRevokeFailed")), "error");
    } finally {
      setBusy(false);
    }
  };

  const discloseURL = (url: string) => {
    setUrlCopied(false);
    setOneTimeURL(url);
  };

  const copyURL = () => {
    void navigator.clipboard?.writeText(oneTimeURL).then(() => {
      setUrlCopied(true);
      setTimeout(() => setUrlCopied(false), 1500);
    });
  };

  return (
    <div className="space-y-3">
      <FormSectionTitle>{t("channels.endpointTitle")}</FormSectionTitle>

      {endpointQuery.isLoading ? (
        <Spinner />
      ) : endpoint ? (
        <>
          <dl className="grid gap-2 text-sm">
            <div className="grid gap-0.5">
              <dt className="text-xs text-muted-foreground">{t("channels.endpointOwner")}</dt>
              <dd className="font-mono break-all">{endpoint.owner_user_id}</dd>
            </div>
            <div className="grid gap-0.5">
              <dt className="text-xs text-muted-foreground">{t("channels.endpointProvider")}</dt>
              <dd>{t("channels.endpointProviderGeneric")}</dd>
            </div>
            <div className="grid gap-0.5">
              <dt className="text-xs text-muted-foreground">{t("channels.endpointTokenLast4")}</dt>
              <dd className="font-mono">••••{endpoint.token_last4}</dd>
            </div>
            <div className="grid gap-0.5">
              <dt className="text-xs text-muted-foreground">{t("channels.endpointCreated")}</dt>
              <dd>{formatTime(endpoint.created_at)}</dd>
            </div>
            {endpoint.rotated_at && (
              <div className="grid gap-0.5">
                <dt className="text-xs text-muted-foreground">{t("channels.endpointRotatedAt")}</dt>
                <dd>{formatTime(endpoint.rotated_at)}</dd>
              </div>
            )}
          </dl>
          <p className="text-xs text-muted-foreground">{t("channels.endpointRebindLocked")}</p>
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" size="sm" onClick={() => setRotateOpen(true)}>
              {t("channels.endpointRotate")}
            </Button>
            <Button variant="destructive" size="sm" onClick={() => setRevokeOpen(true)}>
              {t("channels.endpointRevoke")}
            </Button>
          </div>
        </>
      ) : (
        <>
          <p className="text-sm text-muted-foreground">{t("channels.endpointInactiveDesc")}</p>
          <Button size="sm" onClick={openActivate}>
            {t("channels.endpointActivate")}
          </Button>
        </>
      )}

      <Dialog open={activateOpen} onOpenChange={setActivateOpen}>
        <DialogPopup>
          <DialogHeader>
            <DialogTitle>{t("channels.endpointActivate")}</DialogTitle>
            <DialogDescription>{t("channels.endpointActivateDesc")}</DialogDescription>
          </DialogHeader>
          <DialogPanel>
            <div className="flex flex-col gap-4">
              <p className="text-sm text-muted-foreground">{t("channels.endpointOwnerDesc")}</p>
              {actionError && <FieldError>{actionError}</FieldError>}
            </div>
          </DialogPanel>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setActivateOpen(false)} disabled={busy}>
              {t("common.cancel")}
            </Button>
            <Button onClick={() => void activate()} loading={busy}>
              {t("channels.endpointActivate")}
            </Button>
          </DialogFooter>
        </DialogPopup>
      </Dialog>

      <AlertDialog open={rotateOpen} onOpenChange={setRotateOpen}>
        <AlertDialogPopup>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("channels.endpointRotate")}</AlertDialogTitle>
            <AlertDialogDescription>{t("channels.endpointRotateDesc")}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogClose render={<Button variant="ghost" disabled={busy} />}>
              {t("common.cancel")}
            </AlertDialogClose>
            <Button onClick={() => void rotate()} loading={busy}>
              {t("channels.endpointRotate")}
            </Button>
          </AlertDialogFooter>
        </AlertDialogPopup>
      </AlertDialog>

      <AlertDialog open={revokeOpen} onOpenChange={setRevokeOpen}>
        <AlertDialogPopup>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("channels.endpointRevoke")}</AlertDialogTitle>
            <AlertDialogDescription>{t("channels.endpointRevokeDesc")}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogClose render={<Button variant="ghost" disabled={busy} />}>
              {t("common.cancel")}
            </AlertDialogClose>
            <Button variant="destructive" onClick={() => void revoke()} loading={busy}>
              {t("channels.endpointRevoke")}
            </Button>
          </AlertDialogFooter>
        </AlertDialogPopup>
      </AlertDialog>

      <Dialog open={oneTimeURL !== ""} onOpenChange={(open) => !open && setOneTimeURL("")}>
        <DialogPopup>
          <DialogHeader>
            <DialogTitle>{t("channels.endpointUrlTitle")}</DialogTitle>
            <DialogDescription>{t("channels.endpointUrlDesc")}</DialogDescription>
          </DialogHeader>
          <DialogPanel>
            <div className="flex items-center gap-2 flex-wrap">
              <code className="font-mono text-sm bg-muted text-foreground px-3 py-1 rounded select-all break-all">
                {oneTimeURL}
              </code>
              <Button onClick={copyURL} variant="ghost" size="xs">
                {urlCopied ? t("channels.copied") : t("channels.copy")}
              </Button>
            </div>
          </DialogPanel>
          <DialogFooter>
            <Button onClick={() => setOneTimeURL("")}>{t("common.done")}</Button>
          </DialogFooter>
        </DialogPopup>
      </Dialog>

      <ToastContainer messages={toasts} />
    </div>
  );
}
