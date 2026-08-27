import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createWebhook,
  deleteWebhook,
  listWebhooks,
  rotateWebhook,
  updateWebhook,
} from "@/lib/api-client/sdk.gen";
import type { Webhook } from "@/lib/api-client/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Field, FieldError, FieldLabel } from "@/components/ui/field";
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
import { Badge } from "@/components/ui/badge";
import {
  AlertDialog,
  AlertDialogClose,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogPopup,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import {
  Select,
  SelectItem,
  SelectPopup,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  SettingsCard,
  SettingsCardSection,
  SettingsGridPage,
} from "@/features/settings/SettingsCardGrid";
import { SettingsEmptyState } from "@/features/settings/SettingsEmptyState";
import { Spinner } from "@/components/ui/spinner";
import { ErrorState } from "@/components/RouteFallback";
import { useToast } from "@/hooks/use-toast";
import { useI18n } from "@/lib/i18n";
import { agentsQueryOptions } from "@/lib/queries/agents";
import { errorMessage } from "@/lib/utils";
import { Copy, Plus, RotateCw, Trash2, Webhook as WebhookIcon } from "lucide-react";

type Draft = { name: string; agentID: string; enabled: boolean; wait: string; run: string };
const emptyDraft = (): Draft => ({ name: "", agentID: "", enabled: true, wait: "60", run: "300" });

const webhooksQueryKey = ["webhooks"] as const;

async function fetchAllWebhooks(): Promise<Webhook[]> {
  const items: Webhook[] = [];
  let pageToken: string | undefined;
  do {
    const { data } = await listWebhooks({
      query: { page_size: 500, page_token: pageToken },
      throwOnError: true,
    });
    items.push(...data.webhooks);
    pageToken = data.next_page_token ?? undefined;
  } while (pageToken);
  return items;
}

export function WebhooksPage() {
  const { t } = useI18n();
  const { showToast } = useToast();
  const queryClient = useQueryClient();
  const {
    data: items = [],
    isLoading: loading,
    error: webhooksError,
    refetch: refetchWebhooks,
  } = useQuery({ queryKey: webhooksQueryKey, queryFn: fetchAllWebhooks });
  const { data: agents = [], error: agentsError } = useQuery(agentsQueryOptions);
  const [draft, setDraft] = useState<Draft>(emptyDraft);
  const [selected, setSelected] = useState<Webhook | null>(null);
  const [dialogMode, setDialogMode] = useState<"editor" | "secret" | null>(null);
  const [secretURL, setSecretURL] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [pendingDelete, setPendingDelete] = useState<Webhook | null>(null);

  useEffect(() => {
    const queryError = webhooksError ?? agentsError;
    if (queryError) {
      showToast(queryError instanceof Error ? queryError.message : String(queryError), "error");
    }
  }, [webhooksError, agentsError, showToast]);

  const edit = (item: Webhook) => {
    setSelected(item);
    setDraft({
      name: item.name,
      agentID: item.agent_id,
      enabled: item.is_enabled,
      wait: String(item.wait_timeout_seconds),
      run: String(item.max_run_timeout_seconds),
    });
    setError("");
    setDialogMode("editor");
  };
  const create = () => {
    setSelected(null);
    setDraft(emptyDraft());
    setError("");
    setDialogMode("editor");
  };
  const save = async () => {
    const wait = Number(draft.wait);
    const run = Number(draft.run);
    const agentChanged = !selected || selected.agent_id !== draft.agentID;
    if (
      !draft.name.trim() ||
      !draft.agentID.trim() ||
      (agentChanged && !agents.some((agent) => agent.id === draft.agentID)) ||
      !Number.isInteger(wait) ||
      !Number.isInteger(run)
    ) {
      setError(t("webhooks.validation"));
      return;
    }
    setSaving(true);
    setError("");
    try {
      let createdURL = "";
      if (selected) {
        await updateWebhook({
          path: { id: selected.id },
          body: {
            name: draft.name.trim(),
            ...(agentChanged ? { agent_id: draft.agentID.trim() } : undefined),
            is_enabled: draft.enabled,
            wait_timeout_seconds: wait,
            max_run_timeout_seconds: run,
          },
          throwOnError: true,
        });
      } else {
        const { data } = await createWebhook({
          body: {
            name: draft.name.trim(),
            agent_id: draft.agentID.trim(),
            is_enabled: draft.enabled,
            wait_timeout_seconds: wait,
            max_run_timeout_seconds: run,
          },
          throwOnError: true,
        });
        createdURL = data.url || "";
      }
      // Editor and one-time disclosure are two states of one dialog, never two
      // overlapping modal roots.
      if (createdURL) {
        setSecretURL(createdURL);
        setDialogMode("secret");
      } else {
        setDialogMode(null);
      }
      await queryClient.invalidateQueries({ queryKey: webhooksQueryKey });
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setSaving(false);
    }
  };
  const rotate = async (item: Webhook) => {
    try {
      const { data } = await rotateWebhook({
        path: { id: item.id },
        body: { etag: item.etag },
        throwOnError: true,
      });
      setSecretURL(data.url || "");
      setDialogMode("secret");
      await queryClient.invalidateQueries({ queryKey: webhooksQueryKey });
    } catch (err) {
      showToast(errorMessage(err), "error");
    }
  };
  const remove = async () => {
    if (!pendingDelete) return;
    try {
      await deleteWebhook({ path: { id: pendingDelete.id }, throwOnError: true });
      setPendingDelete(null);
      await queryClient.invalidateQueries({ queryKey: webhooksQueryKey });
    } catch (err) {
      showToast(errorMessage(err), "error");
    }
  };
  const agentItems = agents.map((agent) => ({ label: agent.name, value: agent.id }));
  if (draft.agentID && !agentItems.some((agent) => agent.value === draft.agentID)) {
    agentItems.push({
      label: `${draft.agentID} (${t("webhooks.agentUnavailable")})`,
      value: draft.agentID,
    });
  }
  const updateDraft = (key: keyof Draft, value: string | boolean) =>
    setDraft((current) => ({ ...current, [key]: value }));

  return (
    <>
      <SettingsGridPage
        title={t("webhooks.title")}
        action={
          <Button size="sm" onClick={create}>
            <Plus size={16} />
            {t("webhooks.create")}
          </Button>
        }
      >
        {loading ? (
          <div className="flex justify-center py-8">
            <Spinner />
          </div>
        ) : webhooksError ? (
          // The toast below fires too, but it is gone in 3s while the page kept
          // insisting "no webhooks yet" — the failure has to own the body.
          <ErrorState
            title={t("route.error.title")}
            description={t("route.loadFailed")}
            onRetry={() => void refetchWebhooks()}
          />
        ) : items.length === 0 ? (
          <SettingsEmptyState
            icon={<WebhookIcon size={20} />}
            message={t("webhooks.empty")}
            description={t("webhooks.emptyDesc")}
            action={<Button onClick={create}>{t("webhooks.create")}</Button>}
          />
        ) : (
          <SettingsCardSection title={t("webhooks.configured")} count={items.length}>
            {items.map((item) => (
              <SettingsCard
                key={item.id}
                icon={<WebhookIcon size={16} />}
                title={item.name}
                description={
                  agents.find((agent) => agent.id === item.agent_id)?.name ?? item.agent_id
                }
                onClick={() => edit(item)}
                badge={
                  <Badge size="sm" variant={item.is_enabled ? "success" : "secondary"}>
                    {item.is_enabled ? t("webhooks.enabled") : t("webhooks.disabled")}
                  </Badge>
                }
                action={
                  <div className="flex gap-1">
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-sm"
                      aria-label={t("webhooks.rotate")}
                      onClick={() => void rotate(item)}
                    >
                      <RotateCw size={16} />
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-sm"
                      aria-label={t("common.delete")}
                      onClick={() => setPendingDelete(item)}
                    >
                      <Trash2 size={16} />
                    </Button>
                  </div>
                }
                footer={<span>••••{item.token_last4}</span>}
              />
            ))}
          </SettingsCardSection>
        )}
      </SettingsGridPage>

      <Dialog
        open={dialogMode !== null}
        onOpenChange={(open) => {
          if (!open) {
            setDialogMode(null);
            setSecretURL("");
          }
        }}
      >
        <DialogPopup>
          {dialogMode === "secret" ? (
            <>
              <DialogHeader>
                <DialogTitle>{t("webhooks.urlTitle")}</DialogTitle>
                <DialogDescription>{t("webhooks.urlDesc")}</DialogDescription>
              </DialogHeader>
              <DialogPanel>
                <div className="flex gap-2">
                  <Input nativeInput readOnly value={secretURL} />
                  <Button
                    type="button"
                    variant="outline"
                    size="icon"
                    aria-label={t("webhooks.copy")}
                    onClick={() => void navigator.clipboard.writeText(secretURL)}
                  >
                    <Copy size={16} />
                  </Button>
                </div>
              </DialogPanel>
              <DialogFooter>
                <Button
                  type="button"
                  onClick={() => {
                    setDialogMode(null);
                    setSecretURL("");
                  }}
                >
                  {t("common.done")}
                </Button>
              </DialogFooter>
            </>
          ) : (
            <>
              <DialogHeader>
                <DialogTitle>{selected ? t("webhooks.edit") : t("webhooks.create")}</DialogTitle>
                <DialogDescription>{t("webhooks.editorDesc")}</DialogDescription>
              </DialogHeader>
              <form
                className="contents"
                onSubmit={(event) => {
                  event.preventDefault();
                  void save();
                }}
              >
                <DialogPanel>
                  <div className="flex flex-col gap-4">
                    <Field>
                      <FieldLabel>{t("webhooks.name")}</FieldLabel>
                      <Input
                        nativeInput
                        value={draft.name}
                        onChange={(event) => updateDraft("name", event.target.value)}
                      />
                    </Field>
                    <Field>
                      <FieldLabel>{t("webhooks.agent")}</FieldLabel>
                      <Select
                        items={agentItems}
                        value={draft.agentID || null}
                        // SAFETY: the Select's onValueChange value is a string option; null falls back to empty.
                        onValueChange={(value) => updateDraft("agentID", (value ?? "") as string)}
                      >
                        <SelectTrigger disabled={agents.length === 0}>
                          <SelectValue placeholder={t("webhooks.selectAgent")} />
                        </SelectTrigger>
                        <SelectPopup>
                          {agentItems.map((agent) => (
                            <SelectItem
                              key={agent.value}
                              value={agent.value}
                              disabled={!agents.some((item) => item.id === agent.value)}
                            >
                              {agent.label}
                            </SelectItem>
                          ))}
                        </SelectPopup>
                      </Select>
                    </Field>
                    <Field>
                      <FieldLabel>{t("webhooks.waitTimeout")}</FieldLabel>
                      <Input
                        nativeInput
                        type="number"
                        min="1"
                        max="600"
                        value={draft.wait}
                        onChange={(event) => updateDraft("wait", event.target.value)}
                      />
                    </Field>
                    <Field>
                      <FieldLabel>{t("webhooks.runTimeout")}</FieldLabel>
                      <Input
                        nativeInput
                        type="number"
                        min="1"
                        max="3600"
                        value={draft.run}
                        onChange={(event) => updateDraft("run", event.target.value)}
                      />
                    </Field>
                    <div className="flex items-center gap-2">
                      <Switch
                        checked={draft.enabled}
                        onCheckedChange={(value) => updateDraft("enabled", value)}
                      />
                      <span>{t("webhooks.enabled")}</span>
                    </div>
                    {error && <FieldError>{error}</FieldError>}
                  </div>
                </DialogPanel>
                <DialogFooter>
                  <DialogClose render={<Button type="button" variant="ghost" disabled={saving} />}>
                    {t("common.cancel")}
                  </DialogClose>
                  <Button
                    type="submit"
                    loading={saving}
                    disabled={!selected && agents.length === 0}
                  >
                    {t("common.save")}
                  </Button>
                </DialogFooter>
              </form>
            </>
          )}
        </DialogPopup>
      </Dialog>

      <AlertDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => {
          if (!open) setPendingDelete(null);
        }}
      >
        <AlertDialogPopup>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("webhooks.deleteTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {pendingDelete ? t("webhooks.deleteConfirm", { name: pendingDelete.name }) : ""}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogClose render={<Button type="button" variant="ghost" />}>
              {t("common.cancel")}
            </AlertDialogClose>
            <Button type="button" variant="destructive" onClick={() => void remove()}>
              {t("common.delete")}
            </Button>
          </AlertDialogFooter>
        </AlertDialogPopup>
      </AlertDialog>
    </>
  );
}
