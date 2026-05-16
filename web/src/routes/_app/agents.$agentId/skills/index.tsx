import { createFileRoute } from "@tanstack/react-router";
import { SkillsListPage } from "@/features/sessions/pages/SkillsListPage";

export const Route = createFileRoute("/_app/agents/$agentId/skills/")({
  component: SkillsListPage,
});
