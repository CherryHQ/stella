import { useCallback, useState } from "react";
import { targetValue } from "@/lib/utils";
import { useQueryClient } from "@tanstack/react-query";
import { Pencil, RotateCcw, X } from "lucide-react";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { setProfileSoul } from "@/lib/api-client";
import { useI18n } from "@/lib/i18n";
import type { UserMemory } from "@/lib/types";
import { ConfirmDialog } from "@/features/settings/ConfirmDialog";
import { MemorySection } from "./MemorySection";

type SoulSource = UserMemory["soul_source"];

interface Props {
  agentId: string;
  /** The soul in effect for the viewer, whichever layer supplied it. */
  soul: string;
  /** Which layer supplied `soul`; anything but "user" means no personal override. */
  source: SoulSource;
}

const SOURCE_LABEL_KEY = {
  user: "memories.soul.source.user",
  agent: "memories.soul.source.agent",
  builtin: "memories.soul.source.builtin",
} as const;

/**
 * The agent soul as this viewer experiences it. The agent's default soul lives
 * on the configuration tab (owner-only) and is what shows here until the user
 * customizes it; this section only ever writes the per-user override, and the
 * badge says which layer supplied the text.
 */
export function SoulSection({ agentId, soul: initialSoul, source: initialSource }: Props) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  // The query resolves after first render, so props are the source of truth
  // until this section's own write returns a fresher value.
  const [written, setWritten] = useState<{ soul: string; source: SoulSource } | null>(null);
  const soul = written?.soul ?? initialSoul;
  const source = written?.source ?? initialSource;
  const [saving, setSaving] = useState(false);
  const [editing, setEditing] = useState(false);
  const [confirmingReset, setConfirmingReset] = useState(false);
  const [draft, setDraft] = useState("");

  const isOverride = source === "user";

  const startEdit = useCallback(() => {
    setDraft(soul);
    setEditing(true);
  }, [soul]);

  const write = useCallback(
    async (next: string) => {
      setSaving(true);
      try {
        const { data } = await setProfileSoul({
          path: { agentId },
          body: { soul: next },
          throwOnError: true,
        });
        // The response carries the effective soul after the write, so a reset
        // shows the inherited text immediately instead of an empty section.
        // SAFETY: setProfileSoul returns the refreshed UserMemory on success.
        const refreshed = data as UserMemory;
        setWritten({ soul: refreshed.soul, source: refreshed.soul_source });
        setEditing(false);
        void queryClient.invalidateQueries({ queryKey: ["agent-memory", agentId] });
        void queryClient.invalidateQueries({ queryKey: ["agent-changelog-pages", agentId] });
      } finally {
        setSaving(false);
      }
    },
    [agentId, queryClient],
  );

  return (
    <MemorySection
      title={t("memories.soul.title")}
      description={t("memories.soul.description")}
      defaultOpen
      action={
        !editing ? (
          <div className="flex items-center gap-1">
            {isOverride && (
              <Button
                variant="ghost"
                size="sm"
                disabled={saving}
                onClick={() => setConfirmingReset(true)}
              >
                <RotateCcw className="size-3.5 mr-1.5" />
                {t("memories.soul.reset")}
              </Button>
            )}
            <Button variant="ghost" size="sm" onClick={startEdit}>
              <Pencil className="size-3.5 mr-1.5" />
              {isOverride ? t("common.edit") : t("memories.soul.customize")}
            </Button>
          </div>
        ) : undefined
      }
    >
      <ConfirmDialog
        open={confirmingReset}
        onOpenChange={setConfirmingReset}
        title={t("memories.soul.resetTitle")}
        message={t("memories.soul.resetConfirm")}
        variant="default"
        confirmLabel={t("memories.soul.reset")}
        onConfirm={() => {
          setConfirmingReset(false);
          void write("");
        }}
      />
      {soul && (
        <div className="mb-3">
          <Badge variant={isOverride ? "secondary" : "outline"} size="sm">
            {t(SOURCE_LABEL_KEY[source])}
          </Badge>
        </div>
      )}
      {editing ? (
        <div className="space-y-3">
          <Textarea
            value={draft}
            onChange={(e) => setDraft(targetValue(e))}
            rows={12}
            placeholder={t("memories.soul.placeholder")}
            className="font-mono text-sm"
            autoFocus
          />
          <div className="flex items-center gap-2">
            <Button onClick={() => void write(draft)} disabled={saving || draft === soul} size="sm">
              {saving ? t("common.saving") : t("common.save")}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => setEditing(false)} disabled={saving}>
              <X className="size-3.5 mr-1" />
              {t("common.cancel")}
            </Button>
          </div>
        </div>
      ) : soul ? (
        <MarkdownPreview content={soul} variant="card" />
      ) : (
        <p className="text-sm text-muted-foreground italic">{t("memories.soul.empty")}</p>
      )}
    </MemorySection>
  );
}
