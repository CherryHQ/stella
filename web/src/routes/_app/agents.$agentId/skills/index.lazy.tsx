import { createLazyFileRoute } from "@tanstack/react-router";
import { SkillsListPage } from "@/features/sessions/pages/SkillsListPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/skills/")({
  component: SkillsListPage,
});
