import { useState } from "react";
import { Copy, Check } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/lib/i18n";

/**
 * Reveal a message's meta/action row on pointer hover, but keep it permanently
 * visible on touch devices (which have no hover) so the actions stay reachable.
 */
export const REVEAL_ON_HOVER =
  "opacity-100 transition-opacity [@media(hover:hover)]:opacity-0 [@media(hover:hover)]:group-hover:opacity-100";

/** Copy action for a chat message. Renders nothing for empty text. */
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
    <Button
      variant="ghost"
      size="icon-xs"
      onClick={onCopy}
      title={t("sessions.transcript.copy")}
      className={className}
    >
      {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
    </Button>
  );
}
