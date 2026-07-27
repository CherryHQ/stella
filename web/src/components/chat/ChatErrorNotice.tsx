import { AlertCircle } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

interface Props {
  /** The `error` from `useChat`. When undefined, nothing renders. */
  error: Error | undefined;
  /** Layout-only overrides (spacing, width) for the wrapper. */
  className?: string;
}

/**
 * Inline run-level failure notice for chat surfaces. Renders the SDK error at
 * the bottom of the transcript so a failed turn is visible where the user is
 * looking. Sending the next message clears `useChat`'s error, which unmounts
 * this automatically — no dismiss control needed.
 */
export function ChatErrorNotice({ error, className }: Props) {
  const { t } = useI18n();
  if (!error) return null;
  const detail = error.message?.trim();
  return (
    <div className={cn("mx-auto w-full max-w-3xl px-4 pb-3 sm:px-8", className)}>
      <Alert variant="error">
        <AlertCircle />
        <AlertTitle>{t("chat.error.title")}</AlertTitle>
        <AlertDescription>{detail || t("chat.error.generic")}</AlertDescription>
      </Alert>
    </div>
  );
}
