import { useCallback } from "react";
import { useQuery } from "@tanstack/react-query";
import { Info } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Popover, PopoverPopup, PopoverTrigger } from "@/components/ui/popover";
import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";
import { sessionContextItemsOptions } from "@/lib/queries/session-context";
import type { Session } from "@/lib/types";
import { useI18n } from "@/lib/i18n";

function channelLabel(ch: string | null | undefined): string {
  if (!ch) return "";
  const match = ch.match(/:channel:([^:]+)/);
  return match ? match[1] : ch;
}

function formatTime(value?: string | null): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

/**
 * Session metadata — channel, kind, size — behind an info button in the session
 * header. It used to be a section of the right panel; the panel is the file
 * tree now, and this is reference material you glance at, not a workspace.
 */
export function SessionInfoPopover({ session }: { session: Session }) {
  const { t } = useI18n();
  const contextQuery = useQuery(sessionContextItemsOptions(session.agent_id, session.id));
  const meta = contextQuery.data?.meta;
  const loading = contextQuery.isLoading;

  const copyID = useCallback(() => {
    navigator.clipboard.writeText(session.id).catch(console.error);
  }, [session.id]);

  const rows: { label: string; value: string }[] = [
    { label: t("sessions.inspector.channel"), value: channelLabel(session.channel) || "chat" },
    { label: t("sessions.inspector.kind"), value: session.kind },
    { label: t("sessions.inspector.active"), value: formatTime(session.last_active) },
    {
      label: t("sessions.inspector.messages"),
      value: loading ? "…" : (meta?.message_count ?? 0).toLocaleString(),
    },
    {
      label: t("sessions.inspector.tokens"),
      value: loading
        ? "…"
        : `${(meta?.active_token_count ?? 0).toLocaleString()} / ${(meta?.source_token_count ?? 0).toLocaleString()}`,
    },
    {
      label: t("sessions.inspector.longTerm"),
      value: loading
        ? "…"
        : t("sessions.inspector.summaryDepth", { count: meta?.summary_depth ?? 0 }),
    },
  ];

  return (
    <Popover>
      <Tooltip>
        <TooltipTrigger
          render={
            <PopoverTrigger
              render={
                <Button variant="ghost" size="icon-sm" aria-label={t("sessions.info")}>
                  <Info />
                </Button>
              }
            />
          }
        />
        <TooltipPopup side="bottom">{t("sessions.info")}</TooltipPopup>
      </Tooltip>
      <PopoverPopup align="end" className="w-72">
        <dl className="grid grid-cols-[6rem_minmax(0,1fr)] gap-x-3 gap-y-2 text-xs">
          {rows.map((row) => (
            <div key={row.label} className="contents">
              <dt className="truncate text-muted-foreground">{row.label}</dt>
              <dd className="truncate font-medium">{row.value}</dd>
            </div>
          ))}
          <dt className="truncate text-muted-foreground">{t("sessions.inspector.sessionId")}</dt>
          <dd className="truncate font-mono text-muted-foreground">{session.id}</dd>
        </dl>
        <div className="mt-3 flex justify-end">
          <Button type="button" variant="outline" size="xs" onClick={copyID}>
            {t("sessions.inspector.copySessionId")}
          </Button>
        </div>
      </PopoverPopup>
    </Popover>
  );
}
