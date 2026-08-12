import { useState } from "react";
import { FoldHorizontal, UnfoldHorizontal } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";
import { getStoredChatWidth, setStoredChatWidth, type ChatWidth } from "@/lib/chat-width";
import { useI18n } from "@/lib/i18n";

/**
 * Switches the conversation column between a reading measure and a working one.
 *
 * The preference lives on the document element, so this holds only enough state
 * to render the right icon — no context, and nothing for the seven components
 * that read the width to subscribe to.
 */
export function ChatWidthToggle() {
  const { t } = useI18n();
  const [width, setWidth] = useState<ChatWidth>(() => getStoredChatWidth());
  const next: ChatWidth = width === "wide" ? "comfortable" : "wide";
  const label = t(next === "wide" ? "sessions.width.wide" : "sessions.width.comfortable");

  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <Button
            variant="ghost"
            size="icon-sm"
            aria-pressed={width === "wide"}
            aria-label={label}
            onClick={() => {
              setStoredChatWidth(next);
              setWidth(next);
            }}
          >
            {width === "wide" ? <FoldHorizontal /> : <UnfoldHorizontal />}
          </Button>
        }
      />
      <TooltipPopup side="bottom">{label}</TooltipPopup>
    </Tooltip>
  );
}
