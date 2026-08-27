import type { ArticleStatus, SourceType } from "@/lib/api-client/types.gen";
import type { MessageKey } from "@/lib/i18n/messages";

export type TFunction = (key: MessageKey, vars?: Record<string, string | number>) => string;

export const SOURCE_TYPES: SourceType[] = ["web", "rss", "github", "pdf", "youtube", "twitter"];

export const CENTER_WIDTH_DEFAULT = 420;
export const CENTER_WIDTH_MIN = 280;
export const CENTER_WIDTH_MAX = 640;

export const SOURCE_LABEL_KEYS = {
  web: "recally.source.web",
  rss: "recally.source.rss",
  github: "recally.source.github",
  pdf: "recally.source.pdf",
  youtube: "recally.source.youtube",
  twitter: "recally.source.twitter",
} satisfies Record<SourceType, MessageKey>;

export const STATUS_LABEL_KEYS = {
  unread: "recally.status.unread",
  read: "recally.status.read",
  archived: "recally.status.archived",
} satisfies Record<ArticleStatus, MessageKey>;

export function formatSavedAt(iso: string, t: (key: MessageKey) => string): string {
  const d = new Date(iso);
  const now = new Date();
  const diffMs = now.getTime() - d.getTime();
  const diffDays = Math.floor(diffMs / (1000 * 60 * 60 * 24));
  if (diffDays === 0) return t("recally.time.today");
  if (diffDays === 1) return t("recally.time.yesterday");
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}
