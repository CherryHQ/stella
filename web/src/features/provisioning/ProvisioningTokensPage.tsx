import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createProvisioningToken,
  listProvisioningTokens,
  revokeProvisioningToken,
} from "@/lib/api-client/sdk.gen";
import type { ProvisioningToken } from "@/lib/api-client/types.gen";
import { Alert, AlertDescription } from "@/components/ui/alert";
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
import { Field, FieldError, FieldLabel } from "@/components/ui/field";
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
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { SettingsEmptyState } from "@/features/settings/SettingsEmptyState";
import { SettingsGridPage } from "@/features/settings/SettingsCardGrid";
import { useToast } from "@/hooks/use-toast";
import { apiErrorMessage } from "@/lib/api-error";
import { useI18n } from "@/lib/i18n";
import { Copy, KeyRound, Plus } from "lucide-react";
import {
  ACTIVE_PROVISIONING_TOKEN_LIMIT,
  activeProvisioningTokenCount,
  provisioningTokenExpiry,
  provisioningTokenExpiryDays,
  provisioningTokenStatus,
  type ProvisioningTokenStatus,
} from "./provisioning-helpers";

const provisioningTokensQueryKey = ["provisioningTokens"] as const;

function formatTimestamp(value?: string): string {
  if (!value) return "—";
  const timestamp = new Date(value);
  return Number.isNaN(timestamp.getTime()) ? "—" : timestamp.toLocaleString();
}

function statusVariant(status: ProvisioningTokenStatus) {
  if (status === "active") return "success";
  if (status === "expired") return "warning";
  return "secondary";
}

export function ProvisioningTokensPage() {
  const { t } = useI18n();
  const { showToast } = useToast();
  const queryClient = useQueryClient();
  const {
    data: response,
    isLoading,
    error: queryError,
    refetch,
  } = useQuery({
    queryKey: provisioningTokensQueryKey,
    queryFn: async () => {
      const { data } = await listProvisioningTokens({ throwOnError: true });
      return data;
    },
  });
  const [dialogMode, setDialogMode] = useState<"create" | "secret" | null>(null);
  const [name, setName] = useState("");
  const [expiryDays, setExpiryDays] = useState(90);
  const [secret, setSecret] = useState("");
  const [formError, setFormError] = useState("");
  const [creating, setCreating] = useState(false);
  const [pendingRevoke, setPendingRevoke] = useState<ProvisioningToken | null>(null);
  const [revoking, setRevoking] = useState(false);
  const tokens = response?.provisioning_tokens ?? [];
  const activeCount = activeProvisioningTokenCount(tokens);
  const limitReached = activeCount >= ACTIVE_PROVISIONING_TOKEN_LIMIT;
  const creationBlocked = isLoading || Boolean(queryError) || limitReached;
  const expiryOptions = provisioningTokenExpiryDays.map((days) => ({
    label: t("provisioningTokens.expiryDays", { count: days }),
    value: days,
  }));

  const closeDialog = () => {
    setDialogMode(null);
    setName("");
    setExpiryDays(90);
    setSecret("");
    setFormError("");
  };

  const openCreate = () => {
    if (creationBlocked) return;
    setName("");
    setExpiryDays(90);
    setSecret("");
    setFormError("");
    setDialogMode("create");
  };

  const create = async () => {
    if (!name.trim()) {
      setFormError(t("provisioningTokens.nameRequired"));
      return;
    }
    setCreating(true);
    setFormError("");
    try {
      const { data } = await createProvisioningToken({
        body: { name: name.trim(), expires_at: provisioningTokenExpiry(expiryDays) },
        throwOnError: true,
      });
      setSecret(data.token);
      setDialogMode("secret");
      await queryClient.invalidateQueries({ queryKey: provisioningTokensQueryKey });
    } catch (error) {
      setFormError(apiErrorMessage(error, t("provisioningTokens.createFailed")));
    } finally {
      setCreating(false);
    }
  };

  const revoke = async () => {
    if (!pendingRevoke) return;
    setRevoking(true);
    try {
      await revokeProvisioningToken({ path: { id: pendingRevoke.id }, throwOnError: true });
      setPendingRevoke(null);
      await queryClient.invalidateQueries({ queryKey: provisioningTokensQueryKey });
    } catch (error) {
      showToast(apiErrorMessage(error, t("provisioningTokens.revokeFailed")), "error");
    } finally {
      setRevoking(false);
    }
  };

  const copySecret = async () => {
    try {
      await navigator.clipboard.writeText(secret);
      showToast(t("provisioningTokens.copied"));
    } catch (error) {
      showToast(apiErrorMessage(error, t("provisioningTokens.copyFailed")), "error");
    }
  };

  return (
    <>
      <SettingsGridPage
        title={t("provisioningTokens.title")}
        action={
          <Button size="sm" disabled={creationBlocked} onClick={openCreate}>
            <Plus size={16} />
            {t("provisioningTokens.create")}
          </Button>
        }
      >
        <Alert variant="info">
          <AlertDescription>{t("provisioningTokens.description")}</AlertDescription>
        </Alert>
        {limitReached && (
          <Alert variant="warning">
            <AlertDescription>{t("provisioningTokens.limitReached")}</AlertDescription>
          </Alert>
        )}
        {isLoading ? (
          <div className="flex justify-center py-8">
            <Spinner />
          </div>
        ) : queryError ? (
          <Alert variant="error">
            <AlertDescription className="flex items-center justify-between gap-3">
              <span>{apiErrorMessage(queryError, t("provisioningTokens.loadFailed"))}</span>
              <Button type="button" size="sm" variant="outline" onClick={() => void refetch()}>
                {t("common.retry")}
              </Button>
            </AlertDescription>
          </Alert>
        ) : tokens.length === 0 ? (
          <SettingsEmptyState
            icon={<KeyRound size={20} />}
            message={t("provisioningTokens.empty")}
            description={t("provisioningTokens.emptyDesc")}
            action={<Button onClick={openCreate}>{t("provisioningTokens.create")}</Button>}
          />
        ) : (
          <Table variant="card">
            <TableHeader>
              <TableRow>
                <TableHead>{t("provisioningTokens.token")}</TableHead>
                <TableHead>{t("common.status")}</TableHead>
                <TableHead>{t("provisioningTokens.createdAt")}</TableHead>
                <TableHead>{t("provisioningTokens.expiresAt")}</TableHead>
                <TableHead>{t("provisioningTokens.lastUsedAt")}</TableHead>
                <TableHead>{t("provisioningTokens.actions")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {tokens.map((token) => {
                const status = provisioningTokenStatus(token);
                return (
                  <TableRow key={token.id}>
                    <TableCell>
                      <div className="flex items-center gap-2">
                        <span>{token.name}</span>
                        <Badge size="sm" variant="outline">
                          ••••{token.last4}
                        </Badge>
                      </div>
                    </TableCell>
                    <TableCell>
                      <Badge size="sm" variant={statusVariant(status)}>
                        {t(`provisioningTokens.status.${status}`)}
                      </Badge>
                    </TableCell>
                    <TableCell>{formatTimestamp(token.created_at)}</TableCell>
                    <TableCell>{formatTimestamp(token.expires_at)}</TableCell>
                    <TableCell>{formatTimestamp(token.last_used_at)}</TableCell>
                    <TableCell>
                      {status === "active" && (
                        <Button
                          size="sm"
                          variant="destructive-outline"
                          onClick={() => setPendingRevoke(token)}
                        >
                          {t("provisioningTokens.revoke")}
                        </Button>
                      )}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </SettingsGridPage>

      <Dialog
        open={dialogMode !== null}
        onOpenChange={(open) => {
          if (!open && dialogMode !== "secret" && !creating) closeDialog();
        }}
      >
        <DialogPopup showCloseButton={dialogMode !== "secret"}>
          {dialogMode === "secret" ? (
            <>
              <DialogHeader>
                <DialogTitle>{t("provisioningTokens.secretTitle")}</DialogTitle>
                <DialogDescription>{t("provisioningTokens.secretDescription")}</DialogDescription>
              </DialogHeader>
              <DialogPanel>
                <div className="flex gap-2">
                  <Input
                    nativeInput
                    readOnly
                    value={secret}
                    aria-label={t("provisioningTokens.secretTitle")}
                  />
                  <Button
                    type="button"
                    variant="outline"
                    size="icon"
                    aria-label={t("common.copy")}
                    onClick={() => void copySecret()}
                  >
                    <Copy size={16} />
                  </Button>
                </div>
              </DialogPanel>
              <DialogFooter>
                <Button type="button" onClick={closeDialog}>
                  {t("provisioningTokens.secretDone")}
                </Button>
              </DialogFooter>
            </>
          ) : (
            <>
              <DialogHeader>
                <DialogTitle>{t("provisioningTokens.create")}</DialogTitle>
                <DialogDescription>{t("provisioningTokens.createDescription")}</DialogDescription>
              </DialogHeader>
              <form
                className="contents"
                onSubmit={(event) => {
                  event.preventDefault();
                  void create();
                }}
              >
                <DialogPanel>
                  <div className="flex flex-col gap-4">
                    <Field name="name">
                      <FieldLabel>{t("common.name")}</FieldLabel>
                      <Input
                        nativeInput
                        name="name"
                        value={name}
                        onChange={(event) => setName(event.target.value)}
                      />
                    </Field>
                    <Field name="expiry">
                      <FieldLabel>{t("provisioningTokens.expiry")}</FieldLabel>
                      <Select
                        items={expiryOptions}
                        name="expiry"
                        value={expiryDays}
                        onValueChange={(value) => {
                          if (typeof value === "number") setExpiryDays(value);
                        }}
                      >
                        <SelectTrigger>
                          <SelectValue />
                        </SelectTrigger>
                        <SelectPopup>
                          {expiryOptions.map((option) => (
                            <SelectItem key={option.value} value={option.value}>
                              {t("provisioningTokens.expiryDays", { count: option.value })}
                            </SelectItem>
                          ))}
                        </SelectPopup>
                      </Select>
                    </Field>
                    {formError && <FieldError>{formError}</FieldError>}
                  </div>
                </DialogPanel>
                <DialogFooter>
                  <DialogClose
                    render={<Button type="button" variant="ghost" disabled={creating} />}
                  >
                    {t("common.cancel")}
                  </DialogClose>
                  <Button type="submit" loading={creating}>
                    {t("provisioningTokens.create")}
                  </Button>
                </DialogFooter>
              </form>
            </>
          )}
        </DialogPopup>
      </Dialog>

      <AlertDialog
        open={pendingRevoke !== null}
        onOpenChange={(open) => {
          if (!open && !revoking) setPendingRevoke(null);
        }}
      >
        <AlertDialogPopup>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("provisioningTokens.revokeTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {pendingRevoke
                ? t("provisioningTokens.revokeDescription", { name: pendingRevoke.name })
                : ""}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogClose render={<Button type="button" variant="ghost" disabled={revoking} />}>
              {t("common.cancel")}
            </AlertDialogClose>
            <Button
              type="button"
              variant="destructive"
              loading={revoking}
              onClick={() => void revoke()}
            >
              {t("provisioningTokens.revoke")}
            </Button>
          </AlertDialogFooter>
        </AlertDialogPopup>
      </AlertDialog>
    </>
  );
}
