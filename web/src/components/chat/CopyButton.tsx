import { useState } from "react";
import { Copy, Check } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** Hover-revealed copy action for a chat message. Renders nothing for empty text. */
export function CopyButton({ text, className }: { text: string; className?: string }) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);
  if (!text.trim()) return null;

  const onCopy = () => {
    void navigator.clipboard?.writeText(text);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1500);
  };

  return (
    <span
      className={cn("inline-flex opacity-0 transition-opacity group-hover:opacity-100", className)}
    >
      <Button variant="ghost" size="icon-xs" onClick={onCopy} title={t("sessions.transcript.copy")}>
        {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
      </Button>
    </span>
  );
}
