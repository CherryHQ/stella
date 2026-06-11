import type { ArticleStatus } from "@/lib/api-client/types.gen";
import type { TFunction } from "../constants";
import { STATUS_LABEL_KEYS } from "../constants";

export function StatusBadge({ status, t }: { status: ArticleStatus; t: TFunction }) {
  const classes =
    status === "unread"
      ? "border-info/20 bg-info/10 text-info-foreground"
      : status === "read"
        ? "border-success/20 bg-success/10 text-success-foreground"
        : "border-border bg-muted text-muted-foreground";

  return (
    <span className={`px-1.5 py-0.5 rounded-full border font-mono text-xs ${classes}`}>
      {t(STATUS_LABEL_KEYS[status])}
    </span>
  );
}
