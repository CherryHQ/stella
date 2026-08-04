import { createLazyFileRoute } from "@tanstack/react-router";
import { ProfilePage } from "@/features/agents/ProfilePage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/projects/$projectId/profile")({
  component: ProfilePage,
});
