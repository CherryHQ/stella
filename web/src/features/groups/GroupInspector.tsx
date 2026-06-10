import type { UIMessage } from "ai";
import type { ReactNode } from "react";
import type { GroupMember, GroupMessage } from "@/lib/api-client/types.gen";
import { getAgentColor } from "@/lib/agent-colors";
import { formatTime } from "@/lib/time";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

interface Props {
  members: GroupMember[];
  messages: GroupMessage[];
  liveMessages: UIMessage[];
  uploadContext?: { agentId: string; sessionId: string } | null;
  streaming: boolean;
}

export function GroupInspector({
  members,
  messages,
  liveMessages,
  uploadContext,
  streaming,
}: Props) {
  const { t } = useI18n();
  const activeAgentIds = collectActiveAgentIds(liveMessages);
  const messageCounts = countMessagesByActor(messages);
  const lastMessage = messages[0];

  return (
    <aside className="hidden w-80 shrink-0 flex-col overflow-hidden border-l border-border bg-card md:flex">
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
                    <div className="truncate font-mono text-[10px] text-muted-foreground">
                      @{member.agent_id}
                    </div>
                  </div>
                  <span
                    className={cn(
                      "rounded-md px-1.5 py-0.5 font-mono text-[10px]",
                      active || streaming
                        ? "bg-chart-2/10 text-chart-2"
                        : "bg-muted text-muted-foreground",
                    )}
                  >
                    {active ? t("groups.inspector.active") : t("groups.inspector.idle")}
                  </span>
                </div>
              );
            })}
          </div>
        </section>

        <section>
          <SectionTitle>{t("groups.inspector.stats")}</SectionTitle>
          <div className="mt-2 rounded-md border border-border bg-background">
            <StatRow label={t("groups.inspector.messages")} value={messages.length.toString()} />
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
    </aside>
  );
}

function SectionTitle({ children }: { children: ReactNode }) {
  return (
    <h3 className="font-mono text-[10px] font-semibold uppercase tracking-[0.08em] text-muted-foreground">
      {children}
    </h3>
  );
}

function StatRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between gap-3 border-b border-border px-3 py-2 last:border-b-0">
      <span className="truncate text-xs text-muted-foreground">{label}</span>
      <span className="shrink-0 font-mono text-[11px] text-foreground">{value}</span>
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

function collectActiveAgentIds(messages: UIMessage[]) {
  const ids = new Set<string>();
  for (const message of messages) {
    for (const part of message.parts) {
      if (part.type !== "data-agent-info") continue;
      const data = (part as unknown as { data?: { agentId?: string } }).data;
      if (data?.agentId) ids.add(data.agentId);
    }
  }
  return ids;
}
