import { useState, type ReactNode } from "react";
import { Activity } from "lucide-react";
import type { GroupMember, GroupMessage } from "@/lib/api-client/types.gen";
import { getAgentColor } from "@/lib/agent-colors";
import { formatTime } from "@/lib/time";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Sheet, SheetDescription, SheetPopup, SheetTitle } from "@/components/ui/sheet";
import type { GroupTurnEvent } from "./use-group-events";

interface Props {
  members: GroupMember[];
  messages: GroupMessage[];
  activeAgentIds: Set<string>;
  turns: Map<string, GroupTurnEvent>;
  uploadContext?: { agentId: string; sessionId: string } | null;
}

export function GroupInspector({ members, messages, activeAgentIds, turns, uploadContext }: Props) {
  const { t } = useI18n();
  const [mobileOpen, setMobileOpen] = useState(false);
  const messageCounts = countMessagesByActor(messages);
  const lastMessage = messages[0];

  const content = (
    <>
      <div className="border-b border-border px-4 py-3">
        <h2 className="text-sm font-semibold text-foreground">{t("groups.inspector.title")}</h2>
        <p className="mt-0.5 text-xs text-muted-foreground">
          {t("groups.inspector.subtitle", { count: members.length })}
        </p>
      </div>

      <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-4 py-4">
        <section>
          <SectionTitle>{t("groups.inspector.now")}</SectionTitle>
          <div className="mt-2 space-y-2">
            {members.map((member) => {
              const active = activeAgentIds.has(member.agent_id);
              const turnState = turns.get(member.agent_id)?.state;
              return (
                <div
                  key={member.agent_id}
                  className="flex items-center gap-2 rounded-md border border-border bg-background px-2.5 py-2"
                >
                  <span
                    className="size-2.5 shrink-0 rounded-full"
                    style={{ background: getAgentColor(member.agent_id) }}
                  />
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-xs font-medium text-foreground">
                      {member.agent_name || member.agent_id}
                    </div>
                    <div className="truncate font-mono text-xs text-muted-foreground">
                      @{member.agent_id}
                    </div>
                  </div>
                  <span
                    className={cn(
                      "rounded-md px-1.5 py-0.5 font-mono text-xs",
                      active ? "bg-info/10 text-info-foreground" : "bg-muted text-muted-foreground",
                    )}
                    // The reason is why an agent stayed quiet ("freshness",
                    // "hard_cap"); too long for the badge, too useful to drop.
                    title={turns.get(member.agent_id)?.reason}
                  >
                    {turnState
                      ? t(`groups.inspector.turn.${turnState}`)
                      : active
                        ? t("groups.inspector.active")
                        : t("groups.inspector.idle")}
                  </span>
                </div>
              );
            })}
          </div>
        </section>

        <section>
          <SectionTitle>{t("groups.inspector.stats")}</SectionTitle>
          <div className="mt-2 rounded-md border border-border bg-background">
            <StatRow
              label={t("groups.inspector.recentMessages")}
              value={messages.length.toString()}
            />
            <StatRow
              label={t("groups.inspector.lastActive")}
              value={lastMessage ? formatTime(lastMessage.created_at) : t("common.noData")}
            />
            {members.map((member) => (
              <StatRow
                key={member.agent_id}
                label={member.agent_name || member.agent_id}
                value={(messageCounts.get(member.agent_id) ?? 0).toString()}
              />
            ))}
          </div>
        </section>

        <section>
          <SectionTitle>{t("groups.inspector.files")}</SectionTitle>
          <div className="mt-2 rounded-md border border-border bg-background px-3 py-2 text-xs text-muted-foreground">
            {uploadContext
              ? t("groups.inspector.uploadSession", { session: uploadContext.sessionId })
              : t("groups.inspector.noFiles")}
          </div>
        </section>
      </div>
    </>
  );

  return (
    <>
      <aside className="hidden w-80 shrink-0 flex-col overflow-hidden border-l border-border bg-card md:flex">
        {content}
      </aside>
      <Button
        variant="ghost"
        size="xs"
        onClick={() => setMobileOpen(true)}
        className="absolute right-3 top-3 z-10 h-7 w-7 rounded-full bg-background/80 p-0 text-muted-foreground backdrop-blur md:hidden"
        aria-label={t("groups.inspector.title")}
      >
        <Activity className="size-3.5" />
      </Button>
      <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
        <SheetPopup
          side="right"
          showCloseButton={false}
          className="w-[85%] max-w-sm border-l border-border bg-card"
        >
          <SheetTitle className="sr-only">{t("groups.inspector.title")}</SheetTitle>
          <SheetDescription className="sr-only">
            {t("groups.inspector.subtitle", { count: members.length })}
          </SheetDescription>
          <div className="flex h-full flex-col overflow-hidden">{content}</div>
        </SheetPopup>
      </Sheet>
    </>
  );
}

function SectionTitle({ children }: { children: ReactNode }) {
  return (
    <h3 className="font-mono text-xs font-semibold uppercase tracking-[0.08em] text-muted-foreground">
      {children}
    </h3>
  );
}

function StatRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between gap-3 border-b border-border px-3 py-2 last:border-b-0">
      <span className="truncate text-xs text-muted-foreground">{label}</span>
      <span className="shrink-0 font-mono text-xs text-foreground">{value}</span>
    </div>
  );
}

function countMessagesByActor(messages: GroupMessage[]) {
  const counts = new Map<string, number>();
  for (const message of messages) {
    if (message.actor_type !== "agent") continue;
    counts.set(message.actor_id, (counts.get(message.actor_id) ?? 0) + 1);
  }
  return counts;
}
