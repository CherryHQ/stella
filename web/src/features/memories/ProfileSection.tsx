import { useCallback, useState } from "react";
import { targetValue } from "@/lib/utils";
import { useQueryClient } from "@tanstack/react-query";
import { Pencil, X } from "lucide-react";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { setProfileMemory } from "@/lib/api-client";
import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/time";
import { MemorySection } from "./MemorySection";

interface Props {
  agentId: string;
  content: string;
  updatedAt: string;
}

export function ProfileSection({ agentId, content: initialContent, updatedAt }: Props) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [content, setContent] = useState(initialContent);
  const [saving, setSaving] = useState(false);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");

  const displayContent = content || initialContent;

  const startEdit = useCallback(() => {
    setDraft(displayContent);
    setEditing(true);
  }, [displayContent]);

  const handleSave = useCallback(async () => {
    setSaving(true);
    try {
      await setProfileMemory({
        path: { agentId },
        body: { content: draft },
        throwOnError: true,
      });
      setContent(draft);
      setEditing(false);
      void queryClient.invalidateQueries({ queryKey: ["agent-memory", agentId] });
      void queryClient.invalidateQueries({ queryKey: ["agent-changelog-pages", agentId] });
    } finally {
      setSaving(false);
    }
  }, [agentId, draft, queryClient]);

  return (
    <MemorySection
      title={t("memories.profile.title")}
      description={t("memories.profile.description")}
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
            rows={10}
            placeholder={t("memories.profile.placeholder")}
            className="font-mono text-sm"
            autoFocus
          />
          <div className="flex items-center gap-2">
            <Button
              onClick={() => void handleSave()}
              disabled={saving || draft === displayContent}
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
      ) : displayContent ? (
        <>
          <MarkdownPreview content={displayContent} variant="card" />
          {updatedAt && (
            <p className="text-xs font-mono text-muted-foreground/50 mt-3">
              {formatTime(updatedAt)}
            </p>
          )}
        </>
      ) : (
        <p className="text-sm text-muted-foreground italic">{t("memories.profile.empty")}</p>
      )}
    </MemorySection>
  );
}
