import { useCallback, useState } from "react";
import { targetValue } from "@/lib/utils";
import { Plus, Trash2 } from "lucide-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useToast } from "@/hooks/use-toast";
import { addProfileConstraint, deleteProfileConstraint } from "@/lib/api-client/sdk.gen";
import { useI18n } from "@/lib/i18n";
import { constraintsQueryOptions } from "@/lib/queries/memories";
import { MemorySection } from "./MemorySection";

interface Props {
  agentId: string;
}
export function ConstraintsSection({ agentId }: Props) {
  const { t } = useI18n();
  const { showToast } = useToast();
  const queryClient = useQueryClient();
  const { data: constraints = [], isLoading, error } = useQuery(constraintsQueryOptions(agentId));
  const [newText, setNewText] = useState("");
  const [adding, setAdding] = useState(false);
  const [deletingId, setDeletingId] = useState<string | null>(null);

  const handleAdd = useCallback(async () => {
    if (!newText.trim()) return;
    setAdding(true);
    try {
      await addProfileConstraint({
        path: { agentId },
        body: { text: newText.trim() },
        throwOnError: true,
      });
      setNewText("");
      void queryClient.invalidateQueries({ queryKey: ["agent-constraints", agentId] });
      void queryClient.invalidateQueries({ queryKey: ["agent-changelog-pages", agentId] });
    } catch {
      showToast(t("memories.constraints.addFailed"), "error");
    } finally {
      setAdding(false);
    }
  }, [agentId, newText, queryClient, showToast, t]);

  const handleDelete = useCallback(
    async (constraintId: string) => {
      setDeletingId(constraintId);
      try {
        await deleteProfileConstraint({
          path: { agentId, constraintId },
          throwOnError: true,
        });
        void queryClient.invalidateQueries({ queryKey: ["agent-constraints", agentId] });
        void queryClient.invalidateQueries({ queryKey: ["agent-changelog-pages", agentId] });
      } catch {
        showToast(t("memories.constraints.deleteFailed"), "error");
      } finally {
        setDeletingId(null);
      }
    },
    [agentId, queryClient, showToast, t],
  );

  return (
    <MemorySection
      title={t("memories.constraints.title")}
      description={t("memories.constraints.description")}
      count={constraints.length}
    >
      {isLoading ? (
        <div className="flex items-center justify-center py-6">
          <div className="size-4 animate-spin rounded-full border-2 border-muted-foreground/30 border-t-muted-foreground" />
        </div>
      ) : error ? (
        <p className="text-sm text-destructive-foreground">{t("memories.constraints.error")}</p>
      ) : constraints.length === 0 ? (
        <p className="text-sm text-muted-foreground italic mb-3">
          {t("memories.constraints.empty")}
        </p>
      ) : (
        <div className="space-y-1.5 mb-3">
          {constraints.map((c) => (
            <div
              key={c.id}
              className="group flex items-start gap-2.5 rounded-lg bg-muted/50 px-3 py-2.5"
            >
              <span className="mt-1.5 size-1.5 shrink-0 rounded-full bg-muted-foreground/40" />
              <span className="flex-1 text-sm leading-relaxed">{c.text}</span>
              <Button
                variant="ghost"
                size="icon-xs"
                onClick={() => void handleDelete(c.id)}
                disabled={deletingId === c.id}
                className="shrink-0 opacity-0 group-hover:opacity-100 transition-opacity text-muted-foreground hover:text-destructive-foreground"
              >
                <Trash2 className="size-3.5" />
              </Button>
            </div>
          ))}
        </div>
      )}
      <div className="flex items-center gap-2">
        <Input
          value={newText}
          onChange={(e) => setNewText(targetValue(e))}
          placeholder={t("memories.constraints.addPlaceholder")}
          className="text-sm"
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              void handleAdd();
            }
          }}
        />
        <Button
          variant="outline"
          size="sm"
          onClick={() => void handleAdd()}
          disabled={adding || !newText.trim()}
          className="shrink-0"
        >
          <Plus className="size-3.5 mr-1" />
          {t("memories.constraints.add")}
        </Button>
      </div>
    </MemorySection>
  );
}
