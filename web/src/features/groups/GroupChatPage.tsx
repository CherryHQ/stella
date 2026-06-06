import { useCallback, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useNavigate, useParams } from "@tanstack/react-router";
import { agentsQueryOptions } from "@/lib/queries/agents";
import { groupQueryOptions, groupMembersQueryOptions } from "@/lib/queries/groups";
import { AppShell } from "@/layouts/AppShell";
import { AgentSidebarContent } from "@/features/sessions/AgentSidebar";
import { GroupChat } from "./GroupChat";
import { GroupSettings } from "./GroupSettings";

function SettingsIcon() {
  return (
    <svg className="size-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8">
      <path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z" />
      <circle cx="12" cy="12" r="3" />
    </svg>
  );
}

export function GroupChatPage() {
  const { groupId } = useParams({ from: "/_app/groups/$groupId" });
  const navigate = useNavigate();

  const { data: agents = [] } = useQuery(agentsQueryOptions);
  const { data: group } = useQuery(groupQueryOptions(groupId));
  const { data: members = [] } = useQuery(groupMembersQueryOptions(groupId));

  const [settingsOpen, setSettingsOpen] = useState(false);

  const handleDeleted = useCallback(() => {
    setSettingsOpen(false);
    void navigate({ to: "/agents/$agentId", params: { agentId: agents[0]?.id ?? "" } });
  }, [navigate, agents]);

  return (
    <AppShell
      sidebar={
        <AgentSidebarContent
          agents={agents}
          agentId={agents[0]?.id ?? ""}
          pathname={`/groups/${groupId}`}
          onAgentChange={(id) => {
            void navigate({ to: "/agents/$agentId", params: { agentId: id } });
          }}
        />
      }
      title={
        <div className="flex items-center gap-2">
          <span className="text-sm font-semibold">{group?.group_name || "Group"}</span>
          <span className="text-xs text-muted-foreground">
            {members.length} member{members.length !== 1 && "s"}
          </span>
        </div>
      }
      headerActions={
        <button
          type="button"
          onClick={() => setSettingsOpen(true)}
          className="grid size-8 place-items-center rounded-lg text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          title="Group settings"
        >
          <SettingsIcon />
        </button>
      }
    >
      <GroupChat groupId={groupId} />
      <GroupSettings
        groupId={groupId}
        groupName={group?.group_name ?? ""}
        open={settingsOpen}
        onClose={() => setSettingsOpen(false)}
        onDeleted={handleDeleted}
      />
    </AppShell>
  );
}
