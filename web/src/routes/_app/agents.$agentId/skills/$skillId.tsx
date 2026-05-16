import { createFileRoute } from "@tanstack/react-router";
import { SkillEditPage } from "@/features/sessions/pages/SkillEditPage";

export const Route = createFileRoute("/_app/agents/$agentId/skills/$skillId")({
  component: SkillEditPage,
});
