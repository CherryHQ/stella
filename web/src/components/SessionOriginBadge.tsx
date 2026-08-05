import { Badge } from "@/components/ui/badge";
import { useI18n } from "@/lib/i18n";
import { sessionOriginLabel } from "@/lib/session-origin";
import type { Session } from "@/lib/types";

/**
 * Marks a thread that did not arrive by someone typing in the browser.
 *
 * Neutral on purpose: an origin is not a verdict, so it must not borrow the
 * status colors. It renders nothing at all for ordinary web chats — see
 * `session-origin.ts` for why silence is the point.
 */
export function SessionOriginBadge({ session }: { session: Pick<Session, "kind" | "channel"> }) {
  const { t } = useI18n();
  const label = sessionOriginLabel(session, t);
  if (!label) return null;
  return (
    <Badge variant="secondary" size="sm" className="shrink-0">
      {label}
    </Badge>
  );
}
