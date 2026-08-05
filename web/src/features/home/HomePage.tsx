import { useCallback, useState } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronDown } from "lucide-react";
import { createSession } from "@/lib/api-client/sdk.gen";
import type { Agent, Session } from "@/lib/types";
import { agentsQueryOptions } from "@/lib/queries/agents";
import { agentLevelChats, allChatSessionsQueryOptions } from "@/lib/queries/sessions";
import { getAgentColor } from "@/lib/agent-colors";
import { readLastAgentId, writeLastAgentId } from "@/lib/last-agent";
import { relativeTime } from "@/lib/relative-time";
import { useI18n } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/menu";
import { ChatComposer } from "@/features/sessions/ChatComposer";
import { stashPendingMessage } from "@/features/sessions/pendingMessage";

/** How many cross-agent threads the recents list shows. */
const RECENT_LIMIT = 8;

function AgentAvatar({ agent, index }: { agent: Agent; index: number }) {
  return (
    <span
      className="grid size-5 shrink-0 place-items-center rounded-full text-xs font-semibold text-primary-foreground"
      style={{ background: getAgentColor(agent.id, index) }}
    >
      {agent.name[0]?.toUpperCase()}
    </span>
  );
}

/**
 * The front door: pick an agent, say the first thing, and land in a thread that
 * already has your message in it. Everything else on the page is a shortcut
 * back into work that already exists.
 */
export function HomePage() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data: agents = [] } = useQuery(agentsQueryOptions);

  // Which agent the composer targets is a preference, not an address: the home
  // page is one place regardless of the selection, so it stays out of the URL.
  const [selectedId, setSelectedId] = useState(() => readLastAgentId());
  const selectedAgent =
    agents.find((agent) => agent.id === selectedId) ?? (agents[0] as Agent | undefined);

  const [starting, setStarting] = useState(false);

  const startThread = useCallback(
    async (text: string) => {
      if (!selectedAgent || starting) return;
      const agentId = selectedAgent.id;
      setStarting(true);
      try {
        const { data } = await createSession({
          path: { agentId },
          body: { kind: "chat" },
          throwOnError: true,
        });
        writeLastAgentId(agentId);
        // The draft rides in memory, not the URL — it can be arbitrarily long.
        stashPendingMessage(data.id, text);
        await queryClient.invalidateQueries({ queryKey: ["sessions", agentId] });
        void navigate({
          to: "/agents/$agentId/sessions/$sessionId",
          params: { agentId, sessionId: data.id },
        });
      } catch (err) {
        console.error("[home start thread]", err);
        setStarting(false);
      }
    },
    [navigate, queryClient, selectedAgent, starting],
  );

  // One query per agent because the sessions API is per-agent: there is no
  // cross-agent session list yet (#884). Fine for a single-tenant deployment
  // with a handful of agents; replace with the single endpoint when it lands.
  const sessionQueries = useQueries({
    queries: agents.map((agent) => allChatSessionsQueryOptions(agent.id)),
  });
  const recentRows: { session: Session; agent: Agent; index: number }[] = [];
  agents.forEach((agent, index) => {
    const sessions = sessionQueries[index]?.data;
    if (!sessions) return;
    for (const session of agentLevelChats(sessions)) recentRows.push({ session, agent, index });
  });
  const recents = recentRows
    .sort(
      (a, b) =>
        new Date(b.session.last_active).getTime() - new Date(a.session.last_active).getTime(),
    )
    .slice(0, RECENT_LIMIT);

  if (agents.length === 0) {
    return (
      <div className="flex flex-1 flex-col items-center justify-center gap-3 px-6">
        <p className="text-sm text-muted-foreground">{t("sessions.sidebar.noAgents")}</p>
        <Button variant="outline" size="sm" render={<Link to="/settings/agents" />}>
          {t("sidebar.newAgent")}
        </Button>
      </div>
    );
  }

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="mx-auto flex w-full max-w-2xl flex-col gap-8 px-4 py-16 sm:px-6">
        <h1 className="text-center text-2xl font-semibold">{t("home.greeting")}</h1>

        <div className="flex flex-col gap-2">
          {selectedAgent && (
            <div className="flex px-4 sm:px-8">
              <DropdownMenu>
                <DropdownMenuTrigger
                  render={<Button variant="ghost" size="sm" aria-label={t("home.selectAgent")} />}
                >
                  <AgentAvatar agent={selectedAgent} index={agents.indexOf(selectedAgent)} />
                  <span className="truncate">{selectedAgent.name}</span>
                  <ChevronDown />
                </DropdownMenuTrigger>
                <DropdownMenuContent align="start" sideOffset={6}>
                  {agents.map((agent, index) => (
                    <DropdownMenuItem
                      key={agent.id}
                      onClick={() => {
                        setSelectedId(agent.id);
                        writeLastAgentId(agent.id);
                      }}
                    >
                      <AgentAvatar agent={agent} index={index} />
                      {agent.name}
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          )}
          <ChatComposer
            onSend={(text) => void startThread(text)}
            isStreaming={false}
            disabled={starting || !selectedAgent}
            placeholder={t("sessions.composer.placeholder")}
          />
        </div>

        {recents.length > 0 && (
          <div className="flex flex-col gap-1">
            <p className="px-2 text-xs text-muted-foreground">{t("sidebar.recentThreads")}</p>
            {recents.map(({ session, agent, index }) => (
              <Link
                key={session.id}
                to="/agents/$agentId/sessions/$sessionId"
                params={{ agentId: agent.id, sessionId: session.id }}
                className="flex min-w-0 items-center gap-2 rounded-md px-2 py-2 transition-colors hover:bg-muted"
              >
                <AgentAvatar agent={agent} index={index} />
                <span className="min-w-0 flex-1 truncate text-sm">
                  {session.title || t("sessions.untitled")}
                </span>
                <time className="shrink-0 font-mono text-xs text-muted-foreground">
                  {relativeTime(session.last_active)}
                </time>
              </Link>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
