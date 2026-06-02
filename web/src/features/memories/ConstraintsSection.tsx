import { useCallback, useState } from "react";
import { Plus, Shield, Trash2 } from "lucide-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useI18n } from "@/lib/i18n";
import { constraintsQueryOptions } from "@/lib/queries/memories";

interface Props {
  agentId: string;
}

interface ConstraintEntry {
  id: string;
  text: string;
  created_at: string;
}

export function ConstraintsSection({ agentId }: Props) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { data: constraints = [], isLoading, error } = useQuery(constraintsQueryOptions(agentId));
  const [newText, setNewText] = useState("");
  const [adding, setAdding] = useState(false);
  const [deletingId, setDeletingId] = useState<string | null>(null);

  const handleAdd = useCallback(async () => {
    if (!newText.trim()) return;
    setAdding(true);
    try {
      const { addProfileConstraint } = await import("@/lib/api-client");
      await addProfileConstraint({
        path: { agentId },
        body: { text: newText.trim() },
        throwOnError: true,
      });
      setNewText("");
      void queryClient.invalidateQueries({
        queryKey: ["agent-constraints", agentId],
      });
    } catch (e) {
      console.error(e);
    } finally {
      setAdding(false);
    }
  }, [agentId, newText, queryClient]);

  const handleDelete = useCallback(
    async (constraintId: string) => {
      setDeletingId(constraintId);
      try {
        const { deleteProfileConstraint } = await import("@/lib/api-client");
        await deleteProfileConstraint({
          path: { agentId, constraintId },
          throwOnError: true,
        });
        void queryClient.invalidateQueries({
          queryKey: ["agent-constraints", agentId],
        });
      } catch (e) {
        console.error(e);
      } finally {
        setDeletingId(null);
      }
    },
    [agentId, queryClient],
  );

  return (
    <section className="rounded-2xl border border-border/40 bg-card/45 backdrop-blur-md shadow-2xs overflow-hidden">
      {/* Header */}
      <div className="flex items-start justify-between gap-3 px-6 pt-5 pb-3">
        <div className="flex items-center gap-3 min-w-0">
          <span className="grid size-8 shrink-0 place-items-center rounded-lg bg-amber-500/10 text-amber-600 dark:text-amber-400">
            <Shield className="size-4" />
          </span>
          <div className="min-w-0">
            <h2 className="text-sm font-semibold text-foreground">
              {t("memories.constraints.title")}
              {constraints.length > 0 && (
                <span className="ml-2 text-xs font-normal text-muted-foreground">
                  ({constraints.length})
                </span>
              )}
            </h2>
            <p className="text-xs text-muted-foreground mt-0.5">
              {t("memories.constraints.description")}
            </p>
          </div>
        </div>
      </div>

      {/* Content */}
      <div className="px-6 pb-5">
        {isLoading ? (
          <div className="flex items-center justify-center py-6">
            <div className="size-4 animate-spin rounded-full border-2 border-muted-foreground/30 border-t-muted-foreground" />
          </div>
        ) : error ? (
          <p className="text-sm text-destructive py-4">{t("memories.constraints.error")}</p>
        ) : constraints.length === 0 ? (
          <p className="text-sm text-muted-foreground italic py-4">
            {t("memories.constraints.empty")}
          </p>
        ) : (
          <div className="space-y-1.5 mb-4">
            {(constraints as ConstraintEntry[]).map((c) => (
              <div
                key={c.id}
                className="group flex items-start gap-2 rounded-xl bg-muted/30 px-3 py-2.5 transition-colors hover:bg-muted/50"
              >
                <span className="mt-0.5 size-1.5 shrink-0 rounded-full bg-amber-500/60" />
                <span className="flex-1 text-sm text-foreground/90 leading-relaxed">{c.text}</span>
                <Button
                  variant="ghost"
                  size="icon-xs"
                  onClick={() => void handleDelete(c.id)}
                  disabled={deletingId === c.id}
                  className="shrink-0 opacity-0 group-hover:opacity-100 transition-opacity text-muted-foreground hover:text-destructive"
                >
                  <Trash2 className="size-3.5" />
                </Button>
              </div>
            ))}
          </div>
        )}

        {/* Add constraint */}
        <div className="flex items-center gap-2">
          <Input
            value={newText}
            onChange={(e) => setNewText((e.target as HTMLInputElement).value)}
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
      </div>
    </section>
  );
}
