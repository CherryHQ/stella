import { createLazyFileRoute, useParams } from "@tanstack/react-router";
import { GroupChatPage } from "@/features/groups/GroupChatPage";

function GroupChatKeyed() {
  const { groupId } = useParams({ from: "/_app/groups/$groupId" });
  return <GroupChatPage key={groupId} />;
}

export const Route = createLazyFileRoute("/_app/groups/$groupId")({
  component: GroupChatKeyed,
});
