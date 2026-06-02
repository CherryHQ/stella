import { type ReactNode, useCallback, useState } from "react";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import { Pencil, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { useI18n } from "@/lib/i18n";

interface Props {
  icon: ReactNode;
  title: string;
  description: string;
  content: string;
  emptyText: string;
  placeholder: string;
  saving: boolean;
  onSave: (content: string) => Promise<void>;
  meta?: string;
}

export function MemoryCard({
  icon,
  title,
  description,
  content,
  emptyText,
  placeholder,
  saving,
  onSave,
  meta,
}: Props) {
  const { t } = useI18n();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");

  const startEdit = useCallback(() => {
    setDraft(content);
    setEditing(true);
  }, [content]);

  const cancelEdit = useCallback(() => {
    setEditing(false);
  }, []);

  const handleSave = useCallback(async () => {
    await onSave(draft);
    setEditing(false);
  }, [draft, onSave]);

  return (
    <section className="rounded-2xl border border-border/40 bg-card/45 backdrop-blur-md shadow-2xs overflow-hidden">
      {/* Header */}
      <div className="flex items-start justify-between gap-3 px-6 pt-5 pb-3">
        <div className="flex items-center gap-3 min-w-0">
          <span className="grid size-8 shrink-0 place-items-center rounded-lg bg-primary/10 text-primary">
            {icon}
          </span>
          <div className="min-w-0">
            <h2 className="text-sm font-semibold text-foreground">{title}</h2>
            <p className="text-xs text-muted-foreground mt-0.5">{description}</p>
          </div>
        </div>
        {!editing && (
          <Button
            variant="ghost"
            size="sm"
            onClick={startEdit}
            className="shrink-0 text-muted-foreground"
          >
            <Pencil className="size-3.5 mr-1.5" />
            {t("common.edit")}
          </Button>
        )}
      </div>

      {/* Content */}
      <div className="px-6 pb-5">
        {editing ? (
          <div className="space-y-3">
            <Textarea
              value={draft}
              onChange={(e) => setDraft((e.target as HTMLTextAreaElement).value)}
              rows={12}
              placeholder={placeholder}
              className="text-sm font-mono"
              autoFocus
            />
            <div className="flex items-center gap-2">
              <Button
                onClick={() => void handleSave()}
                disabled={saving || draft === content}
                size="sm"
              >
                {saving ? t("common.saving") : t("common.save")}
              </Button>
              <Button variant="ghost" size="sm" onClick={cancelEdit} disabled={saving}>
                <X className="size-3.5 mr-1" />
                {t("common.cancel")}
              </Button>
            </div>
          </div>
        ) : content ? (
          <MarkdownPreview content={content} variant="card" />
        ) : (
          <p className="text-sm text-muted-foreground italic py-4">{emptyText}</p>
        )}

        {meta && !editing && (
          <p className="text-[11px] font-mono text-muted-foreground/60 mt-2">{meta}</p>
        )}
      </div>
    </section>
  );
}
