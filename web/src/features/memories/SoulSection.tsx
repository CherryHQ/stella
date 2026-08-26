import { useCallback, useState } from "react";
import { targetValue } from "@/lib/utils";
import { useQueryClient } from "@tanstack/react-query";
import { Pencil, X } from "lucide-react";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { setProfileSoul } from "@/lib/api-client";
import { useI18n } from "@/lib/i18n";
import { MemorySection } from "./MemorySection";

interface Props {
  agentId: string;
  soul: string;
}

export function SoulSection({ agentId, soul: initialSoul }: Props) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [soul, setSoul] = useState(initialSoul);
  const [saving, setSaving] = useState(false);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");

  const displaySoul = soul || initialSoul;

  const startEdit = useCallback(() => {
    setDraft(displaySoul);
    setEditing(true);
  }, [displaySoul]);

  const handleSave = useCallback(async () => {
    setSaving(true);
    try {
      await setProfileSoul({
        path: { agentId },
        body: { soul: draft },
        throwOnError: true,
      });
      setSoul(draft);
      setEditing(false);
      void queryClient.invalidateQueries({ queryKey: ["agent-memory", agentId] });
      void queryClient.invalidateQueries({ queryKey: ["agent-changelog-pages", agentId] });
    } finally {
      setSaving(false);
    }
  }, [agentId, draft, queryClient]);

  return (
    <MemorySection
      title={t("memories.soul.title")}
      description={t("memories.soul.description")}
      defaultOpen
      action={
        !editing ? (
          <Button variant="ghost" size="sm" onClick={startEdit}>
            <Pencil className="size-3.5 mr-1.5" />
            {t("common.edit")}
          </Button>
        ) : undefined
      }
    >
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
            <Button
              onClick={() => void handleSave()}
              disabled={saving || draft === displaySoul}
              size="sm"
            >
              {saving ? t("common.saving") : t("common.save")}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => setEditing(false)} disabled={saving}>
              <X className="size-3.5 mr-1" />
              {t("common.cancel")}
            </Button>
          </div>
        </div>
      ) : displaySoul ? (
        <MarkdownPreview content={displaySoul} variant="card" />
      ) : (
        <p className="text-sm text-muted-foreground italic">{t("memories.soul.empty")}</p>
      )}
    </MemorySection>
  );
}
