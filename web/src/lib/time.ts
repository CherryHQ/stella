import i18n from "@/lib/i18n/config";

export function formatTime(ts: string | null | undefined): string {
  if (!ts) return "";
  const d = new Date(ts);
  const now = new Date();
  const diff = now.getTime() - d.getTime();
  // Future timestamps are deadlines, not recent events; render them as an absolute date.
  if (diff < 0) {
    return d.toLocaleString(i18n.language, {
      year: "numeric",
      month: "numeric",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    });
  }
  if (diff < 60_000) return i18n.t("time.justNow");
  if (diff < 3_600_000) return i18n.t("time.minutesAgo", { m: Math.floor(diff / 60_000) });
  if (diff < 86_400_000) return i18n.t("time.hoursAgo", { h: Math.floor(diff / 3_600_000) });
  if (diff < 604_800_000) return i18n.t("time.daysAgo", { d: Math.floor(diff / 86_400_000) });
  return d.toLocaleString(i18n.language, {
    year: "numeric",
    month: "numeric",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}
