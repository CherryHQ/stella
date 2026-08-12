import { useRef, useState, type ReactNode } from "react";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { FileText, Library, Search, Upload } from "lucide-react";
import { createLibraryFile, deleteLibraryFile } from "@/lib/api-client/sdk.gen";
import type { LibraryFile, LibraryFileScope, LibraryFileStatus } from "@/lib/api-client/types.gen";
import { apiErrorMessage } from "@/lib/api-error";
import { useI18n } from "@/lib/i18n";
import { agentsQueryOptions, allAgentsAdminQueryOptions } from "@/lib/queries/agents";
import {
  flattenLibraryFilePages,
  libraryFilesInfiniteQueryOptions,
} from "@/lib/queries/library-files";
import type { ScopeBand } from "@/lib/scope-band";
import { useToast } from "@/hooks/use-toast";
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
import { Field, FieldLabel } from "@/components/ui/field";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import {
  Select,
  SelectItem,
  SelectPopup,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { SettingsList, SettingsRow, SettingsSection } from "@/features/settings/SettingsCardGrid";
import { SettingsEmptyState } from "@/features/settings/SettingsEmptyState";
import { SettingsPageHeader } from "@/features/settings/SettingsPageHeader";

type UploadState = "queued" | "uploading" | "success" | "error";

interface UploadResult {
  id: string;
  file: File;
  state: UploadState;
  message?: string;
}

interface LibraryFilesViewProps {
  scope: LibraryFileScope;
  agentID?: string;
  query: string;
  title: string;
  description: string;
  controls?: ReactNode;
  onQueryChange: (query: string) => void;
}

const statusVariant: Record<LibraryFileStatus, "warning" | "success" | "error"> = {
  processing: "warning",
  ready: "success",
  failed: "error",
};

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KiB", "MiB", "GiB"];
  let value = bytes / 1024;
  let unit = units[0];
  for (let index = 1; index < units.length && value >= 1024; index += 1) {
    value /= 1024;
    unit = units[index];
  }
  const precision = Number.isInteger(value) || value >= 10 ? 0 : 1;
  return `${value.toFixed(precision)} ${unit}`;
}

function LibraryFilesView({
  scope,
  agentID,
  query,
  title,
  description,
  controls,
  onQueryChange,
}: LibraryFilesViewProps) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { showToast } = useToast();
  const fileInput = useRef<HTMLInputElement>(null);
  const [uploads, setUploads] = useState<UploadResult[]>([]);
  const [uploadDialogOpen, setUploadDialogOpen] = useState(false);
  const [pendingDelete, setPendingDelete] = useState<LibraryFile | null>(null);
  const needsAgent = scope === "user_agent" || scope === "system_agent";
  const targetReady = !needsAgent || !!agentID;
  const result = useInfiniteQuery(libraryFilesInfiniteQueryOptions({ scope, agentID, query }));
  const files = flattenLibraryFilePages(result.data?.pages);
  const quota = result.data?.pages[0]?.quota;
  const uploadRunning = uploads.some(
    (item) => item.state === "queued" || item.state === "uploading",
  );

  async function uploadSelected(selected: File[]) {
    const batch = selected.map((file, index) => ({
      id: `${file.name}-${file.lastModified}-${index}`,
      file,
      state: "queued" as const,
    }));
    setUploads(batch);
    setUploadDialogOpen(true);
    let successes = 0;
    for (const item of batch) {
      setUploads((current) =>
        current.map((candidate) =>
          candidate.id === item.id ? { ...candidate, state: "uploading" } : candidate,
        ),
      );
      try {
        await createLibraryFile({
          query: { scope, ...(agentID ? { agent_id: agentID } : {}) },
          body: { file: item.file },
          throwOnError: true,
        });
        successes += 1;
        setUploads((current) =>
          current.map((candidate) =>
            candidate.id === item.id ? { ...candidate, state: "success" } : candidate,
          ),
        );
      } catch (error) {
        setUploads((current) =>
          current.map((candidate) =>
            candidate.id === item.id
              ? {
                  ...candidate,
                  state: "error",
                  message: apiErrorMessage(error, t("library.upload.failed")),
                }
              : candidate,
          ),
        );
      }
    }
    if (successes > 0) {
      await queryClient.invalidateQueries({ queryKey: ["library-files"] });
    }
    showToast(
      t("library.upload.summary", {
        success: successes,
        total: batch.length,
      }),
      successes === batch.length ? "success" : "error",
    );
  }

  async function remove(file: LibraryFile) {
    try {
      await deleteLibraryFile({ path: { id: file.id }, throwOnError: true });
      await queryClient.invalidateQueries({ queryKey: ["library-files"] });
      showToast(t("library.delete.success"), "success");
    } catch (error) {
      showToast(apiErrorMessage(error, t("library.delete.failed")), "error");
    }
  }

  const uploadButton = (
    <Button type="button" disabled={!targetReady} onClick={() => fileInput.current?.click()}>
      <Upload aria-hidden="true" />
      {t("library.upload.action")}
    </Button>
  );

  return (
    <>
      <input
        ref={fileInput}
        className="hidden"
        type="file"
        multiple
        accept=".pdf,.docx,.md,.markdown,.txt"
        aria-label={t("library.upload.action")}
        onChange={(event) => {
          const selected = Array.from(event.currentTarget.files ?? []);
          event.currentTarget.value = "";
          if (selected.length > 0) void uploadSelected(selected);
        }}
      />
      <div className="h-full min-h-0 overflow-y-auto">
        <div className="mx-auto max-w-5xl space-y-8 p-6">
          <SettingsPageHeader title={title} description={description} action={uploadButton} />
          {controls}
          <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
            <InputGroup className="w-full sm:max-w-md">
              <InputGroupInput
                nativeInput
                type="search"
                value={query}
                disabled={!targetReady}
                placeholder={t("library.search.placeholder")}
                aria-label={t("library.search.placeholder")}
                onChange={(event) => onQueryChange((event.target as HTMLInputElement).value)}
              />
              <InputGroupAddon>
                <Search aria-hidden="true" />
              </InputGroupAddon>
            </InputGroup>
            {quota ? (
              <p className="shrink-0 text-sm text-muted-foreground">
                {t("library.quota", {
                  usedFiles: quota.used_files,
                  maxFiles: quota.max_files,
                  usedBytes: formatBytes(quota.used_bytes),
                  maxBytes: formatBytes(quota.max_bytes),
                })}
              </p>
            ) : null}
          </div>

          {!targetReady ? (
            <SettingsEmptyState
              icon={<Library aria-hidden="true" />}
              message={t("library.agent.required")}
              description={t("library.agent.requiredDesc")}
            />
          ) : result.isPending ? (
            <div className="flex justify-center py-12">
              <Spinner />
            </div>
          ) : result.isError ? (
            <SettingsEmptyState
              icon={<Library aria-hidden="true" />}
              message={t("library.load.failed")}
              description={apiErrorMessage(result.error, t("library.load.failedDesc"))}
              action={
                <Button type="button" variant="outline" onClick={() => void result.refetch()}>
                  {t("common.retry")}
                </Button>
              }
            />
          ) : files.length === 0 ? (
            <SettingsEmptyState
              icon={<Library aria-hidden="true" />}
              message={query ? t("library.empty.search") : t("library.empty.title")}
              description={query ? t("library.empty.searchDesc") : t("library.empty.description")}
              action={query ? undefined : uploadButton}
            />
          ) : (
            <SettingsSection title={t("library.files")} count={files.length}>
              <SettingsList>
                {files.map((file) => (
                  <SettingsRow
                    key={file.id}
                    icon={<FileText aria-hidden="true" />}
                    title={file.file_name}
                    subtitle={
                      file.status === "failed" && file.error_message
                        ? file.error_message
                        : `${formatBytes(file.size_bytes)} · ${new Date(file.created_at).toLocaleString()}`
                    }
                    status={
                      <Badge size="sm" variant={statusVariant[file.status]}>
                        {t(`library.status.${file.status}`)}
                      </Badge>
                    }
                    menu={[
                      {
                        label: t("common.delete"),
                        destructive: true,
                        onClick: () => setPendingDelete(file),
                      },
                    ]}
                  />
                ))}
              </SettingsList>
              {result.hasNextPage ? (
                <div className="flex justify-center pt-3">
                  <Button
                    type="button"
                    variant="outline"
                    loading={result.isFetchingNextPage}
                    onClick={() => void result.fetchNextPage()}
                  >
                    {t("common.loadMore")}
                  </Button>
                </div>
              ) : null}
            </SettingsSection>
          )}
        </div>
      </div>

      <Dialog
        open={uploadDialogOpen}
        onOpenChange={(open) => {
          if (!open && !uploadRunning) setUploadDialogOpen(false);
        }}
      >
        <DialogPopup>
          <DialogHeader>
            <DialogTitle>{t("library.upload.title")}</DialogTitle>
            <DialogDescription>{t("library.upload.description")}</DialogDescription>
          </DialogHeader>
          <DialogPanel>
            <div className="divide-y divide-border overflow-hidden rounded-xl border border-border">
              {uploads.map((item) => (
                <div key={item.id} className="flex items-center gap-3 px-4 py-3">
                  <FileText className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm font-medium">{item.file.name}</p>
                    {item.message ? (
                      <p className="truncate text-xs text-destructive-foreground">{item.message}</p>
                    ) : null}
                  </div>
                  {item.state === "uploading" ? (
                    <Spinner />
                  ) : (
                    <Badge
                      size="sm"
                      variant={
                        item.state === "success"
                          ? "success"
                          : item.state === "error"
                            ? "error"
                            : "secondary"
                      }
                    >
                      {t(`library.upload.state.${item.state}`)}
                    </Badge>
                  )}
                </div>
              ))}
            </div>
          </DialogPanel>
          <DialogFooter>
            <DialogClose render={<Button type="button" disabled={uploadRunning} />}>
              {t("common.done")}
            </DialogClose>
          </DialogFooter>
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
            <AlertDialogTitle>{t("library.delete.title")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("library.delete.description", {
                name: pendingDelete?.file_name ?? "",
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogClose render={<Button type="button" variant="ghost" />}>
              {t("common.cancel")}
            </AlertDialogClose>
            <AlertDialogClose
              render={
                <Button
                  type="button"
                  variant="destructive"
                  onClick={() => pendingDelete && void remove(pendingDelete)}
                />
              }
            >
              {t("common.delete")}
            </AlertDialogClose>
          </AlertDialogFooter>
        </AlertDialogPopup>
      </AlertDialog>
    </>
  );
}

interface LibrarySettingsSearch {
  scope?: "system" | "system_agent";
  agent?: string;
  q?: string;
}

export function ScopedSettingsLibraryPage({ scopeBand }: { scopeBand: ScopeBand }) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as LibrarySettingsSearch;
  const systemSurface = scopeBand === "system";
  const scope: LibraryFileScope = systemSurface ? (search.scope ?? "system") : "user";
  const { data: agents = [] } = useQuery(
    allAgentsAdminQueryOptions(systemSurface && scope === "system_agent"),
  );
  const agentID = scope === "system_agent" ? search.agent : undefined;
  const query = search.q ?? "";

  function go(next: LibrarySettingsSearch, replace = false) {
    if (systemSurface) {
      void navigate({ to: "/admin/resources/library", search: next, replace });
      return;
    }
    void navigate({ to: "/settings/library", search: next.q ? { q: next.q } : {}, replace });
  }

  const scopeItems = [
    { value: "system", label: t("library.scope.system") },
    { value: "system_agent", label: t("library.scope.systemAgent") },
  ];
  const agentItems = agents.map((agent) => ({
    value: agent.id,
    label: agent.name,
  }));
  const controls = systemSurface ? (
    <div className="grid gap-4 sm:grid-cols-2">
      <Field>
        <FieldLabel>{t("library.scope.label")}</FieldLabel>
        <Select
          items={scopeItems}
          value={scope}
          onValueChange={(value) => {
            const nextScope = (value ?? "system") as LibraryFileScope;
            go({
              ...(nextScope === "system" || nextScope === "system_agent"
                ? { scope: nextScope }
                : {}),
              ...(query ? { q: query } : {}),
            });
          }}
        >
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectPopup>
            {scopeItems.map((item) => (
              <SelectItem key={item.value} value={item.value}>
                {item.label}
              </SelectItem>
            ))}
          </SelectPopup>
        </Select>
      </Field>
      {scope === "system_agent" ? (
        <Field>
          <FieldLabel>{t("library.agent.label")}</FieldLabel>
          <Select
            items={agentItems}
            value={agentID ?? null}
            onValueChange={(value) =>
              go({
                scope: "system_agent",
                ...(value ? { agent: value } : {}),
                ...(query ? { q: query } : {}),
              })
            }
          >
            <SelectTrigger disabled={agentItems.length === 0}>
              <SelectValue placeholder={t("library.agent.select")} />
            </SelectTrigger>
            <SelectPopup>
              {agentItems.map((item) => (
                <SelectItem key={item.value} value={item.value}>
                  {item.label}
                </SelectItem>
              ))}
            </SelectPopup>
          </Select>
        </Field>
      ) : null}
    </div>
  ) : undefined;

  const description =
    scope === "system"
      ? t("library.description.system")
      : scope === "system_agent"
        ? t("library.description.systemAgent")
        : t("library.description.user");
  return (
    <LibraryFilesView
      scope={scope}
      agentID={agentID}
      query={query}
      title={t("library.title")}
      description={description}
      controls={controls}
      onQueryChange={(next) =>
        go(
          {
            ...(scope === "system" || scope === "system_agent" ? { scope } : {}),
            ...(agentID ? { agent: agentID } : {}),
            ...(next ? { q: next } : {}),
          },
          true,
        )
      }
    />
  );
}

export function SettingsLibraryPage() {
  return <ScopedSettingsLibraryPage scopeBand="personal" />;
}

export function GlobalLibraryPage() {
  return <ScopedSettingsLibraryPage scopeBand="system" />;
}

interface AgentLibrarySearch {
  q?: string;
}

export function AgentLibraryPage() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const { agentId } = useParams({ from: "/_app/agents/$agentId" });
  const search = useSearch({ strict: false }) as AgentLibrarySearch;
  const { data: agents = [] } = useQuery(agentsQueryOptions);
  const agentName = agents.find((agent) => agent.id === agentId)?.name ?? agentId;
  const query = search.q ?? "";
  return (
    <LibraryFilesView
      scope="user_agent"
      agentID={agentId}
      query={query}
      title={t("library.title")}
      description={t("library.description.userAgent", { agent: agentName })}
      onQueryChange={(next) =>
        void navigate({
          to: "/agents/$agentId/library",
          params: { agentId },
          search: next ? { q: next } : {},
          replace: true,
        })
      }
    />
  );
}
