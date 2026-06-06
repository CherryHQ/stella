import { useCallback, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useNavigate, useParams } from "@tanstack/react-router";
import { Settings } from "lucide-react";
import { agentsQueryOptions } from "@/lib/queries/agents";
import { groupQueryOptions, groupMembersQueryOptions } from "@/lib/queries/groups";
import { Button } from "@/components/ui/button";
import { AppShell } from "@/layouts/AppShell";
import { AgentSidebarContent } from "@/features/sessions/AgentSidebar";
import { GroupChat } from "./GroupChat";
import { GroupSettings } from "./GroupSettings";

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
        <Button
          variant="ghost"
          size="xs"
          onClick={() => setSettingsOpen(true)}
          className="h-7 w-7 rounded-full p-0 text-muted-foreground"
          title="Group settings"
        >
          <Settings className="size-3.5" />
        </Button>
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
