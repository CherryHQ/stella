import { useCallback, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { createGroup } from "@/lib/api-client/sdk.gen";
import { useI18n } from "@/lib/i18n";
import { agentsQueryOptions } from "@/lib/queries/agents";
import type { Agent } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogPopup,
  DialogTitle,
  DialogFooter,
  DialogHeader,
  DialogDescription,
} from "@/components/ui/dialog";

interface Props {
  open: boolean;
  onClose: () => void;
}

export function CreateGroupDialog({ open, onClose }: Props) {
  const navigate = useNavigate();
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { data: agents = [] } = useQuery(agentsQueryOptions);

  const [name, setName] = useState("");
  const [selectedAgents, setSelectedAgents] = useState<Set<string>>(new Set());
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");

  const toggleAgent = useCallback((id: string) => {
    setSelectedAgents((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);

  const canSubmit = name.trim().length > 0 && selectedAgents.size > 0 && !submitting;

  const handleSubmit = useCallback(async () => {
    if (!canSubmit) return;
    setSubmitting(true);
    setError("");
    try {
      const { data } = await createGroup({
        body: {
          group_name: name.trim(),
          agent_ids: Array.from(selectedAgents),
        },
        throwOnError: true,
      });
      await queryClient.invalidateQueries({ queryKey: ["groups"] });
      onClose();
      if (data?.id) {
        void navigate({ to: "/groups/$groupId", params: { groupId: data.id } });
      }
    } catch {
      setError("Failed to create group.");
    } finally {
      setSubmitting(false);
    }
  }, [canSubmit, name, selectedAgents, queryClient, onClose, navigate]);

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogPopup>
        <DialogHeader>
          <DialogTitle>{t("groups.createGroup")}</DialogTitle>
          <DialogDescription>{t("groups.createDesc")}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-4 py-2">
          <div>
            <label className="mb-1 block text-xs text-muted-foreground">
              {t("groups.groupName")}
            </label>
            <Input
              placeholder="e.g. Research Team"
              value={name}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setName(e.target.value)}
              onKeyDown={(e: React.KeyboardEvent) => {
                if (e.key === "Enter") void handleSubmit();
              }}
              autoFocus
            />
          </div>
          <div>
            <label className="mb-1 block text-xs text-muted-foreground">
              {t("groups.agentsSelected", { count: selectedAgents.size })}
            </label>
            <div className="grid max-h-48 gap-1 overflow-y-auto rounded-lg border border-border p-1.5">
              {agents.map((ag: Agent) => {
                const selected = selectedAgents.has(ag.id);
                return (
                  <button
                    key={ag.id}
                    type="button"
                    onClick={() => toggleAgent(ag.id)}
                    className={cn(
                      "flex items-center gap-2 rounded-md px-2 py-1.5 text-left text-[13px] transition-colors",
                      selected
                        ? "bg-primary/10 text-foreground"
                        : "text-muted-foreground hover:bg-muted hover:text-foreground",
                    )}
                  >
                    <span
                      className={cn(
                        "grid size-4 shrink-0 place-items-center rounded-sm border text-xs transition-colors",
                        selected
                          ? "border-primary bg-primary text-primary-foreground"
                          : "border-input bg-background",
                      )}
                    >
                      {selected && (
                        <svg
                          className="size-3"
                          viewBox="0 0 24 24"
                          fill="none"
                          stroke="currentColor"
                          strokeWidth="3"
                          strokeLinecap="round"
                          strokeLinejoin="round"
                        >
                          <path d="M5.252 12.7 10.2 18.63 18.748 5.37" />
                        </svg>
                      )}
                    </span>
                    <span className="truncate font-medium">{ag.name}</span>
                  </button>
                );
              })}
              {agents.length === 0 && (
                <p className="px-2 py-2 text-xs text-muted-foreground">{t("groups.noAgents")}</p>
              )}
            </div>
          </div>
          {error && <p className="text-xs text-destructive-foreground">{error}</p>}
        </div>
        <DialogFooter>
          <Button variant="ghost" size="sm" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button size="sm" disabled={!canSubmit} onClick={() => void handleSubmit()}>
            {submitting ? t("groups.creating") : t("common.create")}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  );
}
