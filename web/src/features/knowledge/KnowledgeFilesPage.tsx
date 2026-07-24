import { useState, type FormEvent } from "react";
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type InfiniteData,
} from "@tanstack/react-query";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { Bot, Info, Library, Search, Trash2, Upload } from "lucide-react";
import { deleteKnowledgeFile } from "@/lib/api-client/sdk.gen";
import type {
  KnowledgeFile,
  KnowledgeFileList,
  KnowledgeFileScope,
  KnowledgeFileStatus,
} from "@/lib/api-client/types.gen";
import { apiErrorMessage } from "@/lib/api-error";
import { formatTime } from "@/lib/time";
import { useI18n } from "@/lib/i18n";
import {
  knowledgeAdminAgentsQueryOptions,
  knowledgeFilesInfiniteQueryOptions,
  knowledgeFilesQueryKey,
} from "@/lib/queries/knowledge-files";
import { meQueryOptions } from "@/lib/queries/me";
import { useToast, ToastContainer } from "@/hooks/use-toast";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
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
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectItem,
  SelectPopup,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Tabs, TabsList, TabsTab } from "@/components/ui/tabs";
import { SettingsEmptyState } from "@/features/settings/SettingsEmptyState";
import { SettingsPageHeader } from "@/features/settings/SettingsPageHeader";
import { KnowledgeUploadDialog } from "./KnowledgeUploadDialog";

type SettingsScope = "user" | "system" | "system_agent";

interface KnowledgeFilesManagerProps {
  scope: KnowledgeFileScope;
  agentId?: string;
  q?: string;
  onQueryChange: (q: string) => void;
}

const STATUS_VARIANT: Record<KnowledgeFileStatus, "warning" | "success" | "error"> = {
  processing: "warning",
  ready: "success",
  failed: "error",
};

export function AgentKnowledgePage() {
  const { t } = useI18n();
  const { agentId } = useParams({
    from: "/_app/agents/$agentId/knowledge",
  });
  const search = useSearch({
    from: "/_app/agents/$agentId/knowledge",
  });
  const navigate = useNavigate({
    from: "/agents/$agentId/knowledge",
  });

  return (
    <div className="h-full overflow-y-auto bg-background">
      <div className="mx-auto max-w-5xl space-y-8 p-6 sm:p-8 lg:p-10">
        <SettingsPageHeader
          title={t("knowledge.title")}
          description={t("knowledge.agentDescription")}
        />
        <KnowledgeFilesManager
          scope="user_agent"
          agentId={agentId}
          q={search.q}
          onQueryChange={(q) => {
            void navigate({
              search: { q: q || undefined },
              replace: true,
            });
          }}
        />
      </div>
    </div>
  );
}

export function SettingsKnowledgePage() {
  const { t } = useI18n();
  const { data: me } = useQuery(meQueryOptions);
  const isAdmin = me?.is_admin ?? false;
  const search = useSearch({ from: "/_app/settings/knowledge" });
  const navigate = useNavigate({ from: "/settings/knowledge" });
  const scope: SettingsScope = isAdmin ? search.scope : "user";
  const agentId = scope === "system_agent" ? search.agent_id : undefined;
  const { data: agents = [], isLoading: agentsLoading } = useQuery({
    ...knowledgeAdminAgentsQueryOptions,
    enabled: isAdmin && scope === "system_agent",
  });

  const changeScope = (nextScope: SettingsScope) => {
    void navigate({
      search: {
        scope: nextScope,
        agent_id: undefined,
        q: undefined,
      },
      replace: true,
    });
  };

  return (
    <div className="h-full overflow-y-auto bg-background">
      <div className="mx-auto max-w-5xl space-y-8 p-6 sm:p-8 lg:p-10">
        <SettingsPageHeader
          title={t("knowledge.title")}
          description={
            scope === "user" ? t("knowledge.userDescription") : t("knowledge.settingsDescription")
          }
        />

        {isAdmin && (
          <Tabs value={scope} onValueChange={(value) => changeScope(value as SettingsScope)}>
            <TabsList className="max-w-full overflow-x-auto">
              <TabsTab value="user">{t("knowledge.scope.user")}</TabsTab>
              <TabsTab value="system">{t("knowledge.scope.system")}</TabsTab>
              <TabsTab value="system_agent">{t("knowledge.scope.systemAgent")}</TabsTab>
            </TabsList>
          </Tabs>
        )}

        {isAdmin && scope === "system_agent" && (
          <div className="max-w-sm space-y-2">
            <label className="text-sm font-medium">{t("knowledge.scope.agent")}</label>
            <Select
              value={agentId || null}
              onValueChange={(value) => {
                void navigate({
                  search: {
                    scope,
                    agent_id: (value as string | null) ?? undefined,
                    q: undefined,
                  },
                  replace: true,
                });
              }}
            >
              <SelectTrigger disabled={agentsLoading}>
                <SelectValue placeholder={t("knowledge.scope.selectAgent")}>
                  {(value) =>
                    value ? agents.find((agent) => agent.id === value)?.name || value : null
                  }
                </SelectValue>
              </SelectTrigger>
              <SelectPopup>
                {agents.map((agent) => (
                  <SelectItem key={agent.id} value={agent.id}>
                    {agent.name || agent.id}
                  </SelectItem>
                ))}
              </SelectPopup>
            </Select>
          </div>
        )}

        {scope === "system_agent" && !agentId ? (
          <SettingsEmptyState
            icon={<Bot className="size-5" />}
            message={t("knowledge.scope.selectAgent")}
            description={t("knowledge.scope.selectAgentDescription")}
          />
        ) : (
          <KnowledgeFilesManager
            scope={scope}
            agentId={agentId}
            q={search.q}
            onQueryChange={(q) => {
              void navigate({
                search: {
                  scope,
                  agent_id: agentId,
                  q: q || undefined,
                },
                replace: true,
              });
            }}
          />
        )}
      </div>
    </div>
  );
}

function KnowledgeFilesManager({ scope, agentId, q, onQueryChange }: KnowledgeFilesManagerProps) {
  const { t, locale } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast } = useToast();
  const [uploadOpen, setUploadOpen] = useState(false);
  const [fileToDelete, setFileToDelete] = useState<KnowledgeFile | null>(null);
  const queryInput = { scope, agentId, q };
  const queryKey = knowledgeFilesQueryKey(queryInput);
  const filesQuery = useInfiniteQuery(knowledgeFilesInfiniteQueryOptions(queryInput));
  const files = filesQuery.data?.pages.flatMap((page) => page.knowledge_files) ?? [];
  const quota = filesQuery.data?.pages[0]?.quota;
  const hasProcessing = files.some((file) => file.status === "processing");

  const deleteMutation = useMutation({
    mutationFn: async (file: KnowledgeFile) => {
      await deleteKnowledgeFile({
        path: { id: file.id },
        throwOnError: true,
      });
      return file;
    },
    onSuccess: (deletedFile) => {
      // Keep the current pages visible while immediately reflecting the
      // released quota. A browser refresh can refill the shortened page.
      queryClient.setQueryData<InfiniteData<KnowledgeFileList, string>>(queryKey, (current) => {
        if (!current) return current;
        return {
          ...current,
          pages: current.pages.map((page) => ({
            ...page,
            knowledge_files: page.knowledge_files.filter((file) => file.id !== deletedFile.id),
            quota: {
              ...page.quota,
              used_files: Math.max(0, page.quota.used_files - 1),
              used_bytes: Math.max(0, page.quota.used_bytes - deletedFile.size_bytes),
            },
          })),
        };
      });
      setFileToDelete(null);
      showToast(t("knowledge.delete.succeeded"));
    },
    onError: (error) => {
      showToast(apiErrorMessage(error, t("knowledge.delete.failed")), "error");
    },
  });

  const handleSearch = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const query = form.get("q");
    onQueryChange(typeof query === "string" ? query.trim() : "");
  };

  const handleBatchComplete = async (succeeded: number, failed: number) => {
    // resetQueries discards extra infinite pages and fetches a fresh first page,
    // which updates both the new processing rows and the aggregate quota.
    await queryClient.resetQueries({ queryKey, exact: true });
    showToast(
      t("knowledge.upload.summary", { succeeded, failed }),
      failed > 0 ? "error" : "success",
    );
  };

  return (
    <section className="space-y-5">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="text-sm text-muted-foreground">
          {quota
            ? t("knowledge.quota", {
                usedFiles: quota.used_files,
                maxFiles: quota.max_files,
                usedBytes: formatBytes(quota.used_bytes, locale),
                maxBytes: formatBytes(quota.max_bytes, locale),
              })
            : t("knowledge.quotaLoading")}
        </div>
        <Button onClick={() => setUploadOpen(true)}>
          <Upload />
          {t("knowledge.upload.action")}
        </Button>
      </div>

      <form className="flex max-w-lg gap-2" onSubmit={handleSearch}>
        <Input
          key={q ?? ""}
          nativeInput
          type="search"
          name="q"
          maxLength={200}
          defaultValue={q ?? ""}
          placeholder={t("knowledge.searchPlaceholder")}
          aria-label={t("knowledge.searchPlaceholder")}
        />
        <Button type="submit" variant="outline">
          <Search />
          {t("common.search")}
        </Button>
      </form>

      {hasProcessing && (
        <Alert variant="info">
          <Info />
          <AlertTitle>{t("knowledge.processing.title")}</AlertTitle>
          <AlertDescription>{t("knowledge.processing.description")}</AlertDescription>
        </Alert>
      )}

      {filesQuery.isError && (
        <Alert variant="error">
          <AlertTitle>{t("knowledge.list.failed")}</AlertTitle>
          <AlertDescription>
            {apiErrorMessage(filesQuery.error, t("knowledge.list.failedDescription"))}
          </AlertDescription>
        </Alert>
      )}

      {filesQuery.isLoading ? (
        <div className="rounded-xl border border-border p-8 text-center text-sm text-muted-foreground">
          {t("common.loading")}
        </div>
      ) : files.length === 0 && !filesQuery.isError ? (
        <SettingsEmptyState
          icon={<Library className="size-5" />}
          message={q ? t("knowledge.emptySearch.title") : t("knowledge.empty.title")}
          description={
            q ? t("knowledge.emptySearch.description") : t("knowledge.empty.description")
          }
          action={
            !q ? (
              <Button onClick={() => setUploadOpen(true)}>
                <Upload />
                {t("knowledge.upload.action")}
              </Button>
            ) : undefined
          }
        />
      ) : files.length > 0 ? (
        <div className="rounded-xl border border-border bg-card">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("knowledge.table.file")}</TableHead>
                <TableHead>{t("common.status")}</TableHead>
                <TableHead>{t("knowledge.table.size")}</TableHead>
                <TableHead>{t("knowledge.table.created")}</TableHead>
                <TableHead className="w-px text-right">{t("common.actions")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {files.map((file) => (
                <TableRow key={file.id}>
                  <TableCell className="max-w-sm whitespace-normal">
                    <p className="break-all font-medium">{file.file_name}</p>
                    {file.status === "failed" && file.error_message && (
                      <p className="mt-1 break-words text-xs leading-relaxed text-destructive">
                        {file.error_message}
                      </p>
                    )}
                  </TableCell>
                  <TableCell>
                    <Badge variant={STATUS_VARIANT[file.status]}>
                      {t(`knowledge.status.${file.status}`)}
                    </Badge>
                  </TableCell>
                  <TableCell className="tabular-nums">
                    {formatBytes(file.size_bytes, locale)}
                  </TableCell>
                  <TableCell>{formatTime(file.created_at)}</TableCell>
                  <TableCell className="text-right">
                    <Button
                      size="icon-sm"
                      variant="destructive-outline"
                      title={t("common.delete")}
                      aria-label={t("knowledge.delete.aria", {
                        name: file.file_name,
                      })}
                      onClick={() => setFileToDelete(file)}
                    >
                      <Trash2 />
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      ) : null}

      {filesQuery.hasNextPage && (
        <Button
          variant="outline"
          loading={filesQuery.isFetchingNextPage}
          onClick={() => void filesQuery.fetchNextPage()}
        >
          {t("knowledge.loadMore")}
        </Button>
      )}

      <KnowledgeUploadDialog
        open={uploadOpen}
        scope={scope}
        agentId={agentId}
        onOpenChange={setUploadOpen}
        onBatchComplete={handleBatchComplete}
      />

      <AlertDialog
        open={Boolean(fileToDelete)}
        onOpenChange={(open) => {
          if (!open && !deleteMutation.isPending) setFileToDelete(null);
        }}
      >
        <AlertDialogPopup>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("knowledge.delete.title")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("knowledge.delete.description", {
                name: fileToDelete?.file_name ?? "",
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogClose
              render={<Button variant="ghost" disabled={deleteMutation.isPending} />}
            >
              {t("common.cancel")}
            </AlertDialogClose>
            <Button
              variant="destructive"
              loading={deleteMutation.isPending}
              disabled={!fileToDelete}
              onClick={() => {
                if (fileToDelete) deleteMutation.mutate(fileToDelete);
              }}
            >
              {t("common.delete")}
            </Button>
          </AlertDialogFooter>
        </AlertDialogPopup>
      </AlertDialog>

      <ToastContainer messages={toasts} />
    </section>
  );
}

function formatBytes(bytes: number, locale: string) {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let value = bytes / 1024;
  let unit = units[0];
  for (let index = 1; value >= 1024 && index < units.length; index++) {
    value /= 1024;
    unit = units[index];
  }
  return `${new Intl.NumberFormat(locale, {
    maximumFractionDigits: value >= 10 ? 1 : 2,
  }).format(value)} ${unit}`;
}
