import { useCallback, useMemo, useState } from "react";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil, Plus, RotateCcw, Trash2 } from "lucide-react";
import type { ComponentsKnowledgeItem } from "@/lib/api-client/types.gen";
import {
  createProfileKnowledge,
  deleteProfileKnowledge,
  restoreProfileKnowledge,
  updateProfileKnowledge,
} from "@/lib/api-client";
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
import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsList, TabsPanel, TabsTab } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import { useToast } from "@/hooks/use-toast";
import { useI18n } from "@/lib/i18n";
import {
  flattenKnowledgePages,
  knowledgeInfiniteQueryOptions,
  type KnowledgeState,
} from "@/lib/queries/memories";
import { formatTime } from "@/lib/time";
import { MemorySection } from "./MemorySection";

interface Props {
  agentId: string;
  state: KnowledgeState;
  onStateChange: (state: KnowledgeState) => void;
}

type EditorState = { mode: "create" } | { mode: "edit"; item: ComponentsKnowledgeItem } | null;

function KnowledgeListSkeleton() {
  return (
    <div className="flex flex-col gap-4 py-4" aria-hidden="true">
      {[0, 1, 2].map((row) => (
        <div key={row} className="flex flex-col gap-3">
          <Skeleton className="h-4 w-full" />
          <Skeleton className="h-4 w-2/3" />
          <Skeleton className="h-5 w-32" />
        </div>
      ))}
    </div>
  );
}

function KnowledgeEditor({
  editor,
  draft,
  saving,
  onDraftChange,
  onOpenChange,
  onSave,
}: {
  editor: EditorState;
  draft: string;
  saving: boolean;
  onDraftChange: (value: string) => void;
  onOpenChange: (open: boolean) => void;
  onSave: () => void;
}) {
  const { t } = useI18n();
  const isEdit = editor?.mode === "edit";
  const error =
    draft.length > 0 && !draft.trim() ? t("memories.knowledge.contentRequired") : undefined;
  const isReflectEdit = editor?.mode === "edit" && editor.item.source === "reflect";

  return (
    <Dialog open={editor !== null} onOpenChange={onOpenChange}>
      <DialogPopup>
        <DialogHeader>
          <DialogTitle>
            {t(isEdit ? "memories.knowledge.editTitle" : "memories.knowledge.createTitle")}
          </DialogTitle>
          <DialogDescription>{t("memories.knowledge.editorDescription")}</DialogDescription>
        </DialogHeader>
        <form
          className="contents"
          onSubmit={(event) => {
            event.preventDefault();
            if (!error) onSave();
          }}
        >
          <DialogPanel>
            <Field invalid={Boolean(error)}>
              <FieldLabel>{t("memories.knowledge.contentLabel")}</FieldLabel>
              <Textarea
                value={draft}
                onChange={(event) => onDraftChange(event.target.value)}
                rows={8}
                placeholder={t("memories.knowledge.placeholder")}
                autoFocus
              />
              {isReflectEdit && (
                <FieldDescription>
                  {t("memories.knowledge.reflectEditDescription")}
                </FieldDescription>
              )}
              {error && <FieldError>{error}</FieldError>}
            </Field>
          </DialogPanel>
          <DialogFooter>
            <DialogClose render={<Button variant="ghost" disabled={saving} />}>
              {t("common.cancel")}
            </DialogClose>
            <Button type="submit" loading={saving} disabled={!draft.trim()}>
              {t(isEdit ? "common.save" : "common.add")}
            </Button>
          </DialogFooter>
        </form>
      </DialogPopup>
    </Dialog>
  );
}

export function KnowledgeSection({ agentId, state, onStateChange }: Props) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { showToast } = useToast();
  const query = useInfiniteQuery(knowledgeInfiniteQueryOptions(agentId, state));
  const items = useMemo(() => flattenKnowledgePages(query.data?.pages), [query.data?.pages]);
  const total = query.data?.pages[0]?.total_size ?? 0;
  const [editor, setEditor] = useState<EditorState>(null);
  const [draft, setDraft] = useState("");
  const [saving, setSaving] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<ComponentsKnowledgeItem | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [restoringId, setRestoringId] = useState<string | null>(null);

  const invalidateMemoryLists = useCallback(async () => {
    // Every mutation can move a record between active/removed and adds a changelog entry.
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["agent-knowledge", agentId] }),
      queryClient.invalidateQueries({ queryKey: ["agent-changelog-pages", agentId] }),
    ]);
  }, [agentId, queryClient]);

  const openCreate = useCallback(() => {
    setDraft("");
    setEditor({ mode: "create" });
  }, []);

  const openEdit = useCallback((item: ComponentsKnowledgeItem) => {
    setDraft(item.content);
    setEditor({ mode: "edit", item });
  }, []);

  const saveKnowledge = useCallback(async () => {
    if (!editor || !draft.trim()) return;
    setSaving(true);
    try {
      if (editor.mode === "create") {
        await createProfileKnowledge({
          path: { agentId },
          body: { content: draft.trim() },
          throwOnError: true,
        });
      } else {
        // Knowledge is atomic, so an edit replaces the current fact with a manual successor.
        await updateProfileKnowledge({
          path: { agentId, factId: editor.item.id },
          body: { content: draft.trim() },
          throwOnError: true,
        });
      }
      setEditor(null);
      await invalidateMemoryLists();
      showToast(
        t(editor.mode === "create" ? "memories.knowledge.created" : "memories.knowledge.updated"),
      );
    } catch {
      showToast(t("memories.knowledge.saveError"), "error");
    } finally {
      setSaving(false);
    }
  }, [agentId, draft, editor, invalidateMemoryLists, showToast, t]);

  const deleteKnowledge = useCallback(async () => {
    if (!deleteTarget) return;
    setDeleting(true);
    try {
      await deleteProfileKnowledge({
        path: { agentId, factId: deleteTarget.id },
        throwOnError: true,
      });
      setDeleteTarget(null);
      await invalidateMemoryLists();
      showToast(t("memories.knowledge.removed"));
    } catch {
      showToast(t("memories.knowledge.removeError"), "error");
    } finally {
      setDeleting(false);
    }
  }, [agentId, deleteTarget, invalidateMemoryLists, showToast, t]);

  const restoreKnowledge = useCallback(
    async (item: ComponentsKnowledgeItem) => {
      setRestoringId(item.id);
      try {
        await restoreProfileKnowledge({
          path: { agentId, factId: item.id },
          throwOnError: true,
        });
        await invalidateMemoryLists();
        showToast(t("memories.knowledge.restored"));
      } catch {
        showToast(t("memories.knowledge.restoreError"), "error");
      } finally {
        setRestoringId(null);
      }
    },
    [agentId, invalidateMemoryLists, showToast, t],
  );

  return (
    <MemorySection
      title={t("memories.knowledge.title")}
      description={t("memories.knowledge.description")}
      count={total}
      defaultOpen
      action={
        state === "active" ? (
          <Button variant="ghost" size="sm" onClick={openCreate}>
            <Plus />
            {t("memories.knowledge.add")}
          </Button>
        ) : undefined
      }
    >
      <Tabs value={state} onValueChange={(value) => onStateChange(value as KnowledgeState)}>
        <TabsList>
          <TabsTab value="active">{t("memories.knowledge.active")}</TabsTab>
          <TabsTab value="removed">{t("memories.knowledge.removedTab")}</TabsTab>
        </TabsList>
        <TabsPanel value={state}>
          {query.isLoading ? (
            <KnowledgeListSkeleton />
          ) : query.isError ? (
            <p className="py-6 text-sm text-destructive-foreground">
              {t("memories.knowledge.loadError")}
            </p>
          ) : items.length === 0 ? (
            <p className="py-6 text-sm text-muted-foreground">
              {t(
                state === "active"
                  ? "memories.knowledge.emptyActive"
                  : "memories.knowledge.emptyRemoved",
              )}
            </p>
          ) : (
            <div className="divide-y divide-border">
              {items.map((item) => (
                <article key={item.id} className="flex flex-col gap-3 py-4 first:pt-2">
                  <MarkdownPreview content={item.content} />
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge variant="secondary">
                      {t(
                        item.source === "reflect"
                          ? "memories.knowledge.sourceReflect"
                          : "memories.knowledge.sourceManual",
                      )}
                    </Badge>
                    {state === "removed" && item.removal_source && (
                      <Badge variant="outline">
                        {t(
                          item.removal_source === "curator"
                            ? "memories.knowledge.removedByCurator"
                            : "memories.knowledge.removedManually",
                        )}
                      </Badge>
                    )}
                    {state === "removed" && item.is_restorable && item.restore_deadline && (
                      <span className="text-xs text-muted-foreground">
                        {t("memories.knowledge.restoreUntil", {
                          time: formatTime(item.restore_deadline),
                        })}
                      </span>
                    )}
                    <span className="text-xs text-muted-foreground">
                      {t(
                        state === "removed"
                          ? "memories.knowledge.removedAt"
                          : "memories.knowledge.updatedAt",
                        { time: formatTime(item.deprecated_at ?? item.updated_at) },
                      )}
                    </span>
                    <div className="ml-auto flex items-center gap-1">
                      {state === "active" ? (
                        <>
                          <Tooltip>
                            <TooltipTrigger
                              render={
                                <Button
                                  variant="ghost"
                                  size="icon-sm"
                                  aria-label={t("common.edit")}
                                  onClick={() => openEdit(item)}
                                />
                              }
                            >
                              <Pencil />
                            </TooltipTrigger>
                            <TooltipPopup>{t("common.edit")}</TooltipPopup>
                          </Tooltip>
                          <Tooltip>
                            <TooltipTrigger
                              render={
                                <Button
                                  variant="ghost"
                                  size="icon-sm"
                                  aria-label={t("common.delete")}
                                  onClick={() => setDeleteTarget(item)}
                                />
                              }
                            >
                              <Trash2 />
                            </TooltipTrigger>
                            <TooltipPopup>{t("common.delete")}</TooltipPopup>
                          </Tooltip>
                        </>
                      ) : item.is_restorable ? (
                        <Button
                          variant="outline"
                          size="sm"
                          loading={restoringId === item.id}
                          onClick={() => void restoreKnowledge(item)}
                        >
                          <RotateCcw />
                          {t("memories.knowledge.restore")}
                        </Button>
                      ) : (
                        <span className="text-xs text-muted-foreground">
                          {t("memories.knowledge.notRestorable")}
                        </span>
                      )}
                    </div>
                  </div>
                </article>
              ))}
            </div>
          )}
          {query.hasNextPage && (
            <div className="flex justify-center pt-4">
              <Button
                variant="outline"
                size="sm"
                loading={query.isFetchingNextPage}
                onClick={() => void query.fetchNextPage()}
              >
                {t("common.loadMore")}
              </Button>
            </div>
          )}
        </TabsPanel>
      </Tabs>

      <KnowledgeEditor
        editor={editor}
        draft={draft}
        saving={saving}
        onDraftChange={setDraft}
        onOpenChange={(open) => {
          if (!open && !saving) setEditor(null);
        }}
        onSave={() => void saveKnowledge()}
      />

      <AlertDialog
        open={deleteTarget !== null}
        onOpenChange={(open) => {
          if (!open && !deleting) setDeleteTarget(null);
        }}
      >
        <AlertDialogPopup>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("memories.knowledge.removeTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("memories.knowledge.removeDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogClose render={<Button variant="ghost" disabled={deleting} />}>
              {t("common.cancel")}
            </AlertDialogClose>
            <Button variant="destructive" loading={deleting} onClick={() => void deleteKnowledge()}>
              {t("common.delete")}
            </Button>
          </AlertDialogFooter>
        </AlertDialogPopup>
      </AlertDialog>
    </MemorySection>
  );
}
