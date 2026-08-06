import { useCallback, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useNavigate, useParams } from "@tanstack/react-router";
import { Settings } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { agentsQueryOptions } from "@/lib/queries/agents";
import { groupQueryOptions, groupMembersQueryOptions } from "@/lib/queries/groups";
import { Button } from "@/components/ui/button";
import { AppShell } from "@/layouts/AppShell";
import { ConversationSidebar } from "@/features/sessions/ConversationSidebar";
import { GroupBreadcrumb } from "@/features/sessions/AppBreadcrumb";
import { GroupChat } from "./GroupChat";
import { GroupSettings } from "./GroupSettings";

export function GroupChatPage() {
  const { groupId } = useParams({ from: "/_app/groups/$groupId" });
  const navigate = useNavigate();
  const { t } = useI18n();

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
      sidebar={<ConversationSidebar />}
      title={
        <GroupBreadcrumb
          name={group?.group_name || t("groups.group")}
          memberCount={members.length}
        />
      }
      headerActions={
        <Button
          variant="ghost"
          size="icon-sm"
          onClick={() => setSettingsOpen(true)}
          aria-label={t("groups.settings")}
        >
          <Settings />
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
